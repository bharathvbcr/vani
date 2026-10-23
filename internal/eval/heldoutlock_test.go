package eval

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// An export that cannot get the export lock in time fails with ErrExportBusy
// and writes nothing; it neither hangs nor touches the existing file.
func TestExportHeldoutLockedBoundedWait(t *testing.T) {
	old := exportLockWait
	exportLockWait = 50 * time.Millisecond
	defer func() { exportLockWait = old }()

	dir := t.TempDir()
	out := filepath.Join(dir, "export", "owner_labels.jsonl")
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		t.Fatal(err)
	}
	holder, err := os.OpenFile(out+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := filelock.TryLock(holder, true); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(filepath.Join(dir, "labels"), filepath.Join(dir, "texts"))
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = ExportHeldoutLocked(out, s, func(posting.Key) (string, bool) { called = true; return "x", true })
	if !errors.Is(err, ErrExportBusy) {
		t.Fatalf("err = %v, want ErrExportBusy", err)
	}
	if called {
		t.Error("labels were read without the lock")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("export file written without the lock: %v", err)
	}
}
