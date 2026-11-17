package indexutil

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// w4bResolved is path as Lock reports it: symlinks in its directory resolved.
func w4bResolved(t *testing.T, path string) string {
	t.Helper()
	rp, err := atomicfile.ResolveLeaf(path)
	if err != nil {
		t.Fatal(err)
	}
	return rp
}

func w4bShorten(t *testing.T, notice, wait time.Duration) {
	t.Helper()
	oldN, oldW := NoticeAfter, LockWait
	NoticeAfter, LockWait = notice, wait
	t.Cleanup(func() { NoticeAfter, LockWait = oldN, oldW })
}

// A wait past NoticeAfter says so exactly once, with the index path, and the
// import still succeeds when the lock frees.
func TestW4bLockNoticeOnceWhileWaiting(t *testing.T) {
	w4bShorten(t, 50*time.Millisecond, 5*time.Second)
	path := filepath.Join(t.TempDir(), "index.json")
	_, release, err := Lock(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int32
	var got atomic.Value
	go func() {
		time.Sleep(400 * time.Millisecond)
		release()
	}()
	_, rel2, err := LockNotify(t.Context(), path, func(p string) { n.Add(1); got.Store(p) })
	if err != nil {
		t.Fatal(err)
	}
	rel2()
	if n.Load() != 1 || got.Load() != w4bResolved(t, path) {
		t.Fatalf("notices = %d for %v, want one for %s", n.Load(), got.Load(), w4bResolved(t, path))
	}
}

// An uncontended lock, or one freed before NoticeAfter, prints nothing.
func TestW4bLockNoNoticeWhenFree(t *testing.T) {
	w4bShorten(t, 100*time.Millisecond, 5*time.Second)
	path := filepath.Join(t.TempDir(), "index.json")
	var n atomic.Int32
	_, rel, err := LockNotify(t.Context(), path, func(string) { n.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	rel()
	time.Sleep(250 * time.Millisecond)
	if n.Load() != 0 {
		t.Fatalf("%d notices for a free lock", n.Load())
	}
}

// Ctrl-C (the context ending) stops the wait at once with the context's own
// error, not ErrBusy, long before LockWait.
func TestW4bLockWaitStopsOnCancel(t *testing.T) {
	w4bShorten(t, time.Hour, time.Minute)
	path := filepath.Join(t.TempDir(), "index.json")
	_, release, err := Lock(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_, _, err = Lock(ctx, path)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrBusy) || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

// A lock still held after LockWait is ErrBusy and still notices once.
func TestW4bLockBusyAfterNotice(t *testing.T) {
	w4bShorten(t, 30*time.Millisecond, 200*time.Millisecond)
	path := filepath.Join(t.TempDir(), "index.json")
	_, release, err := Lock(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var n atomic.Int32
	_, _, err = LockNotify(t.Context(), path, func(string) { n.Add(1) })
	if !errors.Is(err, ErrBusy) || n.Load() != 1 {
		t.Fatalf("err = %v, notices = %d", err, n.Load())
	}
}
