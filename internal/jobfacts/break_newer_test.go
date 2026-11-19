package jobfacts

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// BREAK candidate 1: a newer-format store opened as current, then a newer build
// writes it before this Save. Save does not recheck under the lock.
func TestBreakNewerWrittenAfterOpen(t *testing.T) {
	v := vocab(t)
	ver := Version(v)
	path := filepath.Join(t.TempDir(), "facts")
	s := Open(path, ver) // no store yet: writable
	newer := writeRawStore(t, path, storeFile{Format: format + 1, Version: ver})
	p := samplePostings()[0]
	s.Put("gh:a:1", InputOf(p, [16]byte{1}), Derive(v, p))
	err := s.Save()
	got, _ := os.ReadFile(path)
	if !errors.Is(err, ErrNewerFormat) || !bytes.Equal(got, newer) {
		t.Fatalf("Save %v; newer store overwritten: %v", err, !bytes.Equal(got, newer))
	}
}

// BREAK candidate 2: a newer-format file over maxStoreBytes (sparse) is treated
// as "not a plausible size", not read-only, so Save replaces it.
func TestBreakOversizeNewerStore(t *testing.T) {
	v := vocab(t)
	ver := Version(v)
	path := filepath.Join(t.TempDir(), "facts")
	head := writeRawStore(t, path, storeFile{Format: format + 1, Version: ver})
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxStoreBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	s := Open(path, ver)
	p := samplePostings()[0]
	s.Put("gh:a:1", InputOf(p, [16]byte{1}), Derive(v, p))
	err = s.Save()
	got := make([]byte, len(head))
	g, _ := os.Open(path)
	g.Read(got)
	g.Close()
	if !errors.Is(err, ErrNewerFormat) || !bytes.Equal(got, head) {
		t.Fatalf("Info %q; Save %v; oversize newer store overwritten: %v", s.Info(), err, !bytes.Equal(got, head))
	}
}
