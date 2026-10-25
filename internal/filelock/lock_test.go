package filelock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTryAcquireIsExclusiveAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "x.lock") // the directory is made
	a, err := TryAcquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TryAcquire(path); !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("second TryAcquire: %v, want ErrWouldBlock", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("lock file mode %v, want 0600", st.Mode().Perm())
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	if err := a.Release(); err != nil {
		t.Fatalf("second Release: %v, want nil", err)
	}
	b, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("TryAcquire after Release: %v", err)
	}
	_ = b.Release()
}

func TestAcquireWaitsForTheHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	a, err := TryAcquire(path)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = a.Release()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := Acquire(ctx, path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	_ = b.Release()
}

func TestAcquireStopsWhenTheContextEnds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	a, err := TryAcquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Acquire(ctx, path)
	if !errors.Is(err, ErrWouldBlock) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire under a held lock: %v, want ErrWouldBlock and DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Acquire took %s past a 80ms bound", d)
	}
	// The failed wait did not leave the lock taken.
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	c, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("TryAcquire after the holder released: %v", err)
	}
	_ = c.Release()
}

func TestAcquireWithACancelledContextStillTakesAFreeLock(t *testing.T) {
	// A free lock is taken even when ctx is already done: the first attempt
	// does not look at ctx, so "no wait needed" is never reported as a timeout.
	path := filepath.Join(t.TempDir(), "x.lock")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l, err := Acquire(ctx, path)
	if err != nil {
		t.Fatalf("Acquire of a free lock with a done context: %v", err)
	}
	_ = l.Release()
}
