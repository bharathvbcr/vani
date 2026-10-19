// Package marks is the user's own record of what they did with a posting:
// saved it, applied, or hid it, plus a private note. It is kept apart from the
// ledger on purpose. The ledger is the pipeline's record and is locked for the
// length of a run; marks are the user's, written from the app at any time, and
// nothing in the pipeline or the decision backends reads them, so a note never
// reaches a model, a log line or an annotation. The digest reads the marks
// alone, through ReadMarks, which drops every note before it returns.
//
// The store is an append-only JSONL file folded on read, like the ledger:
// every change keeps its time, a crashed write loses at most that one line,
// and the history stays auditable. Writers take an exclusive flock for the
// few milliseconds an append takes; readers take a shared one.
package marks

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Mark is what the user did with a posting. One at a time: applying to a
// saved posting replaces Saved with Applied.
type Mark string

const (
	None    Mark = ""
	Saved   Mark = "saved"
	Applied Mark = "applied"
	Hidden  Mark = "hidden"
)

// Valid reports whether m is a declared mark (None clears).
func (m Mark) Valid() bool {
	switch m {
	case None, Saved, Applied, Hidden:
		return true
	}
	return false
}

const (
	// Version is the only line version this package reads or writes.
	Version = 1
	// MaxNoteBytes bounds one note.
	MaxNoteBytes = 2000
	// MaxKeyBytes bounds a posting key.
	MaxKeyBytes = 512
	// MaxFileBytes bounds the store; a write past it is refused, never trimmed.
	MaxFileBytes = 8 << 20
	maxLineBytes = 8 << 10

	dirName   = "marks"
	fileName  = "marks.jsonl"
	lockName  = "lock"
	lockWait  = 3 * time.Second
	lockRetry = 20 * time.Millisecond
)

var (
	// ErrInvalid means a mark, key or note broke the store's rules.
	ErrInvalid = errors.New("marks: invalid")
	// ErrCorrupt means the store holds bad data before its last line.
	ErrCorrupt = errors.New("marks: corrupt store")
	// ErrFull means the store reached MaxFileBytes.
	ErrFull = errors.New("marks: store is full")
	// ErrBusy means the lock could not be had within lockWait.
	ErrBusy = errors.New("marks: store is busy")
)

// event is one line. Mark and Note are pointers so a line changes only what
// it names: a note edit leaves the mark alone and the other way round.
type event struct {
	V    int         `json:"v"`
	At   time.Time   `json:"at"`
	Key  posting.Key `json:"key"`
	Mark *Mark       `json:"mark,omitempty"`
	Note *string     `json:"note,omitempty"`
}

// Entry is the folded state of one posting's marks.
type Entry struct {
	Key      posting.Key `json:"key"`
	Mark     Mark        `json:"mark,omitempty"`
	MarkedAt time.Time   `json:"marked_at,omitzero"`
	Note     string      `json:"note,omitempty"`
	NoteAt   time.Time   `json:"note_at,omitzero"`
}

func (e Entry) empty() bool { return e.Mark == None && e.Note == "" }

// Dir is where the store lives for a state directory.
func Dir(stateDir string) string { return filepath.Join(stateDir, dirName) }

// CheckKey validates a posting key before it is stored.
func CheckKey(k posting.Key) error {
	switch {
	case k == "" || len(k) > MaxKeyBytes || k.ATS() == "":
		return fmt.Errorf("%w: key %q", ErrInvalid, truncate(string(k), 80))
	case !utf8.ValidString(string(k)) || hasControl(string(k), false):
		return fmt.Errorf("%w: key holds control characters", ErrInvalid)
	}
	return nil
}

// CheckNote validates a note: bounded, valid UTF-8, no control characters
// other than newline and tab.
func CheckNote(s string) error {
	switch {
	case len(s) > MaxNoteBytes:
		return fmt.Errorf("%w: note is %d bytes, over %d", ErrInvalid, len(s), MaxNoteBytes)
	case !utf8.ValidString(s):
		return fmt.Errorf("%w: note is not UTF-8", ErrInvalid)
	case hasControl(s, true):
		return fmt.Errorf("%w: note holds control characters", ErrInvalid)
	}
	return nil
}

