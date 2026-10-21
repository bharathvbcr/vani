package ledger

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const (
	// SchemaVersion is the only event version this implementation reads or writes.
	SchemaVersion = 1
	// MaxLineBytes bounds one serialised event, newline included.
	MaxLineBytes = 1 << 20
	// MaxBatchEvents bounds the events in one AppendBatch.
	MaxBatchEvents = 4096
	// MaxBatchBytes bounds the serialised size of one AppendBatch.
	MaxBatchBytes = 16 << 20
	// MissedRunsKey is the reserved annotation that carries Record.MissedRuns.
	MissedRunsKey = "missed_runs"

	eventsName = "events.jsonl"
	lockName   = "lock"
)

var (
	// ErrLocked means another Vani process holds the ledger directory.
	ErrLocked = errors.New("ledger: directory is locked by another process")
	// ErrLineTooLong means an event serialises past MaxLineBytes.
	ErrLineTooLong = errors.New("ledger: event line exceeds size limit")
	// ErrBatchTooLarge means a batch exceeds MaxBatchEvents or MaxBatchBytes.
	ErrBatchTooLarge = errors.New("ledger: batch exceeds size limit")
	// ErrInvalidEvent means an event failed validation.
	ErrInvalidEvent = errors.New("ledger: invalid event")
	// ErrCorrupt means the file holds invalid data before its last line.
	ErrCorrupt = errors.New("ledger: corrupt events file")
	// ErrClosed means the ledger was used after Close.
	ErrClosed = errors.New("ledger: closed")
)

// File is the JSONL-backed Ledger. The file is the source of truth; records
// are folded in memory and rebuilt by Open.
type File struct {
	// Recovered is true when Open found and ignored a torn last line: one
	// with no newline, or one that is not JSON. The bad bytes are cut off the
	// file on the next successful Append. A whole JSON line that is not a
	// valid event is never torn; Open refuses it as ErrCorrupt.
	Recovered bool

	mu       sync.Mutex
	events   *os.File
	lock     *os.File
	recs     map[posting.Key]*slot // each key's record and where its body lives; see evict.go
	goodSize int64                 // end of the last intact line
	closed   bool
	broken   error // set when the file past goodSize is unknown; every later Append returns it
}

// errBroken is a ledger whose events file can no longer be trusted past the
// last line it acknowledged. Get and All still answer from memory, which holds
// only acknowledged events; Close and a fresh Open recover.
var errBroken = errors.New("ledger: events file is in an unknown state after a failed write; reopen the ledger")

var _ Ledger = (*File)(nil)

// syncDir is atomicfile.SyncDir; a variable only so a test can count its calls.
var syncDir = atomicfile.SyncDir

