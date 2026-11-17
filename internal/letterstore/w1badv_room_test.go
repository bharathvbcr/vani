package letterstore

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// w1badvTimed runs f and fails the test if it has not returned within d.
func w1badvTimed(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %s", what, d)
	}
}

// Room must not answer "room" where Save refuses before spending: a posting
// folder the user cannot write into fails Save at the first write.
func TestW1badvRoomAgreesWithSaveOnAReadOnlyPostingFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.Save(ctx, "gh:acme:1", "First letter.", "", nil); err != nil {
		t.Fatal(err)
	}
	dir := s.dir("gh:acme:1")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	rerr := s.Room(ctx, "gh:acme:1")
	_, serr := s.Save(ctx, "gh:acme:1", "Second letter.", "", nil)
	if serr == nil {
		t.Fatal("precondition: Save succeeded in a read-only folder")
	}
	if rerr == nil {
		t.Fatalf("Room said there is room, Save refused with %v (the model would be paid for a letter that cannot be kept)", serr)
	}
}

// A posting folder whose .lock is a directory: Save cannot take the per-posting
// lock, Room never tries.
func TestW1badvRoomAgreesWithSaveWhenThePostingLockIsADirectory(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.Save(ctx, "gh:acme:1", "First letter.", "", nil); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(s.dir("gh:acme:1"), ".lock")
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	rerr := s.Room(ctx, "gh:acme:1")
	_, serr := s.Save(ctx, "gh:acme:1", "Second letter.", "", nil)
	if serr == nil {
		t.Fatal("precondition: Save succeeded with a directory for a lock")
	}
	if rerr == nil {
		t.Fatalf("Room said there is room, Save refused with %v", serr)
	}
}

// A FIFO where the version pointer belongs: Save reads it with os.ReadFile and
// blocks forever, after the model was paid. The detached context cannot help
// because the read is not context-aware.
func TestW1badvSaveIsBoundedWhenCurrentIsAFifo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s := testStore(t)
	if _, err := s.Save(context.Background(), "gh:acme:1", "First letter.", "", nil); err != nil {
		t.Fatal(err)
	}
	cur := filepath.Join(s.dir("gh:acme:1"), "current")
	if err := os.Remove(cur); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(cur, 0o600); err != nil {
		t.Skipf("no fifo: %v", err)
	}
	// Unblock the stuck reader at the end so the goroutine does not leak.
	t.Cleanup(func() {
		if f, err := os.OpenFile(cur, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
	})
	w1badvTimed(t, 4*time.Second, "Save with a FIFO for current", func() {
		_, _ = s.Save(ctx, "gh:acme:1", "Second letter.", "", nil)
	})
}
