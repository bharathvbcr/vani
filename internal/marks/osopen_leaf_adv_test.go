package marks

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// plantLeafSymlink moves the store file aside and leaves a symlink at its name.
// The lock file stays, which is what lets Read and Compact reach os.Open.
func plantLeafSymlink(t *testing.T) (dir, outside string, before []byte) {
	t.Helper()
	dir = Dir(t.TempDir())
	if _, err := SetMark(dir, k1, Saved, t0); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, fileName)
	before, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	outside = filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.Rename(src, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, src); err != nil {
		t.Fatal(err)
	}
	return dir, outside, before
}

func TestReadDoesNotFollowSymlinkLeaf(t *testing.T) {
	dir, outside, before := plantLeafSymlink(t)
	m, err := Read(dir)
	if err == nil && m[k1].Mark == Saved {
		t.Fatalf("Read followed the symlink and returned the target mark %+v", m[k1])
	}
	if !errors.Is(err, atomicfile.ErrLinkedLeaf) {
		t.Fatalf("Read = %+v, %v; want ErrLinkedLeaf", m, err)
	}
	if b, rerr := os.ReadFile(outside); rerr != nil || !bytes.Equal(b, before) {
		t.Fatalf("target = %q %v", b, rerr)
	}
	st, lerr := os.Lstat(filepath.Join(dir, fileName))
	if lerr != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("leaf = %v %v, want a symlink", st, lerr)
	}
}

func TestCompactDoesNotFollowSymlinkLeaf(t *testing.T) {
	dir, outside, before := plantLeafSymlink(t)
	path := filepath.Join(dir, fileName)
	err := Compact(dir)
	st, lerr := os.Lstat(path)
	if err == nil || (lerr == nil && st.Mode().IsRegular()) {
		body, _ := os.ReadFile(path)
		t.Fatalf("Compact followed the symlink (err=%v, leaf=%v) and rewrote %q", err, st, body)
	}
	if !errors.Is(err, atomicfile.ErrLinkedLeaf) {
		t.Fatalf("Compact = %v, want ErrLinkedLeaf", err)
	}
	if b, rerr := os.ReadFile(outside); rerr != nil || !bytes.Equal(b, before) {
		t.Fatalf("target = %q %v", b, rerr)
	}
	if lerr != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("leaf = %v %v, want a symlink", st, lerr)
	}
}
