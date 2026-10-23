package eval

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/testutil/symlinktest"
)

func TestPutTextThroughSymlinkedDir(t *testing.T) {
	symlinktest.RunDir(t, func(dir string) error {
		s, err := NewStore(filepath.Join(filepath.Dir(dir), "labels"), dir)
		if err != nil {
			return err
		}
		_, err = s.PutText("a job description")
		return err
	})
}

// The labels lock lives in the label directory: a symlinked spelling of it
// excludes the real one.
func TestLabelsLockIsSharedThroughSymlinkedDir(t *testing.T) {
	old := lockWait
	lockWait = 30 * time.Millisecond
	t.Cleanup(func() { lockWait = old })
	root := t.TempDir()
	realDir := filepath.Join(root, "labels")
	link := filepath.Join(root, "labels-link")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	a, err := NewStore(realDir, filepath.Join(root, "text"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewStore(link, filepath.Join(root, "text"))
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := a.lockLabels()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := b.lockLabels(); !errors.Is(err, ErrBusy) {
		t.Errorf("lock through the link = %v, want ErrBusy", err)
	}
}
