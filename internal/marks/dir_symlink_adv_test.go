package marks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadThroughSymlinkedDir(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "状态")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := SetMark(real, k1, Saved, t0); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	m, err := Read(link)
	if err != nil || m[k1].Mark != Saved {
		t.Fatalf("Read via dir symlink = %+v %v", m, err)
	}
}
