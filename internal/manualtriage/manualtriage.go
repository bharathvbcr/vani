// Package manualtriage keeps the readings the owner asked a Claude model for by
// hand ("Re-triage with Sonnet", `vani jobs retriage`). A reading is the four
// triage slots one model gave one posting's text, and it is kept beside the
// ledger, never in it: the daemon holds the ledger lock all day, and a manual
// reading is the owner's act, not a fact the pipeline recorded. Like the marks
// store (internal/marks) it is written from the app at any time.
//
// The store is an append-only JSONL file folded on read (last line for a key
// wins; a clear line removes it). Writers take an exclusive flock for the few
// milliseconds an append takes. Readers take no lock: an append is one whole
// line written in one call, a compaction replaces the file by rename, and a
// reader that meets a half-written last line skips it as torn, exactly as marks
// and the ledger do. A line before the last that does not read is ErrCorrupt,
// and every caller then treats the store as unusable (nothing is applied), never
// as empty.
//
// A reading only orders (Rule 7): jobinsight.RankContext.Manual hands it to the
// ranking's one slot owner (SlotFor), and nothing in the drop policy reads this
// package.
//
// The near-duplication of marks' replay code is deliberate for now: marks is
// pinned by fifteen test files, and the follow-up is a shared append-only-fold
// helper both packages call (docs/FOLLOWUPS.md). The flock itself is
// internal/filelock's (TryAcquire, Acquire); marks keeps its own because it
// also takes shared locks and must not create a store on read.
package manualtriage

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const (
	// Version is the only line version this package reads or writes.
	Version = 1
	// MaxFileBytes bounds the store. A write that would pass it compacts the
	// file to its live readings first; if they alone do not fit, the write is
	// refused (ErrFull), never trimmed.
	MaxFileBytes = 4 << 20
	// MaxEntries bounds the live readings (one per posting).
	MaxEntries = 5000
	// maxLineBytes bounds one stored line: four short slot values, two hashes, a
	// model id and a key, with JSON escaping, fit well inside it (a test pins the
	// worst case).
	maxLineBytes = 8 << 10
	// MaxModelBytes bounds the model id (config.MaxModelName).
	MaxModelBytes = 128

	dirName  = "retriage"
	fileName = "readings.jsonl"
	lockName = "lock"

	opSet   = "set"
	opClear = "clear"
)

var (
	// ErrInvalid means a key, reading or line broke the store's rules.
	ErrInvalid = errors.New("manualtriage: invalid")
	// ErrCorrupt means the store holds bad data before its last line.
	ErrCorrupt = errors.New("manualtriage: corrupt store")
	// ErrFull means the live readings alone do not fit the store's bounds.
	ErrFull = errors.New("manualtriage: store is full")
	// ErrBusy means another writer held the lock for the whole wait.
	ErrBusy = errors.New("manualtriage: store is busy")
)

// lockWait is how long a writer waits for the flock. A variable only so tests
// need not wait.
var lockWait = 3 * time.Second

// Reading is one posting's manual reading: what a model said, to what text.
type Reading struct {
	Key posting.Key `json:"key"`
	// Slots maps each of the triage slots (jdtriage.Slots) to the option the model
	// chose. A reading always has every slot, each a listed option.
	Slots map[string]string `json:"slots"`
	// Model is the model that answered, for display and audit.
	Model string    `json:"model"`
	At    time.Time `json:"at"`
	// Text is TextSHA256 of the posting as it was read: the reading is current only
	// while the posting still hashes to it. Prompt is PromptSHA256 of the question
	// it was given, for audit; a changed prompt does not invalidate a reading.
	Text   string `json:"text"`
	Prompt string `json:"prompt"`
}

// event is one stored line.
type event struct {
	V      int               `json:"v"`
	At     time.Time         `json:"at"`
	Key    posting.Key       `json:"key"`
	Op     string            `json:"op"`
	Slots  map[string]string `json:"slots,omitempty"`
	Model  string            `json:"model,omitempty"`
	Text   string            `json:"text,omitempty"`
	Prompt string            `json:"prompt,omitempty"`
}

// Dir is where the store lives for a state directory.
func Dir(stateDir string) string { return filepath.Join(stateDir, dirName) }