func hasControl(s string, allowNewline bool) bool {
	for _, r := range s {
		if allowNewline && (r == '\n' || r == '\t') {
			continue
		}
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func validate(e event) error {
	if e.V != Version {
		return fmt.Errorf("%w: version %d", ErrInvalid, e.V)
	}
	if e.At.IsZero() {
		return fmt.Errorf("%w: zero time", ErrInvalid)
	}
	if err := CheckKey(e.Key); err != nil {
		return err
	}
	if e.Mark == nil && e.Note == nil {
		return fmt.Errorf("%w: a line changes neither mark nor note", ErrInvalid)
	}
	if e.Mark != nil && !e.Mark.Valid() {
		return fmt.Errorf("%w: mark %q", ErrInvalid, truncate(string(*e.Mark), 40))
	}
	if e.Note != nil {
		return CheckNote(*e.Note)
	}
	return nil
}

func fold(m map[posting.Key]Entry, e event) {
	cur := m[e.Key]
	cur.Key = e.Key
	if e.Mark != nil {
		cur.Mark, cur.MarkedAt = *e.Mark, e.At
		if cur.Mark == None {
			cur.MarkedAt = time.Time{}
		}
	}
	if e.Note != nil {
		cur.Note, cur.NoteAt = *e.Note, e.At
		if cur.Note == "" {
			cur.NoteAt = time.Time{}
		}
	}
	if cur.empty() {
		delete(m, e.Key)
		return
	}
	m[e.Key] = cur
}

// cutBack truncates f to good after cause; a failed truncate is joined to
// cause rather than dropped.
func cutBack(f *os.File, good int64, cause error) error {
	if err := f.Truncate(good); err != nil {
		return errors.Join(cause, fmt.Errorf("marks: the line could not be cut back and may still take effect: %w", err))
	}
	return cause
}

// replay folds src. It returns the end of the last intact line and whether a
// torn last line (no newline, or not JSON at all) was skipped; a bad line
// before the last is ErrCorrupt. So is a last line that is whole JSON but not
// a valid event, such as one from a newer writer: cutting it as torn would
// erase a mark that was really made.
func replay(src io.Reader) (map[posting.Key]Entry, int64, bool, error) {
	out := map[posting.Key]Entry{}
	r := bufio.NewReaderSize(src, 16*1024)
	var good int64
	for n := 1; ; n++ {
		line, err := r.ReadSlice('\n')
		switch {
		case errors.Is(err, bufio.ErrBufferFull) || len(line) > maxLineBytes:
			return nil, good, false, fmt.Errorf("%w: line %d is over %d bytes", ErrCorrupt, n, maxLineBytes)
		case errors.Is(err, io.EOF):
			return out, good, len(line) > 0, nil
		case err != nil:
			return nil, good, false, fmt.Errorf("marks: read: %w", err)
		}
		var e event
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		perr := dec.Decode(&e)
		if perr == nil {
			perr = validate(e)
		}
		if perr != nil {
			if _, pe := r.Peek(1); errors.Is(pe, io.EOF) && !json.Valid(line) {
				return out, good, true, nil
			}
			return nil, good, false, fmt.Errorf("%w: line %d: %v", ErrCorrupt, n, perr)
		}
		fold(out, e)
		good += int64(len(line))
	}
}

// turns holds one first-come-first-served turn per store directory, for the
// callers in this process. The flock alone starves a writer: it polls with
// LOCK_NB, and readers whose shared locks overlap never leave it a free
// moment, so it gave up with ErrBusy under load. Taking a turn first means
// in-process callers reach the flock one at a time, in the order they asked
// (goroutines blocked sending on a channel are queued in order), and the
// flock then contends only with other processes.
var turns sync.Map // cleaned dir -> chan struct{} (capacity 1)

func turn(dir string) chan struct{} {
	v, _ := turns.LoadOrStore(filepath.Clean(dir), make(chan struct{}, 1))
	return v.(chan struct{})
}

// held is a taken lock: the flock and this process's turn. Close gives both
// back.
type held struct {
	f    *os.File
	turn chan struct{}
}

func (h *held) Close() error {
	err := h.f.Close() // closing the descriptor releases the flock
	<-h.turn
	return err
}

// lock takes this process's turn on the store and then its flock, waiting at
// most lockWait for both together. It creates the directory (0700) when
// create is set; a reader of a store that was never written gets (nil, nil).
func lock(dir string, how int, create bool) (*held, error) {
	deadline := time.Now().Add(lockWait)
	t := turn(dir)
	wait := time.NewTimer(lockWait)
	defer wait.Stop()
	select {
	case t <- struct{}{}:
	case <-wait.C:
		return nil, ErrBusy
	}
	l, err := flock(dir, how, create, deadline)
	if err != nil || l == nil {
		<-t
		return nil, err
	}
	return &held{f: l, turn: t}, nil
}

// flock opens the lock file and takes its flock, polling until deadline.
func flock(dir string, how int, create bool, deadline time.Time) (*os.File, error) {
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("marks: create dir: %w", err)
		}
	}
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	l, err := os.OpenFile(filepath.Join(dir, lockName), flags, 0o600)
	if err != nil {
		if !create && errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("marks: open lock: %w", err)
	}
	for {
		err := syscall.Flock(int(l.Fd()), how|syscall.LOCK_NB)
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			l.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, ErrBusy
			}
			return nil, fmt.Errorf("marks: lock: %w", err)
		}
		time.Sleep(lockRetry)
	}
}

