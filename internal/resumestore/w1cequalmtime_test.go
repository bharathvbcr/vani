package resumestore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/filelock"
)

// A version saved in the window whose modification time equals the newest one
// the listing saw (a coarse-timestamp filesystem: HFS+ 1 s, FAT 2 s) changes the
// folder's set of versions, so makeRoom keeps the folder.
func TestW1cMakeRoomKeepsAFolderSavedWithAnEqualMtime(t *testing.T) {
	s, state := newStore(t)
	root, name := w1cFull(t, state)
	v1 := filepath.Join(root, name(0), "v1.json")
	if err := os.WriteFile(v1, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	at := t0.Add(-1)
	if err := os.Chtimes(v1, at, at); err != nil {
		t.Fatal(err)
	}
	w1cHook(t, func(dir string) {
		if filepath.Base(dir) != name(0) {
			return
		}
		l, err := filelock.TryAcquire(filepath.Join(dir, lockFile))
		if err != nil {
			t.Error(err)
			return
		}
		defer l.Release()
		v2 := filepath.Join(dir, "v2.json")
		if err := os.WriteFile(v2, []byte("{}"), 0o600); err != nil {
			t.Error(err)
			return
		}
		if err := os.Chtimes(v2, at, at); err != nil {
			t.Error(err)
		}
	})
	mustSave(t, s, "fresh", goodMeta(), pdfBytes("1"))
	if _, err := os.Stat(filepath.Join(root, name(0), "v2.json")); err != nil {
		t.Fatalf("a folder saved in the window with an equal mtime was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, name(1))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the next candidate survived: %v", err)
	}
}

// versionState distinguishes two folders whose newest times are equal.
func TestW1cVersionStateSeesASetChangeAtAnEqualTime(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"v1.json"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(dir, n), t0, t0); err != nil {
			t.Fatal(err)
		}
	}
	n1, names1 := versionState(dir)
	if err := os.WriteFile(filepath.Join(dir, "v2.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, "v2.json"), t0, t0); err != nil {
		t.Fatal(err)
	}
	n2, names2 := versionState(dir)
	if !n1.Equal(n2) || names1 == names2 {
		t.Errorf("newest %v/%v names %q/%q: want equal times and different sets", n1, n2, names1, names2)
	}
}
