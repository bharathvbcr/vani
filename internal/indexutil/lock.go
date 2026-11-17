package indexutil

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/filelock"
)

// ErrBusy means another import held the index lock for longer than LockWait.
// Importing again once that one has finished is safe.
var ErrBusy = errors.New("index is busy: another import is writing it; retry in a moment")

// LockWait bounds how long an import waits for another import of the same
// index. A variable so a test can shorten it.
var LockWait = 30 * time.Second

// NoticeAfter is how long an import waits for the lock before telling the
// person it is waiting. A variable so a test can shorten it.
var NoticeAfter = 2 * time.Second

// Lock is LockNotify with no notice.
func Lock(ctx context.Context, path string) (string, func(), error) {
	return LockNotify(ctx, path, nil)
}

// LockNotify takes the exclusive import lock of the index file at path
// (path+".lock", beside it, because the index is replaced by rename and cannot
// carry its own lock). It returns the resolved path, which the caller must use
// for both the read and the write so a link retargeted between the two steps
// cannot split them, and the release func. A lock still held after LockWait is
// ErrBusy; a ctx that ends first is that ctx's error. When the lock is still
// held after NoticeAfter, waiting (if non-nil) is called once with the
// resolved path, from another goroutine.
func LockNotify(ctx context.Context, path string, waiting func(index string)) (string, func(), error) {
	rp, err := atomicfile.ResolveLeaf(path)
	if err != nil {
		return "", nil, err
	}
	lctx, cancel := context.WithTimeout(ctx, LockWait)
	defer cancel()
	if waiting != nil {
		// The notice never runs once LockNotify has returned, so a caller
		// may stop reading what it writes.
		var mu sync.Mutex
		done := false
		t := time.AfterFunc(NoticeAfter, func() {
			mu.Lock()
			defer mu.Unlock()
			if !done {
				waiting(rp)
			}
		})
		defer func() {
			t.Stop()
			mu.Lock()
			done = true
			mu.Unlock()
		}()
	}
	l, err := filelock.Acquire(lctx, rp+".lock")
	if err != nil {
		if cerr := context.Cause(ctx); cerr != nil {
			return "", nil, cerr
		}
		if errors.Is(err, filelock.ErrWouldBlock) {
			return "", nil, fmt.Errorf("%w: %w", ErrBusy, err)
		}
		return "", nil, err
	}
	return rp, func() { _ = l.Release() }, nil
}
