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
	"sort"
	"strconv"
	"sync"

	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const (
	// SchemaVersion is the only event version this implementation reads or writes.
	SchemaVersion = 1
	// MaxLineBytes bounds one serialised event, newline included.
	MaxLineBytes = 1 << 20
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
	// Recovered is true when Open found and ignored a torn last line. The
	// bad bytes are cut off the file on the next successful Append.
	Recovered bool

	mu       sync.Mutex
	events   *os.File
	lock     *os.File
	recs     map[posting.Key]*Record
	goodSize int64 // end of the last intact line
	closed   bool
}

var _ Ledger = (*File)(nil)

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
	ev, err := os.OpenFile(filepath.Join(dir, eventsName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		lock.Close()
		return nil, fmt.Errorf("ledger: open events: %w", err)
	}
	f := &File{events: ev, lock: lock, recs: map[posting.Key]*Record{}}
	if err := f.replay(); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// readLine returns one line without its newline. hadNL is false for a final
// line the file ended in the middle of. A line past MaxLineBytes is an error.
func readLine(r *bufio.Reader) (line []byte, raw int, hadNL bool, err error) {
	for {
		chunk, e := r.ReadSlice('\n')
		raw += len(chunk)
		if raw > MaxLineBytes {
			return nil, raw, false, ErrLineTooLong
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
	off, torn, err := replay(f.events, f.recs)
	f.goodSize, f.Recovered = off, torn
	return err
}

// replay folds every intact event line of r into recs. It returns the end of
// the last intact line and whether a torn last line (no newline, or a final
// line that does not decode) was ignored; anything bad before the last line
// is ErrCorrupt.
func replay(src io.Reader, recs map[posting.Key]*Record) (good int64, torn bool, err error) {
	r := bufio.NewReaderSize(src, 64*1024)
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
		perr := json.Unmarshal(line, &e)
		if perr == nil {
			perr = validate(e)
		}
		if perr != nil {
			if _, pe := r.Peek(1); errors.Is(pe, io.EOF) {
				return good, true, nil
			}
			return good, false, fmt.Errorf("%w: line %d: %v", ErrCorrupt, n, perr)
		}
		fold(recs, e)
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
	recs := map[posting.Key]*Record{}
	_, torn, err := replay(ev, recs)
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

// fold applies a validated event to the in-memory state.
func fold(recs map[posting.Key]*Record, e Event) {
	r, ok := recs[e.Key]
	if !ok {
		r = &Record{Key: e.Key, Annotations: map[string]string{}, FirstSeen: e.At}
		recs[e.Key] = r
	}
	r.LastEvent = e.At
	r.Status = e.Status
	if e.Posting != nil {
		r.Posting = *e.Posting
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
		r.Annotations[k] = v
	}
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
	line = append(line, '\n')
	if len(line) > MaxLineBytes {
		return ErrLineTooLong
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrClosed
	}
	if f.Recovered {
		// Cut the torn tail so the new line starts on a clean boundary.
		if err := f.events.Truncate(f.goodSize); err != nil {
			return fmt.Errorf("ledger: truncate torn tail: %w", err)
		}
		f.Recovered = false
	}
	if _, err := f.events.WriteAt(line, f.goodSize); err != nil {
		_ = f.events.Truncate(f.goodSize) // do not leave a partial line behind
		return fmt.Errorf("ledger: write: %w", err)
	}
	if err := f.events.Sync(); err != nil {
		_ = f.events.Truncate(f.goodSize)
		return fmt.Errorf("ledger: fsync: %w", err)
	}
	f.goodSize += int64(len(line))
	fold(f.recs, e)
	return nil
}

func cloneRecord(r *Record) Record {
	c := *r
	c.Annotations = make(map[string]string, len(r.Annotations))
	for k, v := range r.Annotations {
		c.Annotations[k] = v
	}
	return c
}

// Get returns a copy of the record for k.
func (f *File) Get(k posting.Key) (Record, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.recs[k]
	if !ok {
		return Record{}, false
	}
	return cloneRecord(r), true
}

// All returns copies of every record, sorted by Key.
func (f *File) All() []Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return sorted(f.recs)
}

func sorted(recs map[posting.Key]*Record) []Record {
	out := make([]Record, 0, len(recs))
	for _, r := range recs {
		out = append(out, cloneRecord(r))
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
