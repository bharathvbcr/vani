package jobfacts

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Save must not decode the existing file on every call: a current-format store
// that is the file this Store last saw costs no header read; a file another
// writer replaced costs exactly one. (Not parallel: headerReads is global.)
func TestSaveSkipsHeaderReadWhileFileIsUnchanged(t *testing.T) {
	v := vocab(t)
	ver := Version(v)
	path := filepath.Join(t.TempDir(), "facts")
	ps := samplePostings()
	put := func(s *Store, i int) {
		s.Put(posting.Key("gh:a:"+string(rune('0'+i))), InputOf(ps[i], [16]byte{byte(i)}), Derive(v, ps[i]))
	}

	s := Open(path, ver) // no file: first Save writes without a header read
	put(s, 0)
	before := headerReads.Load()
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	put(s, 1)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	put(s, 2)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if n := headerReads.Load() - before; n != 0 {
		t.Fatalf("three Saves of an unchanged store did %d header reads, want 0", n)
	}

	// Reopened store of the current format: still none.
	r := Open(path, ver)
	put(r, 3)
	before = headerReads.Load()
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	put(r, 4)
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	if n := headerReads.Load() - before; n != 0 {
		t.Fatalf("Saves after Open of a current store did %d header reads, want 0", n)
	}

	// Another writer replaces the file (current format, different content):
	// exactly one header read, then none again.
	writeRawStore(t, path, storeFile{Format: format, Version: "another"})
	put(r, 0)
	before = headerReads.Load()
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	if n := headerReads.Load() - before; n != 1 {
		t.Fatalf("Save over a replaced file did %d header reads, want exactly 1", n)
	}
	put(r, 1)
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	if n := headerReads.Load() - before; n != 1 {
		t.Fatalf("Save after re-learning the file did more header reads: %d total, want 1", n)
	}
}

// A replacement by a newer build is still caught when the identity differs,
// and a file deleted after Open is written fresh with no header read.
func TestSaveIdentityStillCatchesNewerAndMissing(t *testing.T) {
	v := vocab(t)
	ver := Version(v)
	ps := samplePostings()
	path := filepath.Join(t.TempDir(), "facts")
	s := Open(path, ver)
	s.Put("gh:a:1", InputOf(ps[0], [16]byte{1}), Derive(v, ps[0]))
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	writeRawStore(t, path, storeFile{Format: format + 1, Version: ver})
	s.Put("gh:a:2", InputOf(ps[1], [16]byte{2}), Derive(v, ps[1]))
	if err := s.Save(); !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("Save over a newer replacement: %v, want ErrNewerFormat", err)
	}

	path2 := filepath.Join(t.TempDir(), "facts")
	d := Open(path2, ver)
	d.Put("gh:a:1", InputOf(ps[0], [16]byte{1}), Derive(v, ps[0]))
	if err := d.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path2); err != nil {
		t.Fatal(err)
	}
	d.Put("gh:a:2", InputOf(ps[1], [16]byte{2}), Derive(v, ps[1]))
	before := headerReads.Load()
	if err := d.Save(); err != nil {
		t.Fatal(err)
	}
	if n := headerReads.Load() - before; n != 0 {
		t.Fatalf("Save of a deleted store did %d header reads, want 0", n)
	}
}