// TextSHA256 is the hash a reading is keyed on: the posting's title, location and
// description as the triage question lays them out (jdtriage.Render with no
// target summary), so anything beyond the question's own cut does not change it
// and neither does a change to the target summary.
func TextSHA256(p posting.Posting) string {
	text, _ := jdtriage.Render(p.Title, p.Location, p.Description, "")
	sum := sha256.Sum256(text)
	return hex.EncodeToString(sum[:])
}

// PromptSHA256 is the hash of the question's fixed parts: parts the caller
// supplies (the system prompt, the normalized target summary) and the fixture
// every triage question is built from.
func PromptSHA256(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = io.WriteString(h, p)
		_, _ = h.Write([]byte{0})
	}
	_, _ = h.Write(jdtriage.FixtureJSON)
	return hex.EncodeToString(h.Sum(nil))
}

// State says whether a reading still describes the posting.
type State string

const (
	// Current: the posting text is what the model read.
	Current State = "current"
	// TextChanged: the posting's text differs from what the model read.
	TextChanged State = "text_changed"
	// TextGone: the posting's text is no longer in the ledger, so the reading
	// cannot be checked and is not used.
	TextGone State = "text_gone"
)

// StateOf is r's state against the posting as the ledger has it now. gone says
// the ledger no longer holds the posting's text (a closed role's body is
// evicted).
func (r Reading) StateOf(p posting.Posting, gone bool) State {
	switch {
	case gone:
		return TextGone
	case r.Text != TextSHA256(p):
		return TextChanged
	}
	return Current
}

func hexOK(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func checkModel(m string) error {
	if m == "" || len(m) > MaxModelBytes || !utf8.ValidString(m) {
		return fmt.Errorf("%w: model id is empty, oversized or not UTF-8", ErrInvalid)
	}
	for _, r := range m {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("%w: model id holds control characters or space", ErrInvalid)
		}
	}
	return nil
}

// CheckSlots requires every triage slot, each a listed option, and no other
// slot: a partial or invented reading is not stored.
func CheckSlots(slots map[string]string) error {
	values := jdtriage.SlotValues()
	if len(slots) != len(values) {
		return fmt.Errorf("%w: a reading names %d slots, want %d", ErrInvalid, len(slots), len(values))
	}
	for name, opts := range values {
		v, ok := slots[name]
		if !ok || !slices.Contains(opts, v) {
			return fmt.Errorf("%w: slot %q is missing or not a listed option", ErrInvalid, name)
		}
	}
	return nil
}

func validate(e event) error {
	if e.V != Version {
		return fmt.Errorf("%w: version %d", ErrInvalid, e.V)
	}
	if e.At.IsZero() {
		return fmt.Errorf("%w: zero time", ErrInvalid)
	}
	if err := marks.CheckKey(e.Key); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	switch e.Op {
	case opClear:
		if e.Slots != nil || e.Model != "" || e.Text != "" || e.Prompt != "" {
			return fmt.Errorf("%w: a clear line carries a reading", ErrInvalid)
		}
		return nil
	case opSet:
		if err := CheckSlots(e.Slots); err != nil {
			return err
		}
		if err := checkModel(e.Model); err != nil {
			return err
		}
		if !hexOK(e.Text) || !hexOK(e.Prompt) {
			return fmt.Errorf("%w: a reading has no hashes", ErrInvalid)
		}
		return nil
	}
	return fmt.Errorf("%w: op %q", ErrInvalid, e.Op)
}

func fold(m map[posting.Key]Reading, e event) {
	if e.Op == opClear {
		delete(m, e.Key)
		return
	}
	m[e.Key] = Reading{Key: e.Key, Slots: maps.Clone(e.Slots), Model: e.Model, At: e.At, Text: e.Text, Prompt: e.Prompt}
}

type replayed struct {
	m     map[posting.Key]Reading
	good  int64 // end of the last intact line
	torn  bool  // a half-written last line was skipped
	lines int
}

