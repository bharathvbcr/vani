package inbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/testutil/symlinktest"
)

func TestSpoolThroughSymlinkedDir(t *testing.T) {
	symlinktest.RunDir(t, func(dir string) error {
		_, err := Write(dir, batchOf("acme", Job{Title: "Engineer", URL: "https://careers.acme.com/1"}))
		return err
	})
}

// A drain through a symlinked spelling of the spool excludes a drain of the
// real directory.
func TestDrainLockIsSharedThroughSymlinkedDir(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	link := filepath.Join(root, "link")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(realDir, batchOf("acme", Job{Title: "Engineer", URL: "https://careers.acme.com/1"})); err != nil {
		t.Fatal(err)
	}
	var inner error
	_, err := Drain(context.Background(), realDir, 5, func(Batch) error {
		_, inner = Drain(context.Background(), link, 5, func(Batch) error { return nil })
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(inner, ErrBusy) {
		t.Errorf("drain through the link = %v, want ErrBusy", inner)
	}
}
