package feedback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The feedback lock lives in the store directory: a symlinked spelling of it
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
	unlock, err := lock(context.Background(), realDir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := lock(context.Background(), link); !errors.Is(err, ErrBusy) {
		t.Errorf("lock through the link = %v, want ErrBusy", err)
	}
}