// Read returns every posting's marks. A store that was never written is empty.
func Read(dir string) (map[posting.Key]Entry, error) {
	l, err := lock(dir, syscall.LOCK_SH, false)
	if err != nil {
		return nil, err
	}
	if l == nil {
		// No lock file: the store was never written, unless the data file is
		// there anyway (the lock was removed, or only marks.jsonl was copied
		// back). That is not an empty store; reading it as one would drop
		// every mark.
		if _, err := os.Lstat(filepath.Join(dir, fileName)); !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s exists without its lock file", ErrCorrupt, fileName)
		}
		return map[posting.Key]Entry{}, nil
	}
	defer l.Close()
	f, err := os.Open(filepath.Join(dir, fileName))
	if errors.Is(err, os.ErrNotExist) {
		return map[posting.Key]Entry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("marks: %w", err)
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil {
		return nil, fmt.Errorf("marks: stat: %w", err)
	} else if st.Size() > MaxFileBytes {
		return nil, fmt.Errorf("%w: %d bytes, over %d", ErrCorrupt, st.Size(), MaxFileBytes)
	}
	m, _, _, err := replay(f)
	return m, err
}

// ReadMarks returns every marked posting's mark and nothing else: notes are
// dropped here, so a reader that must never see one (the digest) cannot. A
// posting with only a note is absent. Errors are Read's.
func ReadMarks(dir string) (map[posting.Key]Mark, error) {
	m, err := Read(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[posting.Key]Mark, len(m))
	for k, e := range m {
		if e.Mark != None {
			out[k] = e.Mark
		}
	}
	return out, nil
}

// SetMark records mark for k at now (None clears it) and returns the entry.
func SetMark(dir string, k posting.Key, mark Mark, now time.Time) (Entry, error) {
	return write(dir, event{V: Version, At: now.UTC(), Key: k, Mark: &mark})
}

// SetNote records note for k at now ("" clears it) and returns the entry.
func SetNote(dir string, k posting.Key, note string, now time.Time) (Entry, error) {
	return write(dir, event{V: Version, At: now.UTC(), Key: k, Note: &note})
}

func write(dir string, e event) (Entry, error) {
	if err := validate(e); err != nil {
		return Entry{}, err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return Entry{}, fmt.Errorf("marks: marshal: %w", err)
	}
	line = append(line, '\n')
	if len(line) > maxLineBytes {
		return Entry{}, fmt.Errorf("%w: line is over %d bytes", ErrInvalid, maxLineBytes)
	}
	l, err := lock(dir, syscall.LOCK_EX, true)
	if err != nil {
		return Entry{}, err
	}
	defer l.Close()
	f, err := os.OpenFile(filepath.Join(dir, fileName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return Entry{}, fmt.Errorf("marks: open: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Entry{}, fmt.Errorf("marks: stat: %w", err)
	}
	if st.Size()+int64(len(line)) > MaxFileBytes {
		return Entry{}, ErrFull
	}
	m, good, torn, err := replay(f)
	if err != nil {
		return Entry{}, err
	}
	if torn {
		// A writer died mid-line; cut it so this line starts on a clean boundary.
		if err := f.Truncate(good); err != nil {
			return Entry{}, fmt.Errorf("marks: truncate torn tail: %w", err)
		}
	}
	// A failed write or fsync is cut back to the last good line. If the cut
	// fails too, the line may still be in the file and take effect on the
	// next replay, so the caller is told it may have been written.
	if _, err := f.WriteAt(line, good); err != nil {
		return Entry{}, cutBack(f, good, fmt.Errorf("marks: write: %w", err))
	}
	if err := f.Sync(); err != nil {
		return Entry{}, cutBack(f, good, fmt.Errorf("marks: fsync: %w", err))
	}
	fold(m, e)
	if out, ok := m[e.Key]; ok {
		return out, nil
	}
	return Entry{Key: e.Key}, nil
}
