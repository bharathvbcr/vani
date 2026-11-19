package jobfacts

import (
	"bytes"
	"encoding/gob"
	"errors"
	"github.com/bharathvbcr/Vani/internal/posting"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Format values at the int boundaries, negative included: only a Format above
// this build's is read-only; anything else is replaceable.
func TestAttackHeaderFormatBoundaries(t *testing.T) {
	v := vocab(t)
	ver := Version(v)
	type futureFile struct {
		Format  int
		Entries string
	}
	const maxInt = int(^uint(0) >> 1)
	cases := []struct {
		name     string
		format   int
		readOnly bool
	}{
		{"max int", maxInt, true},
		{"format+1", format + 1, true},
		{"current format other layout", format, false},
		{"zero", 0, false},
		{"negative", -1, false},
		{"min int", -maxInt - 1, false},
	}
	for _, c := range cases {
		for _, layout := range []string{"decodes", "changed"} {
			t.Run(c.name+"/"+layout, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "facts")
				var content any = storeFile{Format: c.format, Version: ver}
				if layout == "changed" {
					content = futureFile{Format: c.format, Entries: "x"}
				}
				want := writeRawStore(t, path, content)
				s := Open(path, ver)
				ro := strings.Contains(s.Info(), "read-only")
				if ro != c.readOnly {
					t.Fatalf("Info %q: read-only=%v want %v", s.Info(), ro, c.readOnly)
				}
				s.Put("gh:a:1", InputOf(samplePostings()[0], [16]byte{1}), Derive(v, samplePostings()[0]))
				err := s.Save()
				got, _ := os.ReadFile(path)
				if c.readOnly {
					if !errors.Is(err, ErrNewerFormat) || !bytes.Equal(got, want) {
						t.Fatalf("Save %v, file changed %v", err, !bytes.Equal(got, want))
					}
				} else if err != nil || bytes.Equal(got, want) {
					t.Fatalf("a non-newer store must be replaced: Save %v changed %v", err, !bytes.Equal(got, want))
				}
			})
		}
	}
}

// Same Format, other Version: still replaced.
func TestAttackSameFormatOtherVersionReplaced(t *testing.T) {
	v := vocab(t)
	ver := Version(v)
	path := filepath.Join(t.TempDir(), "facts")
	writeRawStore(t, path, storeFile{Format: format, Version: "other"})
	s := Open(path, ver)
	if strings.Contains(s.Info(), "read-only") {
		t.Fatalf("Info %q", s.Info())
	}
	p := samplePostings()[0]
	s.Put("gh:a:1", InputOf(p, [16]byte{1}), Derive(v, p))
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if Open(path, ver).Info() != "loaded" {
		t.Fatal("not replaced")
	}
}

