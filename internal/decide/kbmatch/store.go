package kbmatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

const (
	// FileName is the store, in the state directory. It is beside the ledger, not
	// in it: an answer orders and explains, it is not a fact about the posting.
	FileName     = "kbmatch.json"
	StoreVersion = 1
	// MaxEntries bounds the store; the oldest answers go first. maxFileBytes
	// bounds the saved file the same way: a store that would pass it loses its
	// oldest answers on Save, so the entry limit can never leave Save refusing.
	MaxEntries   = 20000
	maxFileBytes = 8 << 20
	// MaxFileBytes is maxFileBytes for callers that tell the owner the limit.
	MaxFileBytes = maxFileBytes
	maxKeyBytes  = 512
	maxModelLen  = 128
)

// ErrCorrupt means the store cannot be read. Nothing in it is irreplaceable (the
// model can be asked again), but the caller decides whether to start over.
var ErrCorrupt = errors.New("kbmatch: the store cannot be read")

// ErrBusy means another run holds the store.
var ErrBusy = errors.New("kbmatch: another run is using the store")

// Entry is one answer and what it was given.
type Entry struct {
	Value string `json:"value"`
	// Model is the model that answered, for display and audit; it does not
	// invalidate the answer.
	Model string `json:"model"`
	// KB is the digest hash and Text TextSHA256: an answer is current only when
	// both match what the question would be given now.
	KB   string    `json:"kb"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
	// P is the backend's probability when it gave one.
	P float64 `json:"p,omitempty"`
	// Backend is the backend that answered ("llm", "openrouter"; answers
	// before 2026-10-20 say "jev" for openrouter) and Mode what the
	// question carried: "full" digest or "focused" (compact digest plus lines an
	// embedding model picked). Both are audit; neither invalidates the answer.
	Backend string `json:"backend,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

// The modes an Entry may record.
const (
	ModeFull    = "full"
	ModeFocused = "focused"
)

type storeFile struct {
	Version int              `json:"version"`
	Entries map[string]Entry `json:"entries"`
}

// Store holds the answers.
type Store struct {
	path string
	f    storeFile
	// fresh is the keys Put since the store was opened: this run's answers,
	// the paid ones eviction exists to keep. They go only after every older
	// answer is gone.
	fresh map[string]struct{}
	// dropped is the answers Save has evicted to fit maxFileBytes since the
	// store was opened; Dropped reports it.
	dropped int
}

// NewStore is an empty store that will be saved at path.
func NewStore(path string) *Store {
	return &Store{path: path, f: storeFile{Version: StoreVersion, Entries: map[string]Entry{}}, fresh: map[string]struct{}{}}
}

// OpenStore reads the store at path; a missing file is an empty store.
func OpenStore(path string) (*Store, error) {
	s := NewStore(path)
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("kbmatch: %w", err)
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("kbmatch: %w", err)
	}
	if len(raw) > maxFileBytes {
		return nil, fmt.Errorf("%w: larger than %d bytes", ErrCorrupt, maxFileBytes)
	}
	var sf storeFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sf); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: trailing data", ErrCorrupt)
	}
	if sf.Version != StoreVersion {
		return nil, fmt.Errorf("%w: version %d, want %d", ErrCorrupt, sf.Version, StoreVersion)
	}
	if len(sf.Entries) > MaxEntries {
		return nil, fmt.Errorf("%w: %d entries, max %d", ErrCorrupt, len(sf.Entries), MaxEntries)
	}
	for k, e := range sf.Entries {
		if err := checkEntry(k, e); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
	}
	if sf.Entries == nil {
		sf.Entries = map[string]Entry{}
	}
	s.f = sf
	return s, nil
}

