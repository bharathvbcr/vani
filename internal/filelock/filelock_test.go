package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func open(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// Two opens of one lock file stand for two processes: the lock belongs to
// the open file, on Unix and Windows alike.
func TestSharedAndExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	a, b := open(t, path), open(t, path)

	if err := TryLock(a, false); err != nil {
		t.Fatal(err)
	}
	if err := TryLock(b, false); err != nil {
		t.Fatalf("second shared lock: %v", err)
	}
	if err := Unlock(b); err != nil {
		t.Fatal(err)
	}
	if err := TryLock(b, true); !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("exclusive over a shared lock: %v, want ErrWouldBlock", err)
	}
	if err := Unlock(a); err != nil {
		t.Fatal(err)
	}
	if err := TryLock(b, true); err != nil {
		t.Fatalf("exclusive once free: %v", err)
	}
	if err := TryLock(a, false); !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("shared over an exclusive lock: %v, want ErrWouldBlock", err)
	}
	if err := Unlock(b); err != nil {
		t.Fatal(err)
	}
	if err := TryLock(a, true); err != nil {
		t.Fatalf("exclusive after unlock: %v", err)
	}
}

// Closing the file gives the lock back too.
func TestCloseReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	a, b := open(t, path), open(t, path)
	if err := TryLock(a, true); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := TryLock(b, true); err != nil {
		t.Fatalf("lock after the holder closed: %v", err)
	}
}
