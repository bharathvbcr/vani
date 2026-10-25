package filelock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// pollEvery is how often Acquire retries a held lock. flock has no timed wait,
// so a bounded wait is a poll.
const pollEvery = 25 * time.Millisecond

// Lock is an exclusive lock on a file of its own, held until Release or until
// the process ends. It is the one cross-process mutex the commands use for work
// that must not run twice at once (a paid model turn, a headless Chrome).
type Lock struct {
	mu sync.Mutex
	f  *os.File
}

// TryAcquire takes the exclusive lock on path without waiting, creating the
// file (0600) and its directory (0700) when they are missing. A lock held
// elsewhere is ErrWouldBlock.
func TryAcquire(path string) (*Lock, error) {
	f, err := openLockFile(path)
	if err != nil {
		return nil, err
	}
	if err := TryLock(f, true); err != nil {
		_ = f.Close()
		if errors.Is(err, ErrWouldBlock) {
			return nil, ErrWouldBlock
		}
		return nil, fmt.Errorf("filelock: lock: %w", err)
	}
	return &Lock{f: f}, nil
}

// Acquire takes the exclusive lock on path, waiting until ctx ends. When it
// ends first the error wraps both ErrWouldBlock and the context's error, so a
// caller can tell "still held" from "the build ran out of time".
func Acquire(ctx context.Context, path string) (*Lock, error) {
	f, err := openLockFile(path)
	if err != nil {
		return nil, err
	}
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		err := TryLock(f, true)
		if err == nil {
			return &Lock{f: f}, nil
		}
		if !errors.Is(err, ErrWouldBlock) {
			_ = f.Close()
			return nil, fmt.Errorf("filelock: lock: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("%w: %w", ErrWouldBlock, context.Cause(ctx))
		case <-tick.C:
		}
	}
}

func openLockFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("filelock: create dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("filelock: open lock: %w", err)
	}
	return f, nil
}

// Release gives the lock back. It may be called more than once.
func (l *Lock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	uerr := Unlock(l.f)
	cerr := l.f.Close()
	l.f = nil
	return errors.Join(uerr, cerr)
}
