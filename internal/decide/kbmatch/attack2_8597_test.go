package kbmatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// storeMarshalling builds a store whose indented JSON (the file without its
// trailing newline) is exactly want bytes, by adding fat entries and then one
// entry whose key length lands the total.
func storeMarshalling(t *testing.T, path string, want int) *Store {
	t.Helper()
	s := NewStore(path)
	at := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	tunAt := at.Add(48 * time.Hour)
	tun := func(n int) string { return strings.Repeat("z", n) }
	// What the tunable entry costs at key length 1; each key byte adds one.
	before := marshalLen(s)
	if err := s.Put(tun(1), entry("core", tunAt)); err != nil {
		t.Fatal(err)
	}
	t0 := marshalLen(s) - before
	delete(s.f.Entries, tun(1))

	i := 0
	put := func() {
		if err := s.Put(fmt.Sprintf("%0200d", i), entry("partial", at.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatal(err)
		}
		i++
	}
	put()
	one := marshalLen(s)
	put()
	step := marshalLen(s) - one
	if step > maxKeyBytes-1 {
		t.Fatalf("setup: step %d", step)
	}
	for n := (want - 50000) / step; i < n; {
		put()
	}
	for {
		x := want - marshalLen(s) - t0 + 1
		if x >= 0 && x <= maxKeyBytes-1 {
			if err := s.Put(tun(1+x), entry("core", tunAt)); err != nil {
				t.Fatal(err)
			}
			break
		}
		if s.Len() >= MaxEntries-1 || x < 0 {
			t.Fatalf("setup could not land on %d (x %d, entries %d)", want, x, i)
		}
		put()
	}
	if got := marshalLen(s); got != want {
		t.Fatalf("setup: marshalled %d, want exactly %d", got, want)
	}
	return s
}

// The file is the JSON plus a newline and OpenStore refuses one over
// maxFileBytes, so Save must measure the file, not the JSON: a store whose file
// is limit-1, limit or limit+1 bytes always saves a file that reopens, and only
// the one over the limit loses answers.
func TestSaveAtTheLimitReopens(t *testing.T) {
	for _, tc := range []struct {
		name      string
		file      int // bytes of the file the store would write
		wantEvict bool
	}{
		{"limit-1", maxFileBytes - 1, false},
		{"limit", maxFileBytes, false},
		{"limit+1", maxFileBytes + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			s := storeMarshalling(t, path, tc.file-1)
			n := s.Len()
			if err := s.Save(); err != nil {
				t.Fatalf("Save: %v", err)
			}
			back, err := OpenStore(path)
			if err != nil {
				t.Fatalf("Save wrote a file the store cannot reopen: %v", err)
			}
			if evicted := back.Len() < n; evicted != tc.wantEvict {
				t.Fatalf("entries %d -> %d, evicted=%v, want %v", n, back.Len(), evicted, tc.wantEvict)
			}
		})
	}
}

// Answers Put in this run outlive every older one, even when they tie on At and
// sort first by key.
func TestSaveEvictionTieDoesNotDropTheNewestAnswer(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	s := NewStore(path)
	at := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	pad := strings.Repeat("k", maxKeyBytes-12)
	for i := 0; i < MaxEntries/2; i++ {
		e := entry("partial", at)
		e.Model = strings.Repeat("m", maxModelLen)
		if err := s.Put(fmt.Sprintf("%s:%08d", pad, i), e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	back, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Len() >= MaxEntries/2 {
		t.Fatalf("setup: nothing was evicted (%d entries), the file is not over the limit", back.Len())
	}
	// Reopened: every held answer is from an earlier run. Put one that ties on
	// At and sorts first by key, then overflow the file again.
	if err := back.Put("a:newest", entry("core", at)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		e := entry("partial", at)
		e.Model = strings.Repeat("m", maxModelLen)
		if err := back.Put(fmt.Sprintf("%s:%08d", pad, MaxEntries+i), e); err != nil {
			t.Fatal(err)
		}
	}
	if err := back.Save(); err != nil {
		t.Fatal(err)
	}
	if _, ok := back.Lookup("a:newest", h64a, h64b); !ok {
		t.Fatal("the answer just Put was the first evicted (tie on At broken by key)")
	}
}

// Older answers go before this run's, whatever their At: a loaded entry with a
// time later than a fresh one is still evicted first.
func TestEvictionKeepsThisRunsAnswersOverOlderOnes(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	old := NewStore(path)
	at := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	if err := old.Put("old", entry("core", at.Add(24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := old.Save(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("new", entry("core", at)); err != nil {
		t.Fatal(err)
	}
	s.evictOldest(1)
	if _, ok := s.Lookup("new", h64a, h64b); !ok {
		t.Fatal("this run's answer was evicted before an older one")
	}
	if _, ok := s.Lookup("old", h64a, h64b); ok {
		t.Fatal("the older answer survived")
	}
	s.evictOldest(1)
	if s.Len() != 0 {
		t.Fatalf("this run's answers must go once the older ones are gone, %d left", s.Len())
	}
}

// Put refuses a new answer with no time (it would rank oldest and be the first
// evicted), but a file that already holds one still opens and is evicted first.
func TestZeroAtIsRefusedOnPutButLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	s := NewStore(path)
	if err := s.Put("zz:zero", entry("core", time.Time{})); err == nil {
		t.Fatal("Put accepted an answer with a zero At")
	}
	if s.Len() != 0 {
		t.Fatalf("a refused answer was stored (%d entries)", s.Len())
	}
	raw := `{"version":1,"entries":{"z":{"value":"core","model":"m","kb":"` + h64a + `","text":"` + h64b + `","at":"0001-01-01T00:00:00Z"},"k":{"value":"core","model":"m","kb":"` + h64a + `","text":"` + h64b + `","at":"2026-10-20T00:00:00Z"}}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := OpenStore(path)
	if err != nil {
		t.Fatalf("a file with a zero-At entry no longer opens: %v", err)
	}
	if _, ok := l.Lookup("z", h64a, h64b); !ok {
		t.Fatal("the zero-At entry was not loaded")
	}
	l.evictOldest(1)
	if _, ok := l.Lookup("z", h64a, h64b); ok {
		t.Fatal("the zero-At entry should be evicted first")
	}
}
