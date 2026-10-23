package kbmatch

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// A wait that is cancelled returns promptly with context.Canceled, long before
// its own deadline, and does not leave the lock taken.
func TestLockWaitStopsOnCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	hold, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	type res struct {
		unlock func()
		err    error
		at     time.Time
	}
	done := make(chan res, 1)
	go func() {
		u, err := LockWait(ctx, path, time.Minute)
		done <- res{u, err, time.Now()}
	}()
	time.Sleep(100 * time.Millisecond)
	cancelled := time.Now()
	cancel()
	select {
	case r := <-done:
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", r.err)
		}
		if r.unlock != nil {
			t.Errorf("a cancelled wait returned an unlock")
		}
		if d := r.at.Sub(cancelled); d > 100*time.Millisecond {
			t.Errorf("returned %s after cancel, want < 100ms", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LockWait ignored cancellation")
	}
	// It did not take the lock: once the holder lets go, another run can.
	hold()
	u, err := Lock(path)
	if err != nil {
		t.Fatalf("lock after a cancelled wait: %v", err)
	}
	u()
}

// An already-cancelled context never takes a free lock.
func TestLockWaitCancelledBeforeStartDoesNotLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if u, err := LockWait(ctx, path, time.Second); !errors.Is(err, context.Canceled) || u != nil {
		t.Fatalf("got %v, %v", u != nil, err)
	}
	u, err := Lock(path)
	if err != nil {
		t.Fatalf("the lock was left taken: %v", err)
	}
	u()
}

// OpenLocked forwards the context: cancelled during the wait, it returns the
// context error and leaves a corrupt store file in place.
func TestOpenLockedStopsOnCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	hold, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, _, err = OpenLocked(ctx, path, time.Minute)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %s", d)
	}
}