// Open locks dir, creating it (0700) if needed, and replays dir/events.jsonl.
func Open(dir string) (*File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("ledger: create dir: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, lockName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("ledger: open lock: %w", err)
	}
	if err := filelock.TryLock(lock, true); err != nil {
		lock.Close()
		if errors.Is(err, filelock.ErrWouldBlock) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("ledger: lock: %w", err)
	}
	evPath := filepath.Join(dir, eventsName)
	ev, created, err := atomicfile.OpenAppend(evPath, 0)
	if err != nil {
		lock.Close()
		return nil, fmt.Errorf("ledger: open events: %w", err)
	}
	if created {
		// Appends fsync the file, not the directory entry that names it.
		if err := atomicfile.SyncNew(evPath, syncDir); err != nil {
			ev.Close()
			lock.Close()
			return nil, fmt.Errorf("ledger: %w", err)
		}
	}
	f := &File{events: ev, lock: lock, recs: map[posting.Key]*slot{}}
	if err := f.replay(); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// readLine returns one line without its newline. hadNL is false for a final
// line the file ended in the middle of. A line past MaxLineBytes is an error.
// A line that fits the reader's buffer is returned as a view of it, valid only
// until the next read from r (a long line is copied).
func readLine(r *bufio.Reader) (line []byte, raw int, hadNL bool, err error) {
	for {
		chunk, e := r.ReadSlice('\n')
		raw += len(chunk)
		if raw > MaxLineBytes {
			return nil, raw, false, ErrLineTooLong
		}
		if e == nil && line == nil {
			return chunk[:len(chunk)-1], raw, true, nil
		}
		line = append(line, chunk...)
		switch {
		case e == nil:
			return bytes.TrimSuffix(line, []byte("\n")), raw, true, nil
		case errors.Is(e, bufio.ErrBufferFull):
			continue
		case errors.Is(e, io.EOF):
			if raw == 0 {
				return nil, 0, false, io.EOF
			}
			return line, raw, false, nil
		default:
			return nil, raw, false, e
		}
	}
}

func (f *File) replay() error {
	off, torn, err := replay(f.events, f.recs, f.events)
	f.goodSize, f.Recovered = off, torn
	if err == nil {
		packBodies(f.recs)
	}
	return err
}

const (
	packChunk = 64 << 10 // bytes of description per packed chunk
	packMax   = packChunk / 2
)

// packBodies copies the descriptions still resident after replay into dense
// chunks. Replay decoded every body, closed or not, into the same size class, so
// the ones that stay sit one to a span among freed neighbours and keep all those
// spans in use. The copies are packed back to back and the originals' spans
// empty out. A chunk is freed once every body in it is gone, so this only
// applies to what Open itself loaded: bodies added later are separate strings.
//
// Cost and benefit (BenchmarkOpen20k, 20k postings, 80% dead): Open runs once
// per process, and this pass adds about 14 ms to it (+9% min-of-5 on a loaded
// machine) for 66 MB less resident memory afterwards. Append, AppendBatch and
// ReconcileRun are neutral or faster.
func packBodies(recs map[posting.Key]*slot) {
	var todo []*slot
	size := 0
	flush := func() {
		var sb strings.Builder
		sb.Grow(size)
		for _, s := range todo {
			sb.WriteString(s.Posting.Description)
		}
		all, off := sb.String(), 0
		for _, s := range todo {
			n := len(s.Posting.Description)
			s.Posting.Description = all[off : off+n]
			off += n
		}
		todo, size = todo[:0], 0
	}
	for _, s := range recs {
		n := len(s.Posting.Description)
		if n == 0 || n > packMax {
			continue
		}
		if size+n > packChunk {
			flush()
		}
		todo, size = append(todo, s), size+n
	}
	flush()
}

// replay folds every intact event line of r into recs. It returns the end of
// the last intact line and whether a torn last line (no newline, or a final
// line that is not JSON at all, as a crash mid-write can leave) was ignored.
// Anything bad before the last line is ErrCorrupt, and so is a last line that
// is whole JSON but not a valid event: that is a writer's finished line (a
// newer schema, say), and treating it as torn would cut it off the file.
//
// With rd (the file src reads, as an io.ReaderAt) it also evicts and restores
// descriptions as evict.go describes; without, every body stays.
func replay(src io.Reader, recs map[posting.Key]*slot, rd io.ReaderAt) (good int64, torn bool, err error) {
	return replayWith(src, recs, rd, nil)
}

// replayWith is replay that, given lean, cuts each posting's description out of
// its line before decoding (lean.go): the record folds with an empty
// Description and BodyEvicted set, and lean remembers the line to read it back
// from. lean is for the lock-free snapshot reader and is exclusive with rd.
func replayWith(src io.Reader, recs map[posting.Key]*slot, rd io.ReaderAt, lean *leanState) (good int64, torn bool, err error) {
	bufSize := 64 * 1024
	if lean != nil {
		bufSize = 256 * 1024 // the scan reads every byte and decodes few
	}
	r := bufio.NewReaderSize(src, bufSize)
	in := interner{}
	for n := 1; ; n++ {
		line, raw, hadNL, err := readLine(r)
		if errors.Is(err, io.EOF) {
			return good, false, nil
		}
		if err != nil {
			return good, false, fmt.Errorf("%w: line %d: %v", ErrCorrupt, n, err)
		}
		if !hadNL {
			return good, true, nil // torn tail: the writer died (or is still writing) before the newline
		}
		var e Event
		dec, cut := line, 0
		if lean != nil {
			if l, c, ok := stripDescription(line, lean.buf); ok {
				dec, cut, lean.buf = l, c, l
			}
		}
		perr := json.Unmarshal(dec, &e)
		if perr == nil {
			perr = validate(e)
		}
		if perr != nil {
			valid := json.Valid(line) // before Peek: line may be a view of r's buffer
			if _, pe := r.Peek(1); errors.Is(pe, io.EOF) && !valid {
				return good, true, nil
			}
			return good, false, fmt.Errorf("%w: line %d: %v", ErrCorrupt, n, perr)
		}
		s := slotFor(recs, e)
		fold(&s.Record, e, in)
		if cut > 0 && e.Posting != nil {
			s.BodyEvicted = true
			lean.note(e.Key, good, raw, line)
		}
		if rd != nil {
			if err := s.apply(e, good, raw, nil, nil, rd, good); err != nil {
				return good, false, fmt.Errorf("%w: line %d: %v", ErrCorrupt, n, err)
			}
		}
		good += int64(raw)
	}
}

// Snapshot is the folded state read from a ledger directory without its lock.
type Snapshot struct {
	Records []Record // sorted by Key
	// Torn is true when the last line was incomplete: a writer was mid-append
	// or died. The snapshot holds every event before it.
	Torn bool
}

// ReadSnapshot folds dir/events.jsonl read-only, without the directory lock,
// so a reader works while a run or the daemon holds the ledger. The events
// file is append-only and each line is written whole before the next, so
// every line before the last newline is final; a partial last line is left
// out, never repaired (that is the lock holder's job). It never creates,
// truncates or writes anything.
func ReadSnapshot(dir string) (Snapshot, error) {
	ev, err := os.Open(filepath.Join(dir, eventsName))
	if err != nil {
		return Snapshot{}, fmt.Errorf("ledger: %w", err)
	}
	defer ev.Close()
	recs := map[posting.Key]*slot{}
	_, torn, err := replay(ev, recs, nil) // no eviction: a one-shot read returns every body
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Records: sorted(recs), Torn: torn}, nil
}

