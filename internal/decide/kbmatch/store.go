package kbmatch

import (
	"bytes"
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
	// MaxEntries bounds the store; the oldest answers go first.
	MaxEntries   = 20000
	maxFileBytes = 8 << 20
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
	// Backend is the backend that answered ("llm", "jev") and Mode what the
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
}

// NewStore is an empty store that will be saved at path.
func NewStore(path string) *Store {
	return &Store{path: path, f: storeFile{Version: StoreVersion, Entries: map[string]Entry{}}}
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

// Put records an answer, replacing any earlier one for key. The oldest are
// dropped past MaxEntries.
func (s *Store) Put(key string, e Entry) error {
	if err := checkEntry(key, e); err != nil {
		return fmt.Errorf("kbmatch: %w", err)
	}
	s.f.Entries[key] = e
	if len(s.f.Entries) > MaxEntries {
		keys := make([]string, 0, len(s.f.Entries))
		for k := range s.f.Entries {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := s.f.Entries[keys[i]].At, s.f.Entries[keys[j]].At
			if !a.Equal(b) {
				return a.Before(b)
			}
			return keys[i] < keys[j]
		})
		for _, k := range keys[:len(keys)-MaxEntries] {
			delete(s.f.Entries, k)
		}
	}
	return nil
}

// Save writes the store atomically (0600).
func (s *Store) Save() error {
	raw, err := json.MarshalIndent(s.f, "", " ")
	if err != nil {
		return fmt.Errorf("kbmatch: %w", err)
	}
	if len(raw) > maxFileBytes {
		return fmt.Errorf("kbmatch: the store would be larger than %d bytes", maxFileBytes)
	}
	return atomicfile.Write(s.path, append(raw, '\n'))
}

// Lock takes the store's run lock without waiting: two runs would spend twice
// and the later save would drop the earlier one's answers.
func Lock(path string) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("kbmatch: %w", err)
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
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
