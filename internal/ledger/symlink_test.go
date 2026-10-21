package ledger

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// An events.jsonl that is a symlink must stop the daemon, not be replaced by an
// empty log: Open fails closed with ErrLinkedLeaf and leaves link and target be.
func TestOpenRefusesSymlinkedEventsLog(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "history.jsonl")
	const content = "keep me\n"
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, eventsName)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	f, err := Open(dir)
	if err == nil {
		f.Close()
		t.Fatal("Open started on a symlinked events log")
	}
	if !errors.Is(err, atomicfile.ErrLinkedLeaf) {
		t.Errorf("Open = %v, want ErrLinkedLeaf", err)
	}
	if b, _ := os.ReadFile(target); string(b) != content {
		t.Errorf("target changed: %q", b)
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link replaced: %v %v", st, err)
	}
	// The lock was released: a second attempt reports the same error, not ErrLocked.
	if _, err := Open(dir); !errors.Is(err, atomicfile.ErrLinkedLeaf) {
		t.Errorf("second Open = %v, want ErrLinkedLeaf", err)
	}
}

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
