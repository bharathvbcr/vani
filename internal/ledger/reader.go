package ledger

import (
	"crypto/sha256"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Reader is a lock-free fold of dir/events.jsonl that holds no description in
// memory and keeps its fold between reads, so a long-lived reader (vani jobs
// serve) pays only for the events appended since its last read, and a one-shot
// reader with a checkpoint (checkpoint.go) pays only for those appended since
// the checkpoint was written.
//
// It folds the way ReadSnapshotScoped always has: no lock is taken and nothing
// in the ledger directory is written; a torn last line is left out and read
// again once it is whole; mid-file corruption is ErrCorrupt. Descriptions are
// cut out of each line as it is folded (lean.go) and read back on demand by
// Body or Hydrate, verified against the fingerprint they were folded with.
//
// Advance keeps the fold current. The events file is append-only for every
// writer except two, and Advance detects both: a writer that rolls back a line
// after a failed write (File.Append truncates to its last good size, and may
// then append a different line where the old one was), and rekey, which
// renames a new file over the old. A file that was replaced, shrank, or no
// longer holds the last line this Reader folded, byte for byte, is folded
// again from the start. An in-place edit of earlier lines that leaves the last
// one intact is outside that contract and is not detected by Advance (the
// checkpoint loader samples more lines; see checkpoint.go).
//
// A Reader is safe for concurrent use.
type Reader struct {
	mu   sync.Mutex
	path string
	opts ReaderOptions
	ev   *os.File
	id   FileID
	recs map[posting.Key]*slot
	lean *leanState
	// good is the end of the last intact line folded; last is that line's start
	// and lastSum its SHA-256, which Advance checks before reading on.
	good, last int64
	lastSum    [32]byte
	torn       bool
	stale      bool     // a failed fold left the state unknown: Advance folds again
	cache      []Record // sorted records, nil until asked for after a change
	info       ReaderInfo
}

// ReaderOptions configures OpenReader.
type ReaderOptions struct {
	// Checkpoint is the file a Reader starts from and saves to (checkpoint.go).
	// Empty: no checkpoint is read or written.
	Checkpoint string
	// SaveAfter saves a new checkpoint (best effort) after a fold that read at
	// least this many bytes. Zero never saves on its own; SaveCheckpoint still does.
	SaveAfter int64
}

// ReaderInfo says how a Reader's fold was built, for a caller to report.
type ReaderInfo struct {
	// Checkpoint is how the last full fold started: "none" (no checkpoint
	// configured), "loaded", or "not used: <why>".
	Checkpoint string `json:"checkpoint"`
	// Folded is how many bytes of events the last OpenReader or Advance read.
	Folded int64 `json:"folded"`
	// Refolds counts the full folds Advance had to make.
	Refolds int `json:"refolds,omitempty"`
	// Saved is the last checkpoint save: "" (none tried), "saved",
	// "skipped: <why>" or "failed: <why>". A failed save costs only speed.
	Saved string `json:"saved,omitempty"`
}

// OpenReader folds dir/events.jsonl, from opts.Checkpoint when it holds a
// valid checkpoint of this file. A missing events file is an error wrapping
// os.ErrNotExist, and nothing is created.
func OpenReader(dir string, opts ReaderOptions) (*Reader, error) {
	r := &Reader{path: filepath.Join(dir, eventsName), opts: opts}
	if err := r.refold(true); err != nil {
		r.closeFile()
		return nil, err
	}
	r.maybeSave()
	return r, nil
}

// refold opens the events file again and folds it from the checkpoint (when
// allowed and valid) or from its first byte.
func (r *Reader) refold(useCheckpoint bool) error {
	r.closeFile()
	ev, err := os.Open(r.path)
	if err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	fi, err := ev.Stat()
	if err != nil {
		ev.Close()
		return fmt.Errorf("ledger: %w", err)
	}
	r.ev, r.id = ev, fileIDOf(fi)
	r.reset()
	r.info.Checkpoint = "none"
	if useCheckpoint && r.opts.Checkpoint != "" {
		if why := r.loadCheckpoint(fi.Size()); why != "" {
			r.info.Checkpoint = "not used: " + why
			r.reset()
		} else {
			r.info.Checkpoint = "loaded"
		}
	}
	return r.foldTail()
}

func (r *Reader) reset() {
	r.recs, r.lean = map[posting.Key]*slot{}, newLeanState()
	r.good, r.last, r.lastSum, r.torn, r.cache = 0, 0, [32]byte{}, false, nil
}

// foldTail folds every line from r.good to the end of the file.
func (r *Reader) foldTail() error {
	from := r.good
	src := io.NewSectionReader(r.ev, from, math.MaxInt64-from)
	good, last, torn, err := replayFrom(src, r.recs, nil, r.lean, from)
	if err != nil {
		r.stale = true
		return err
	}
	r.info.Folded = good - from
	if good > from {
		sum, err := lineSum(r.ev, last, good)
		if err != nil {
			r.stale = true
			return fmt.Errorf("ledger: %w", err)
		}
		r.last, r.lastSum, r.cache = last, sum, nil
	}
	r.good, r.torn, r.stale = good, torn, false
	return nil
}

// Advance folds the events appended since the last read, or the whole file
// again when it was replaced, shrank, or no longer holds the last line folded.
// changed reports whether the records may differ from the last Snapshot.
func (r *Reader) Advance() (changed bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ev == nil {
		return false, ErrClosed
	}
	fi, err := os.Stat(r.path)
	if err != nil {
		return false, fmt.Errorf("ledger: %w", err)
	}
	if why := r.mismatch(fi); why != "" {
		r.info.Refolds++
		if err := r.refold(true); err != nil {
			return false, fmt.Errorf("ledger: %s, and folding it again failed: %w", why, err)
		}
		r.maybeSave()
		return true, nil
	}
	if fi.Size() == r.good {
		r.info.Folded = 0
		return false, nil
	}
	before := r.good
	if err := r.foldTail(); err != nil {
		return false, err
	}
	r.maybeSave()
	return r.good != before, nil
}

// mismatch is why the fold no longer describes the file at path, or "".
func (r *Reader) mismatch(fi os.FileInfo) string {
	switch {
	case r.stale:
		return "a previous read failed"
	case fileIDOf(fi) != r.id:
		return "the events file was replaced"
	case fi.Size() < r.good:
		return "the events file shrank"
	case r.good > 0:
		if sum, err := lineSum(r.ev, r.last, r.good); err != nil || sum != r.lastSum {
			return "the last line read changed"
		}
	}
	return ""
}

// Snapshot returns the records as folded, sorted by Key, with no description
// where one was cut out (BodyEvicted). The slice is the caller's: its records
// may be changed, but their Annotations maps are shared with the Reader and
// must not be.
func (r *Reader) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil {
		r.cache = sorted(r.recs)
	}
	return Snapshot{Records: slices.Clone(r.cache), Torn: r.torn}
}