func validate(e Event) error {
	switch {
	case e.V != SchemaVersion:
		return fmt.Errorf("%w: version %d", ErrInvalidEvent, e.V)
	case e.Key == "" || e.Key.ATS() == "":
		return fmt.Errorf("%w: key %q", ErrInvalidEvent, e.Key)
	case !e.Status.Valid():
		return fmt.Errorf("%w: status %q", ErrInvalidEvent, e.Status)
	case e.At.IsZero():
		return fmt.Errorf("%w: zero time", ErrInvalidEvent)
	case e.Posting != nil && e.Posting.Key != e.Key:
		// A posting filed under another key would silently merge two postings.
		return fmt.Errorf("%w: posting key %q under event key %q", ErrInvalidEvent, e.Posting.Key, e.Key)
	}
	if v, ok := e.Annotations[MissedRunsKey]; ok {
		if n, err := strconv.Atoi(v); err != nil || n < 0 {
			return fmt.Errorf("%w: %s %q", ErrInvalidEvent, MissedRunsKey, v)
		}
	}
	return nil
}

// slotFor returns the slot e folds into, creating it on the key's first event.
func slotFor(recs map[posting.Key]*slot, e Event) *slot {
	s, ok := recs[e.Key]
	if !ok {
		s = &slot{Record: Record{Key: e.Key, Annotations: map[string]string{}, FirstSeen: e.At}}
		recs[e.Key] = s
	}
	return s
}

// interner makes equal short strings share one allocation. Replay decodes every
// line into fresh strings, and annotation names and values, companies and
// locations repeat across thousands of records; keeping one copy of each cuts
// what the replayed ledger holds. It is bounded (internMax strings of at most
// internLen bytes) and nil means no interning.
type interner map[string]string

const (
	internMax = 8192
	internLen = 64
)

func (in interner) of(s string) string {
	if in == nil || len(s) > internLen {
		return s
	}
	if c, ok := in[s]; ok {
		return c
	}
	if len(in) < internMax {
		in[s] = s
	}
	return s
}

