package ledger

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The ledger lock and events file live in the directory: opening it through a
// symlinked spelling finds the real directory locked and the events file kept.
func TestOpenThroughSymlinkedDirSharesTheLock(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	link := filepath.Join(root, "link")
	f := mustOpen(t, realDir)
	defer f.Close()
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); !errors.Is(err, ErrLocked) {
		t.Errorf("Open through the link = %v, want ErrLocked", err)
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link not kept: %v %v", st, err)
	}
}
