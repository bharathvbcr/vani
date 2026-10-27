package feedback

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// A symlink planted after the live log is opened, and before rotation would
// rename it, must not be moved. refuseLeafSwap is the check AppendImpressions
// runs on the opened inode before Rename.
func TestRefuseLeafSwapLeavesPlantedSymlink(t *testing.T) {
	old := maxLogBytes
	maxLogBytes = 8
	t.Cleanup(func() { maxLogBytes = old })

	state := t.TempDir()
	dir := Dir(state)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(dir, ImpressionsFile)
	if err := os.WriteFile(live, []byte("0123456789abcdef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, created, err := openImpression(live)
	if err != nil || created {
		t.Fatalf("open created=%v err=%v", created, err)
	}
	opened, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.jsonl")
	const keep = "keep-target\n"
	if err := os.WriteFile(target, []byte(keep), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(live); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, live); err != nil {
		t.Fatal(err)
	}
	// Same predicate as rotation: the opened file is over the cap, but the
	// path is no longer that file.
	if opened.Size()+1 <= maxLogBytes {
		t.Fatal("fixture is not large enough to rotate")
	}
	if err := refuseLeafSwap(live, opened); !errors.Is(err, atomicfile.ErrLinkedLeaf) {
		t.Fatalf("refuseLeafSwap = %v, want ErrLinkedLeaf", err)
	}
	if st, err := os.Lstat(live); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("live = %v %v, want the planted symlink", st, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, RotatedFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rotation moved the link: %v", err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != keep {
		t.Fatalf("target = %q %v", b, err)
	}
}

func TestRefuseLeafSwapRejectsReplacedRegularFile(t *testing.T) {
	state := t.TempDir()
	dir := Dir(state)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(dir, ImpressionsFile)
	f, created, err := openImpression(live)
	if err != nil || !created {
		t.Fatalf("open created=%v err=%v", created, err)
	}
	opened, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(live); err != nil {
		t.Fatal(err)
	}
	const planted = "other-file\n"
	if err := os.WriteFile(live, []byte(planted), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := refuseLeafSwap(live, opened); err == nil || errors.Is(err, atomicfile.ErrLinkedLeaf) {
		t.Fatalf("refuseLeafSwap = %v, want a replacement error", err)
	}
	if b, err := os.ReadFile(live); err != nil || string(b) != planted {
		t.Fatalf("replacement = %q %v", b, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, RotatedFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rotated file appeared: %v", err)
	}
}