// replay folds src. A torn last line (no newline, or not JSON at all) is skipped
// and reported; a bad line before the last is ErrCorrupt. So is a last line that
// is whole JSON but not a valid event, such as one from a newer writer: cutting
// it as torn would erase a reading that was really made.
func replay(src io.Reader) (replayed, error) {
	out := replayed{m: map[posting.Key]Reading{}}
	r := bufio.NewReaderSize(src, 2*maxLineBytes)
	for n := 1; ; n++ {
		line, err := r.ReadSlice('\n')
		switch {
		case errors.Is(err, bufio.ErrBufferFull) || len(line) > maxLineBytes:
			return replayed{}, fmt.Errorf("%w: line %d is over %d bytes", ErrCorrupt, n, maxLineBytes)
		case errors.Is(err, io.EOF):
			out.torn = len(line) > 0
			return out, nil
		case err != nil:
			return replayed{}, fmt.Errorf("manualtriage: read: %w", err)
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
				out.torn = true
				return out, nil
			}
			return replayed{}, fmt.Errorf("%w: line %d: %v", ErrCorrupt, n, perr)
		}
		fold(out.m, e)
		out.good += int64(len(line))
		out.lines++
		if len(out.m) > MaxEntries {
			return replayed{}, fmt.Errorf("%w: more than %d readings", ErrCorrupt, MaxEntries)
		}
	}
}