// Body reads k's description back from the events file. A body that cannot be
// read verbatim (no line for it, or the line changed) is ErrBodyUnavailable.
func (r *Reader) Body(k posting.Key) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ev == nil {
		return "", ErrClosed
	}
	return r.lean.read(r.ev, k, r.good)
}

// Hydrate reads back, into recs, the description of every record keep reports
// true for that has none (BodyEvicted). The first body that cannot be read
// verbatim stops it with ErrBodyUnavailable.
func (r *Reader) Hydrate(recs []Record, keep func(Record) bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ev == nil {
		return ErrClosed
	}
	for i := range recs {
		rec := &recs[i]
		if !rec.BodyEvicted || !keep(*rec) {
			continue
		}
		d, err := r.lean.read(r.ev, rec.Key, r.good)
		if err != nil {
			return err
		}
		rec.Posting.Description, rec.BodyEvicted = d, false
	}
	return nil
}

// BodyFP is the BodyFingerprint of k's current description, whether or not it
// is held in memory; false when no event carried a posting for k.
func (r *Reader) BodyFP(k posting.Key) ([16]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fp, ok := r.lean.fps[k]
	return fp, ok
}

// Info is how the fold was built.
func (r *Reader) Info() ReaderInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.info
}

// Close releases the events file. It is safe to call twice.
func (r *Reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closeFile()
}

func (r *Reader) closeFile() error {
	if r.ev == nil {
		return nil
	}
	err := r.ev.Close()
	r.ev = nil
	return err
}

// lineSum is the SHA-256 of the bytes [off, end) of rd, one line at most.
func lineSum(rd io.ReaderAt, off, end int64) ([32]byte, error) {
	n := end - off
	if off < 0 || n <= 0 || n > MaxLineBytes {
		return [32]byte{}, fmt.Errorf("line at %d..%d is out of bounds", off, end)
	}
	buf := make([]byte, n)
	if _, err := rd.ReadAt(buf, off); err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(buf), nil
}
