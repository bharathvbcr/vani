package indexutil

import (
	"context"
	"errors"
	"fmt"
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

// Lock takes the exclusive import lock of the index file at path (path+".lock",
// beside it, because the index is replaced by rename and cannot carry its own
// lock). It returns the resolved path, which the caller must use for both the
// read and the write so a link retargeted between the two steps cannot split
// them, and the release func. A lock still held after LockWait, or a ctx that
// ends first, is ErrBusy.
func Lock(ctx context.Context, path string) (string, func(), error) {
	rp, err := atomicfile.ResolveLeaf(path)
	if err != nil {
		return "", nil, err
	}
	lctx, cancel := context.WithTimeout(ctx, LockWait)
	defer cancel()
	l, err := filelock.Acquire(lctx, rp+".lock")
	if err != nil {
		if errors.Is(err, filelock.ErrWouldBlock) {
			return "", nil, fmt.Errorf("%w: %w", ErrBusy, err)
		}
		return "", nil, err
	}
	return rp, func() { _ = l.Release() }, nil
}