func checkEntry(key string, e Entry) error {
	switch {
	case key == "" || len(key) > maxKeyBytes:
		return errors.New("an entry has an empty or oversized key")
	case !slices.Contains(jobinsight.KBMatchOrder[:], e.Value):
		return fmt.Errorf("an entry holds %q, not an option", e.Value)
	case len(e.KB) != 64 || len(e.Text) != 64:
		return errors.New("an entry has no hashes")
	case len(e.Model) > maxModelLen:
		return errors.New("an entry has an oversized model name")
	case !(e.P >= 0 && e.P <= 1):
		return errors.New("an entry has a probability outside 0..1")
	case e.Mode != "" && e.Mode != ModeFull && e.Mode != ModeFocused:
		return fmt.Errorf("an entry has mode %q", e.Mode)
	case len(e.Backend) > 32:
		return errors.New("an entry has an oversized backend name")
	}
	return nil
}

// Dropped is how many answers Save has evicted since the store was opened to
// keep the file under the byte limit. A caller reads it after Save to tell the
// owner that paid answers were let go; answers past MaxEntries are Put's
// bound, not counted here.
func (s *Store) Dropped() int { return s.dropped }

// Len is the number of answers held.
func (s *Store) Len() int { return len(s.f.Entries) }

// Lookup returns the answer for key if it was given exactly this digest and
// text. An answer for anything else is not found: it is not a score of 0.
func (s *Store) Lookup(key string, kb, text string) (Entry, bool) {
	e, ok := s.f.Entries[key]
	if !ok || e.KB != kb || e.Text != text {
		return Entry{}, false
	}
	return e, true
}

// Put records an answer, replacing any earlier one for key. Past MaxEntries the
// oldest others are dropped, never the answer being put: it was just paid for,
// whatever its At. Those evictions are Put's count bound and are not counted in
// Dropped, which is only what Save drops to fit the byte limit. A new answer needs a time (a zero At would rank it
// oldest); a file already holding zero-At entries still opens.
func (s *Store) Put(key string, e Entry) error {
	if err := checkEntry(key, e); err != nil {
		return fmt.Errorf("kbmatch: %w", err)
	}
	if e.At.IsZero() {
		return errors.New("kbmatch: an answer has no time")
	}
	s.f.Entries[key] = e
	s.fresh[key] = struct{}{}
	if len(s.f.Entries) > MaxEntries {
		s.evictOldestExcept(len(s.f.Entries)-MaxEntries, key)
	}
	return nil
}

// evictOldest drops n answers (all of them if n is larger).
func (s *Store) evictOldest(n int) { s.evictOldestExcept(n, "") }

