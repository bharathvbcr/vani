//go:build unix

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
