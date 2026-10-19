package marks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestReadMarksDropsNotes(t *testing.T) {
	dir := Dir(t.TempDir())
	if m, err := ReadMarks(dir); err != nil || len(m) != 0 {
		t.Fatalf("a store never written reads empty: %v %v", m, err)
	}
	const k2, k3 posting.Key = "gh:acme:2", "gh:acme:3"
	if _, err := SetMark(dir, k1, Hidden, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := SetNote(dir, k1, "private", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := SetNote(dir, k2, "a note, no mark", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := SetMark(dir, k3, Saved, t0); err != nil {
		t.Fatal(err)
	}
	m, err := ReadMarks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m[k1] != Hidden || m[k3] != Saved {
		t.Errorf("marks %v", m)
	}
	if _, ok := m[k2]; ok {
		t.Error("a note-only posting has a mark")
	}
}

// Data without its lock file is not a store that was never written: it must
// not read as "no marks" (the digest would then list every hidden role).
func TestReadDataWithoutLockIsCorrupt(t *testing.T) {
	dir := Dir(t.TempDir())
	if _, err := SetMark(dir, k1, Hidden, t0); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, lockName)); err != nil {
		t.Fatal(err)
	}
	if m, err := ReadMarks(dir); !errors.Is(err, ErrCorrupt) || m != nil {
		t.Errorf("ReadMarks: %v %v", m, err)
	}
	if m, err := Read(dir); !errors.Is(err, ErrCorrupt) || m != nil {
		t.Errorf("Read: %v %v", m, err)
	}
	// An empty directory (created, never written) still reads empty.
	empty := Dir(t.TempDir())
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	if m, err := ReadMarks(empty); err != nil || len(m) != 0 {
		t.Errorf("empty dir: %v %v", m, err)
	}
}

func TestReadMarksCorruptIsAnError(t *testing.T) {
	dir := Dir(t.TempDir())
	if _, err := SetMark(dir, k1, Hidden, t0); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, fileName)
	good, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, append([]byte("{\"v\":1}\n"), good...), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := ReadMarks(dir)
	if !errors.Is(err, ErrCorrupt) || m != nil {
		t.Errorf("corrupt store: %v %v", m, err)
	}
}
