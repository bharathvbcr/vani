package marks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The marks lock lives in the store directory: a symlinked spelling of it
// excludes the real one.
func TestLockIsSharedThroughSymlinkedDir(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	link := filepath.Join(root, "link")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	l, err := flock(realDir, true, true, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := flock(link, true, true, time.Now().Add(30*time.Millisecond)); !errors.Is(err, ErrBusy) {
		t.Errorf("lock through the link = %v, want ErrBusy", err)
	}
}
