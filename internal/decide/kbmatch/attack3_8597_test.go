package kbmatch

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var a3base = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)

func a3key(i int) string { return fmt.Sprintf("%s%06d", strings.Repeat("k", 480), i) }

// A store holding only this run's answers over the byte limit drops the oldest
// of them, keeps the newest, counts what it dropped, and reopens.
func TestAttackOnlyFreshAnswersOverTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	s := NewStore(path)
	const n = 16000
	for i := 0; i < n; i++ {
		if err := s.Put(a3key(i), entry("core", a3base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatal(err)
		}
		if s.Len() > MaxEntries {
			t.Fatal("over MaxEntries")
		}
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if s.Dropped() == 0 || s.Dropped() != n-s.Len() {
		t.Fatalf("dropped %d, held %d of %d", s.Dropped(), s.Len(), n)
	}
	if _, ok := s.Lookup(a3key(n-1), h64a, h64b); !ok {
		t.Fatal("the newest answer was dropped")
	}
	if _, ok := s.Lookup(a3key(0), h64a, h64b); ok {
		t.Fatal("the oldest answer survived")
	}
	r, err := OpenStore(path)
	if err != nil || r.Len() != s.Len() || r.Dropped() != 0 {
		t.Fatalf("reopen: %v len %d dropped %d", err, r.Len(), r.Dropped())
	}
	// A reopened store Saved again drops nothing and counts nothing.
	if err := r.Save(); err != nil || r.Dropped() != 0 || r.Len() != s.Len() {
		t.Fatalf("resave: %v dropped %d len %d", err, r.Dropped(), r.Len())
	}
}

// Put that overwrites an old key makes it this run's: it outlives the older ones.
func TestAttackOverwriteOfAnOldKeyIsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	old := NewStore(path)
	const n = 9000
	for i := 0; i < n; i++ {
		if err := old.Put(a3key(i), entry("partial", a3base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Save(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	held := s.Len()
	// Overwrite the oldest stored key with a time older than every other entry.
	oldest := a3key(n - held)
	if err := s.Put(oldest, entry("core", a3base.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	for i := n; i < n+4000; i++ {
		if err := s.Put(a3key(i), entry("core", a3base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if s.Dropped() == 0 {
		t.Fatal("setup: nothing dropped")
	}
	e, ok := s.Lookup(oldest, h64a, h64b)
	if !ok || e.Value != "core" {
		t.Fatalf("the overwritten answer was evicted before older ones: %+v %v", e, ok)
	}
}

// Dropped accumulates across Saves and does not change when a Save drops nothing.
func TestAttackDroppedAcrossTwoSaves(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	s := NewStore(path)
	put := func(from, to int) {
		for i := from; i < to; i++ {
			if err := s.Put(a3key(i), entry("core", a3base.Add(time.Duration(i)*time.Second))); err != nil {
				t.Fatal(err)
			}
		}
	}
	put(0, 12000)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	d1 := s.Dropped()
	if d1 == 0 {
		t.Fatal("setup: nothing dropped")
	}
	if err := s.Save(); err != nil || s.Dropped() != d1 {
		t.Fatalf("a no-op Save changed Dropped: %d -> %d (%v)", d1, s.Dropped(), err)
	}
	put(12000, 14000)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if s.Dropped() <= d1 || s.Dropped() != 14000-s.Len() {
		t.Fatalf("dropped %d (was %d), held %d of 14000", s.Dropped(), d1, s.Len())
	}
}

// A Put at MaxEntries that evicts a key leaves the store at the bound.
func TestAttackPutAtMaxEntriesStaysBounded(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), FileName))
	for i := 0; i < MaxEntries+50; i++ {
		if err := s.Put(fmt.Sprintf("k%d", i), entry("core", a3base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	if s.Len() != MaxEntries {
		t.Fatalf("len %d", s.Len())
	}
	if _, ok := s.Lookup(fmt.Sprintf("k%d", MaxEntries+49), h64a, h64b); !ok {
		t.Fatal("newest missing")
	}
}

// The answer Put is never the one it evicts, even when its At is older than
// every held answer; the oldest other answer goes instead, and Dropped (Save's
// byte-limit count) does not count Put's evictions.
func TestPutKeepsTheAnswerItWasGiven(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), FileName))
	for i := 0; i < MaxEntries; i++ {
		if err := s.Put(fmt.Sprintf("k%d", i), entry("core", a3base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Put("late", entry("core", a3base.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Lookup("late", h64a, h64b); !ok {
		t.Fatal("Put returned nil but the paid answer is not in the store")
	}
	if _, ok := s.Lookup("k0", h64a, h64b); ok {
		t.Fatal("the oldest other answer was kept")
	}
	if s.Len() != MaxEntries || s.Dropped() != 0 {
		t.Fatalf("len=%d dropped=%d", s.Len(), s.Dropped())
	}
	// Replacing an existing key at the bound evicts nothing.
	if err := s.Put("late", entry("partial", a3base.Add(-2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if s.Len() != MaxEntries {
		t.Fatalf("len=%d after replacing", s.Len())
	}
	if _, ok := s.Lookup("k1", h64a, h64b); !ok {
		t.Fatal("a replace evicted another answer")
	}
}
