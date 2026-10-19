package kbsync

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// StateVersion is the sync-state file format.
const StateVersion = 1

// MaxStateBytes caps the sync-state file.
const MaxStateBytes = 4 << 20

// ErrState wraps every sync-state problem.
var ErrState = errors.New("kbsync: sync state")

// SyncState records, per rkb block id, the hash of the resolved block DevPrism
// last accepted. A hash is written only after its upsert succeeded. The file
// is Vani's, not DevPrism's or rkb's.
type SyncState struct {
	Version int               `json:"version"`
	Blocks  map[string]string `json:"blocks"`
	Synced  map[string]string `json:"synced_at,omitempty"` // block id -> RFC3339 time of the accepted upsert

	path string
}

// NewSyncState returns an empty state bound to path.
func NewSyncState(path string) *SyncState {
	return &SyncState{Version: StateVersion, Blocks: map[string]string{}, Synced: map[string]string{}, path: path}
}

// LoadState reads the state file. A missing file is an empty state (first
// run); an unreadable, oversized or corrupt file is an error, because treating
// it as empty would silently re-send every block.
func LoadState(path string) (*SyncState, error) {
	st := NewSyncState(path)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrState, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxStateBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read: %v", ErrState, err)
	}
	if len(raw) > MaxStateBytes {
		return nil, fmt.Errorf("%w: larger than %d bytes", ErrState, MaxStateBytes)
	}
	var in SyncState
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrState, err)
	}
	if in.Version != StateVersion {
		return nil, fmt.Errorf("%w: version %d, want %d", ErrState, in.Version, StateVersion)
	}
	if in.Blocks != nil {
		st.Blocks = in.Blocks
	}
	if in.Synced != nil {
		st.Synced = in.Synced
	}
	return st, nil
}

// Save writes the state atomically with mode 0600: a temp file in the same
// directory, fsynced, then renamed over the target, then the directory is
// fsynced. A crash leaves the old file or the new one, never a torn one.
func (s *SyncState) Save() error {
	if s.path == "" {
		return fmt.Errorf("%w: state has no path", ErrState)
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrState, err)
	}
	raw = append(raw, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%w: %v", ErrState, err)
	}
	tmp, err := os.CreateTemp(dir, ".kbsync-state-*")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrState, err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("%w: %v", ErrState, err)
	}
	if _, err := tmp.Write(raw); err != nil {
		return fmt.Errorf("%w: %v", ErrState, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("%w: %v", ErrState, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%w: %v", ErrState, err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("%w: %v", ErrState, err)
	}
	ok = true
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// record notes an accepted upsert and persists it before returning.
func (s *SyncState) record(id, hash string, at time.Time) error {
	s.Blocks[id] = hash
	s.Synced[id] = at.UTC().Format(time.RFC3339)
	return s.Save()
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
