package kbmatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func marshalLen(s *Store) int {
	raw, _ := json.MarshalIndent(s.f, "", " ")
	return len(raw)
}

// A store whose JSON is exactly maxFileBytes passes Save's check (> limit), but
// the file gets a trailing newline, so it is maxFileBytes+1 and OpenStore reads
// it as corrupt (and OpenLocked then deletes it).
// Entries sharing one At are evicted in key order, so the newest answer can be
// the one that goes when its key sorts first.
// An answer with a zero At is evicted before any other: Put accepts it.
// The largest single entry is far under the limit, so one entry can never make
// Save drop everything.
func TestOneMaximalEntryAlwaysSaves(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	s := NewStore(path)
	e := entry("core", time.Now().UTC())
	e.Model = strings.Repeat("m", maxModelLen)
	e.Backend = strings.Repeat("b", 32)
	e.Mode = ModeFocused
	if err := s.Put(strings.Repeat("k", maxKeyBytes), e); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Size() > 4<<10 {
		t.Fatalf("one maximal entry is %d bytes", info.Size())
	}
}

// A store already trimmed (or loaded from disk near the limit) saves again and
// again without changing and without losing the newest answer.
func TestSaveTwiceAndAfterReloadIsStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	s := NewStore(path)
	at := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	fatEntries(t, s, MaxEntries/2, at)
	if err := s.Put("gh:new:1", entry("core", at.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatal("a second Save changed the file")
	}
	back, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := back.Put("gh:new:2", entry("core", at.Add(2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := back.Save(); err != nil {
		t.Fatalf("Save after reload near the limit: %v", err)
	}
	again, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen after second trim: %v", err)
	}
	for _, k := range []string{"gh:new:1", "gh:new:2"} {
		if _, ok := again.Lookup(k, h64a, h64b); !ok {
			t.Errorf("%s lost", k)
		}
	}
}