// Read returns every posting's current manual reading. A store that was never
// written is empty. It takes no lock (see the package doc).
func Read(dir string) (map[posting.Key]Reading, error) {
	f, err := atomicfile.OpenRead(filepath.Join(dir, fileName))
	if errors.Is(err, os.ErrNotExist) {
		return map[posting.Key]Reading{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("manualtriage: %w", err)
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err != nil {
		return nil, fmt.Errorf("manualtriage: stat: %w", err)
	} else if st.Size() > MaxFileBytes {
		return nil, fmt.Errorf("%w: %d bytes, over %d", ErrCorrupt, st.Size(), MaxFileBytes)
	}
	r, err := replay(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	return r.m, nil
}

// Set records rd (its At is the time it was made) and replaces any earlier
// reading of the same posting.
func Set(dir string, rd Reading) error {
	return write(dir, event{V: Version, At: rd.At.UTC(), Key: rd.Key, Op: opSet,
		Slots: rd.Slots, Model: rd.Model, Text: rd.Text, Prompt: rd.Prompt})
}

// Clear removes k's manual reading at now and reports whether there was one.
// Clearing a posting with no reading writes nothing.
func Clear(dir string, k posting.Key, now time.Time) (had bool, err error) {
	err = writeWith(dir, event{V: Version, At: now.UTC(), Key: k, Op: opClear}, func(r replayed) bool {
		_, had = r.m[k]
		return had
	})
	return had, err
}

func write(dir string, e event) error { return writeWith(dir, e, nil) }

// turns serializes the writers of one store inside this process, first come
// first served, so the flock (polled with LOCK_NB) is contended only by other
// processes.
var turns sync.Map // cleaned dir -> chan struct{} (capacity 1)

func turn(dir string) chan struct{} {
	v, _ := turns.LoadOrStore(filepath.Clean(dir), make(chan struct{}, 1))
	return v.(chan struct{})
}

// lock takes this process's turn and then the exclusive flock, waiting at most
// lockWait for both together.
func lock(dir string) (unlock func(), err error) {
	deadline := time.Now().Add(lockWait)
	t := turn(dir)
	wait := time.NewTimer(lockWait)
	defer wait.Stop()
	select {
	case t <- struct{}{}:
	case <-wait.C:
		return nil, ErrBusy
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	l, err := filelock.Acquire(ctx, filepath.Join(dir, lockName))
	if err != nil {
		<-t
		if errors.Is(err, filelock.ErrWouldBlock) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("manualtriage: %w", err)
	}
	return func() { _ = l.Release(); <-t }, nil
}

// askLockName is the lock one paid reading holds while it asks, apart from the
// write lock so a reader's turn at the store is never kept waiting by a slow
// provider.
const askLockName = "ask.lock"

// TryAskLock takes, without waiting, the lock that lets one paid reading run at a
// time in this state directory: a second click (or a second shell) while a model
// is being asked would pay twice for the same posting. It gives ErrBusy when
// another holds it. The flock is the process's: it goes when the process ends.
func TryAskLock(dir string) (unlock func(), err error) {
	l, err := filelock.TryAcquire(filepath.Join(dir, askLockName))
	if err != nil {
		if errors.Is(err, filelock.ErrWouldBlock) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("manualtriage: %w", err)
	}
	return func() { _ = l.Release() }, nil
}

// syncDir is atomicfile.SyncDir; a variable only so a test can count its calls.
var syncDir = atomicfile.SyncDir

// afterCompact runs after a compacted replace and before the file is reopened.
// Production passes the path through. A test plants a symlink in that window.
var afterCompact = func(string) {}

// writeWith appends e under the lock. needed, when set, is asked once the store
// has been read and says whether e changes anything; false writes nothing.
func writeWith(dir string, e event, needed func(replayed) bool) error {
	if err := validate(e); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("manualtriage: marshal: %w", err)
	}
	line = append(line, '\n')
	if len(line) > maxLineBytes {
		return fmt.Errorf("%w: line is over %d bytes", ErrInvalid, maxLineBytes)
	}
	unlock, err := lock(dir)
	if err != nil {
		return err
	}
	defer unlock()
	path := filepath.Join(dir, fileName)
	f, created, err := atomicfile.OpenAppend(path, 0)
	if err != nil {
		return fmt.Errorf("manualtriage: open: %w", err)
	}
	defer func() { _ = f.Close() }()
	if created {
		// The line's fsync below does not cover the entry that names the file.
		if err := atomicfile.SyncNew(path, syncDir); err != nil {
			return fmt.Errorf("manualtriage: %w", err)
		}
	}
	if st, err := f.Stat(); err != nil {
		return fmt.Errorf("manualtriage: stat: %w", err)
	} else if st.Size() > MaxFileBytes {
		return fmt.Errorf("%w: %d bytes, over %d", ErrCorrupt, st.Size(), MaxFileBytes)
	}
	// The replay is the store's check: a write never lands on a store that does
	// not read cleanly.
	r, err := replay(f)
	if err != nil {
		return err
	}
	if needed != nil && !needed(r) {
		return nil
	}
	if _, has := r.m[e.Key]; e.Op == opSet && !has && len(r.m) >= MaxEntries {
		return fmt.Errorf("%w: %d readings kept", ErrFull, MaxEntries)
	}
	good := r.good
	if good+int64(len(line)) > MaxFileBytes {
		// Closed first: Windows cannot rename over an open file.
		if err := f.Close(); err != nil {
			return fmt.Errorf("manualtriage: close: %w", err)
		}
		size, err := compact(path, r.m)
		if err != nil {
			return err
		}
		afterCompact(path)
		nf, err := reopenCompacted(path)
		if err != nil {
			return fmt.Errorf("manualtriage: reopen after compaction: %w", err)
		}
		f = nf
		defer func() { _ = f.Close() }()
		good = size
		if good+int64(len(line)) > MaxFileBytes {
			return ErrFull
		}
	} else if r.torn {
		// A writer died mid-line; cut it so this line starts on a clean boundary.
		if err := f.Truncate(good); err != nil {
			return fmt.Errorf("manualtriage: truncate torn tail: %w", err)
		}
	}
	if _, err := f.WriteAt(line, good); err != nil {
		return cutBack(f, good, fmt.Errorf("manualtriage: write: %w", err))
	}
	if err := f.Sync(); err != nil {
		return cutBack(f, good, fmt.Errorf("manualtriage: fsync: %w", err))
	}
	return nil
}

// cutBack truncates f to good after cause; a failed truncate is joined to cause
// rather than dropped, because the line may then still take effect.
func cutBack(f *os.File, good int64, cause error) error {
	if err := f.Truncate(good); err != nil {
		return errors.Join(cause, fmt.Errorf("manualtriage: the line could not be cut back and may still take effect: %w", err))
	}
	return cause
}

// reopenCompacted opens the file compact just replaced. OpenAppend refuses a
// symlink planted at the leaf in that window, so WriteAt cannot follow it. A
// file this call had to create is synced before it is trusted.
func reopenCompacted(path string) (*os.File, error) {
	f, created, err := atomicfile.OpenAppend(path, 0)
	if err != nil {
		return nil, err
	}
	if created {
		if err := atomicfile.SyncNew(path, syncDir); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	return f, nil
}

// compact replaces the file with one set line per live reading, sorted by key,
// and returns its size. The replacement is atomic.
func compact(path string, m map[posting.Key]Reading) (int64, error) {
	keys := make([]posting.Key, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	var buf bytes.Buffer
	for _, k := range keys {
		rd := m[k]
		line, err := json.Marshal(event{V: Version, At: rd.At, Key: k, Op: opSet, Slots: rd.Slots, Model: rd.Model, Text: rd.Text, Prompt: rd.Prompt})
		if err != nil {
			return 0, fmt.Errorf("manualtriage: compact: %w", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := atomicfile.Write(path, buf.Bytes()); err != nil {
		return 0, fmt.Errorf("manualtriage: compact: %w", err)
	}
	return int64(buf.Len()), nil
}