// fold applies a validated event to the in-memory state.
func fold(r *Record, e Event, in interner) {
	r.LastEvent = e.At
	r.Status = e.Status
	if e.Posting != nil {
		r.Posting = *e.Posting
		r.Posting.Company, r.Posting.Location, r.Posting.Department = in.of(r.Posting.Company), in.of(r.Posting.Location), in.of(r.Posting.Department)
		r.BodyEvicted = false
		r.MissedRuns = 0
	}
	for k, v := range e.Annotations {
		if k == MissedRunsKey {
			r.MissedRuns, _ = strconv.Atoi(v) // validated
			continue
		}
		if v == "" {
			delete(r.Annotations, k) // an empty value forgets the key
			continue
		}
		r.Annotations[in.of(k)] = in.of(v)
	}
}

// invalidUTF8Mark is how encoding/json writes a byte that is not valid UTF-8:
// the escape of U+FFFD, in place of the byte.
var invalidUTF8Mark = []byte(`�`)

// wireForm returns the event as replay will read it back from line (e's
// encoding). Marshal replaces every invalid UTF-8 byte of a string with U+FFFD,
// so an event holding such text decodes to different text than it was given;
// folding e as given would make memory differ from the next Open (and a body
// fingerprint taken of the raw text could never match the line). Folding the
// decoded form keeps live and replayed records identical. Only a line carrying
// the marker (or a literal backslash-ufffd, which merely costs a redundant
// decode) is decoded again; any other event is its own wire form.
func wireForm(e Event, line []byte) (Event, error) {
	if !bytes.Contains(line, invalidUTF8Mark) {
		return e, nil
	}
	var w Event
	if err := json.Unmarshal(line, &w); err != nil {
		return e, fmt.Errorf("ledger: re-read %s: %w", e.Key, err)
	}
	if err := validate(w); err != nil {
		return e, err
	}
	return w, nil
}

// Append validates e, writes it durably, then folds it into memory.
func (f *File) Append(ctx context.Context, e Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validate(e); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("ledger: marshal: %w", err)
	}
	if e, err = wireForm(e, line); err != nil {
		return err
	}
	line = append(line, '\n')
	if len(line) > MaxLineBytes {
		return ErrLineTooLong
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrClosed
	}
	if f.broken != nil {
		return f.broken
	}
	pre, err := f.prepare([]Event{e})
	if err != nil {
		return err
	}
	if f.Recovered {
		// Cut the torn tail so the new line starts on a clean boundary.
		if err := f.events.Truncate(f.goodSize); err != nil {
			return fmt.Errorf("ledger: truncate torn tail: %w", err)
		}
		f.Recovered = false
	}
	if _, err := f.events.WriteAt(line, f.goodSize); err != nil {
		// Do not leave a partial line behind. If it cannot be cut, the file
		// past goodSize is unknown: a later, shorter line written over it
		// would leave the old line's tail as a complete garbage line, and the
		// next Open would refuse the whole file.
		if terr := f.events.Truncate(f.goodSize); terr != nil {
			f.broken = fmt.Errorf("%w: write failed (%v) and its partial line could not be cut: %v", errBroken, err, terr)
			return f.broken
		}
		return fmt.Errorf("ledger: write: %w", err)
	}
	if err := f.events.Sync(); err != nil {
		// After a failed fsync the kernel may already have dropped the dirty
		// pages, so a retried fsync can report success for lost data. The file
		// is no longer known; the next Open replays what it really holds.
		f.broken = fmt.Errorf("%w: fsync failed: %v", errBroken, err)
		if terr := f.events.Truncate(f.goodSize); terr != nil {
			f.broken = fmt.Errorf("%w; cutting the line failed too: %v", f.broken, terr)
		}
		return f.broken
	}
	off := f.goodSize
	f.goodSize += int64(len(line))
	f.commit([]Event{e}, []int64{off}, []int{len(line)}, pre)
	return nil
}