// evictOldestExcept drops n answers other than the one under keep (all of them
// if n is larger): answers from before this open first, oldest At first, ties by
// key, then this run's answers in the same order. Keys are never empty, so an
// empty keep protects nothing.
func (s *Store) evictOldestExcept(n int, keep string) {
	keys := make([]string, 0, len(s.f.Entries))
	for k := range s.f.Entries {
		if k != keep {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		_, fi := s.fresh[keys[i]]
		_, fj := s.fresh[keys[j]]
		if fi != fj {
			return fj
		}
		a, b := s.f.Entries[keys[i]].At, s.f.Entries[keys[j]].At
		if !a.Equal(b) {
			return a.Before(b)
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys[:min(n, len(keys))] {
		delete(s.f.Entries, k)
	}
}

// encode is the exact bytes of the saved file, trailing newline included: the
// size Save checks and the size OpenStore reads back are this one length.
func (s *Store) encode() ([]byte, error) {
	raw, err := json.MarshalIndent(s.f, "", " ")
	if err != nil {
		return nil, fmt.Errorf("kbmatch: %w", err)
	}
	return append(raw, '\n'), nil
}

// Save writes the store atomically (0600). A store whose file would be larger than
// maxFileBytes first drops answers (the same order as MaxEntries) until
// it fits, so a full store still saves the newest answer instead of refusing and
// losing every paid answer of the run. It never writes a file over the limit.
// What it dropped is counted in Dropped; it is never silent to a caller that asks.
func (s *Store) Save() error {
	raw, err := s.encode()
	if err != nil {
		return err
	}
	for len(raw) > maxFileBytes && len(s.f.Entries) > 0 {
		// Drop in proportion to the overshoot (by the average entry size), at
		// least one; each pass re-measures, so the estimate only sets the pace.
		n := len(s.f.Entries)
		drop := max(1, (len(raw)-maxFileBytes)*n/len(raw)+1)
		before := len(s.f.Entries)
		s.evictOldest(drop)
		s.dropped += before - len(s.f.Entries)
		if raw, err = s.encode(); err != nil {
			return err
		}
	}
	if len(raw) > maxFileBytes {
		return fmt.Errorf("kbmatch: the store would be larger than %d bytes", maxFileBytes)
	}
	return atomicfile.Write(s.path, raw)
}

// Lock takes the store's run lock without waiting: two runs would spend twice
// and the later save would drop the earlier one's answers.
func Lock(path string) (unlock func(), err error) {
	target, err := atomicfile.ResolveLeaf(path)
	if err != nil {
		return nil, fmt.Errorf("kbmatch: %w", err)
	}
	return lockAt(target)
}

// lockAt locks target, a path already resolved by atomicfile.ResolveLeaf. The
// store is read, removed and saved at that same path, so the lock and the
// writes can never name different files.
func lockAt(target string) (unlock func(), err error) {
	lockPath := target + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("kbmatch: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("kbmatch: %w", err)
	}
	if err := filelock.TryLock(f, true); err != nil {
		_ = f.Close()
		if errors.Is(err, filelock.ErrWouldBlock) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("kbmatch: lock: %w", err)
	}
	return func() { _ = filelock.Unlock(f); _ = f.Close() }, nil
}

// lockRetry is how often LockWait tries again while another run holds the lock.
const lockRetry = 20 * time.Millisecond

// LockWait is Lock that waits up to wait (at least one try) for another run to
// let go, then gives ErrBusy. A run holds the lock for as long as it asks, so
// the wait is short: it rides out a run that is only finishing, not one that is
// working through its list. The wait ends when ctx does, with ctx.Err() and
// without the lock.
func LockWait(ctx context.Context, path string, wait time.Duration) (unlock func(), err error) {
	target, err := atomicfile.ResolveLeaf(path)
	if err != nil {
		return nil, fmt.Errorf("kbmatch: %w", err)
	}
	return lockWaitAt(ctx, target, wait)
}

func lockWaitAt(ctx context.Context, target string, wait time.Duration) (unlock func(), err error) {
	deadline := time.Now().Add(wait)
	timer := time.NewTimer(lockRetry)
	defer timer.Stop()
	for {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		unlock, err = lockAt(target)
		if !errors.Is(err, ErrBusy) || !time.Now().Before(deadline) {
			return unlock, err
		}
		timer.Reset(lockRetry)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// OpenLocked takes the run lock, waiting up to wait, and only then opens the
// store, so what the caller reads is what no other run can change before it
// saves. A corrupt store is removed here, under the lock, and recovered carries
// why; the caller starts from an empty store. Nothing is removed without the
// lock. The caller calls unlock when the run is done.
func OpenLocked(ctx context.Context, path string, wait time.Duration) (st *Store, unlock func(), recovered error, err error) {
	// Resolve once: the lock, the read, the corrupt-file removal and every later
	// Save of the returned store all use this one path.
	path, err = atomicfile.ResolveLeaf(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("kbmatch: %w", err)
	}
	unlock, err = lockWaitAt(ctx, path, wait)
	if err != nil {
		return nil, nil, nil, err
	}
	st, err = OpenStore(path)
	if errors.Is(err, ErrCorrupt) {
		recovered = err
		if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			unlock()
			return nil, nil, nil, rerr
		}
		st, err = NewStore(path), nil
	}
	if err != nil {
		unlock()
		return nil, nil, nil, err
	}
	return st, unlock, recovered, nil
}