// Garbage and truncations never claim read-only unless a Format header decodes
// as newer, and never panic.
func TestAttackGarbageAndTruncation(t *testing.T) {
	v := vocab(t)
	ver := Version(v)
	var full bytes.Buffer
	if err := gob.NewEncoder(&full).Encode(storeFile{Format: format + 1, Version: ver, Entries: make([]storeEntry, 50)}); err != nil {
		t.Fatal(err)
	}
	raw := full.Bytes()
	cases := map[string][]byte{
		"empty":       {},
		"one byte":    {0x7f},
		"nul run":     make([]byte, 4096),
		"ff run":      bytes.Repeat([]byte{0xff}, 4096),
		"text":        []byte("not a gob at all"),
		"huge length": {0xf8, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 1, 2, 3},
	}
	for n := 1; n < len(raw); n += 7 {
		cases["truncated"+string(rune('a'+n%26))+strings.Repeat("_", n%5)+itoa(n)] = raw[:n]
	}
	for name, b := range cases {
		path := filepath.Join(t.TempDir(), "facts")
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
		s := Open(path, ver) // must not panic
		if s.Len() != 0 {
			t.Errorf("%s: %d entries from garbage", name, s.Len())
		}
		err := s.Save() // clean store: nothing dirty
		if err != nil && !errors.Is(err, ErrNewerFormat) {
			t.Errorf("%s: Save %v", name, err)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

// A symlink to a newer store is read-only and the target is untouched; a
// directory at the path is neither read-only nor a panic.
func TestAttackSymlinkAndNonRegular(t *testing.T) {
	v := vocab(t)
	ver := Version(v)
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	want := writeRawStore(t, target, storeFile{Format: format + 1, Version: ver})
	link := filepath.Join(dir, "facts")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	s := Open(link, ver)
	p := samplePostings()[0]
	s.Put("gh:a:1", InputOf(p, [16]byte{1}), Derive(v, p))
	if err := s.Save(); !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("Save %v (Info %q)", err, s.Info())
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, want) {
		t.Fatal("target rewritten")
	}
	d := filepath.Join(dir, "adir")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	s = Open(d, ver)
	if strings.Contains(s.Info(), "read-only") {
		t.Fatalf("directory read-only: %q", s.Info())
	}
}

// The file replaced by an older-format one between Open and Save: read-only
// sticks and nothing is written (safe: the older file is left alone).
func TestAttackReplacedByOlderAfterOpen(t *testing.T) {
	v := vocab(t)
	ver := Version(v)
	path := filepath.Join(t.TempDir(), "facts")
	writeRawStore(t, path, storeFile{Format: format + 1, Version: ver})
	s := Open(path, ver)
	older := writeRawStore(t, path, storeFile{Format: format - 1, Version: ver})
	p := samplePostings()[0]
	s.Put("gh:a:1", InputOf(p, [16]byte{1}), Derive(v, p))
	if err := s.Save(); !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("Save %v", err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, older) {
		t.Fatal("older file was written over")
	}
}

// Read-only store: Put/Prune/Dirty/Changes/Get stay coherent in memory, Save
// keeps refusing (never clears dirty), and no lock file appears.
func TestAttackReadOnlySemantics(t *testing.T) {
	v := vocab(t)
	ver := Version(v)
	path := filepath.Join(t.TempDir(), "facts")
	want := writeRawStore(t, path, storeFile{Format: format + 1, Version: ver})
	s := Open(path, ver)
	s.Prune(func(string2 posting.Key) bool { return false }) // empty store: no edits
	if s.Dirty() || s.Changes() != 0 {
		t.Fatalf("Prune on an empty read-only store edited it: dirty %v changes %d", s.Dirty(), s.Changes())
	}
	p := samplePostings()[0]
	in := InputOf(p, [16]byte{1})
	s.Put("gh:a:1", in, Derive(v, p))
	if _, ok := s.Get("gh:a:1", in); !ok || s.Len() != 1 {
		t.Fatal("in-memory put not served")
	}
	for i := 0; i < 3; i++ {
		if err := s.Save(); !errors.Is(err, ErrNewerFormat) {
			t.Fatalf("Save %d: %v", i, err)
		}
	}
	if !s.Dirty() || s.Changes() != 1 {
		t.Fatalf("dirty %v changes %d", s.Dirty(), s.Changes())
	}
	s.Prune(func(k posting.Key) bool { return false })
	if s.Len() != 0 || s.Changes() != 2 {
		t.Fatalf("len %d changes %d", s.Len(), s.Changes())
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, want) {
		t.Fatal("rewritten")
	}
	if _, err := os.Stat(path + ".lock"); err == nil {
		t.Fatal("lock file")
	}
}

// Two goroutines on one read-only store and two Store instances (one older
// reader, one current) racing: the newer file is never rewritten by the
// read-only one, and -race stays quiet.
func TestAttackConcurrentReadOnlyAndCurrent(t *testing.T) {
	v := vocab(t)
	ver := Version(v)
	path := filepath.Join(t.TempDir(), "facts")
	want := writeRawStore(t, path, storeFile{Format: format + 1, Version: ver})
	ro := Open(path, ver)
	p := samplePostings()[0]
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				k := posting.Key("gh:a:" + itoa(g*100+i))
				ro.Put(k, InputOf(p, [16]byte{1}), Derive(v, p))
				_, _ = ro.Get(k, InputOf(p, [16]byte{1}))
				ro.Prune(func(posting.Key) bool { return i%2 == 0 })
				if err := ro.Save(); !errors.Is(err, ErrNewerFormat) {
					t.Errorf("Save %v", err)
					return
				}
				_ = ro.Info()
				_ = ro.Dirty()
			}
		}(g)
	}
	wg.Wait()
	if got, _ := os.ReadFile(path); !bytes.Equal(got, want) {
		t.Fatal("rewritten by a read-only store")
	}
}