// AppendBatch validates every event of evs, writes them as one buffer with one
// fsync, then folds them in order (the same key twice: the later wins). Any
// validation, size or context failure writes nothing; a failed write or fsync
// is handled exactly as in Append. An empty batch does no I/O.
//
// Crash semantics match sequential Appends: a crash between the write and the
// fsync may leave a prefix of the batch on disk, whole lines of which replay
// folds and a torn last line of which it ignores. The batch is atomic against
// errors, not against a crash, so callers batch only events that are each
// valid on their own.
func (f *File) AppendBatch(ctx context.Context, evs []Event) error {
	if len(evs) == 0 {
		return nil
	}
	if len(evs) > MaxBatchEvents {
		return ErrBatchTooLarge
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var buf bytes.Buffer
	lens := make([]int, 0, len(evs))
	evs = slices.Clone(evs) // wireForm may replace entries; the caller's slice stays as given
	for i, e := range evs {
		if err := validate(e); err != nil {
			return err
		}
		line, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("ledger: marshal: %w", err)
		}
		if evs[i], err = wireForm(e, line); err != nil {
			return err
		}
		if len(line)+1 > MaxLineBytes {
			return ErrLineTooLong
		}
		if buf.Len()+len(line)+1 > MaxBatchBytes {
			return ErrBatchTooLarge
		}
		buf.Write(line)
		buf.WriteByte('\n')
		lens = append(lens, len(line)+1)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrClosed
	}
	if f.broken != nil {
		return f.broken
	}
	pre, err := f.prepare(evs)
	if err != nil {
		return err
	}
	if f.Recovered {
		if err := f.events.Truncate(f.goodSize); err != nil {
			return fmt.Errorf("ledger: truncate torn tail: %w", err)
		}
		f.Recovered = false
	}
	if _, err := f.events.WriteAt(buf.Bytes(), f.goodSize); err != nil {
		if terr := f.events.Truncate(f.goodSize); terr != nil {
			f.broken = fmt.Errorf("%w: write failed (%v) and its partial batch could not be cut: %v", errBroken, err, terr)
			return f.broken
		}
		return fmt.Errorf("ledger: write: %w", err)
	}
	if err := f.events.Sync(); err != nil {
		f.broken = fmt.Errorf("%w: fsync failed: %v", errBroken, err)
		if terr := f.events.Truncate(f.goodSize); terr != nil {
			f.broken = fmt.Errorf("%w; cutting the batch failed too: %v", f.broken, terr)
		}
		return f.broken
	}
	offs := make([]int64, len(evs))
	at := f.goodSize
	for i, n := range lens {
		offs[i], at = at, at+int64(n)
	}
	f.goodSize = at
	f.commit(evs, offs, lens, pre)
	return nil
}

// Broken returns the error that poisoned this ledger, or nil. A broken ledger
// refuses every Append until it is reopened, so a long-lived holder (the
// daemon) should exit on it rather than fail every later run.
func (f *File) Broken() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.broken
}

func cloneRecord(r *Record) Record {
	c := *r
	c.Annotations = make(map[string]string, len(r.Annotations))
	for k, v := range r.Annotations {
		c.Annotations[k] = v
	}
	return c
}

// Get returns a copy of the record for k with its description: an evicted body
// is read back from the events file. If it cannot be (ErrBodyUnavailable, see
// Hydrate) the record comes back with an empty Description and BodyEvicted
// true, never with another body.
func (f *File) Get(k posting.Key) (Record, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.recs[k]
	if !ok {
		return Record{}, false
	}
	out := cloneRecord(&s.Record)
	if s.BodyEvicted && !f.closed {
		if d, err := s.read(f.events, f.goodSize); err == nil {
			out.Posting.Description, out.BodyEvicted = d, false
		}
	}
	return out, true
}

// All returns copies of every record, sorted by Key. A Dead, Dropped or
// Duplicate record comes back without its Description and with BodyEvicted
// true; All reads nothing from disk. Use Get, Hydrate or Full for the text.
func (f *File) All() []Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return sorted(f.recs)
}

func sorted(recs map[posting.Key]*slot) []Record {
	out := make([]Record, 0, len(recs))
	for _, r := range recs {
		out = append(out, cloneRecord(&r.Record))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Close releases the file and the directory lock. It is safe to call twice.
func (f *File) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	var errs []error
	if f.events != nil {
		errs = append(errs, f.events.Close())
	}
	if f.lock != nil {
		errs = append(errs, filelock.Unlock(f.lock), f.lock.Close())
	}
	return errors.Join(errs...)
}
