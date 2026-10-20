package semantic

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bharathvbcr/Vani/internal/filelock"
)

// lockFileName is the inter-process lock for a cache directory. A Warm holds
// it exclusively from reading the score file to writing it, so two processes
// (the daemon's warm and a manual `vani semantic warm`) cannot lose each
// other's entries. Lookup never takes it: it reads an atomically replaced file.
const lockFileName = "semantic.lock"

// DefaultLockWait is how long a Warm waits for another warm to finish.
const DefaultLockWait = 30 * time.Second

const lockPoll = 50 * time.Millisecond

// ErrWarmBusy means another warm held the cache lock for the whole wait, so
// this one did not run. It is a skipped warm, not a scored one.
var ErrWarmBusy = errors.New("semantic warm skipped: another warm is running")

// lockCache takes the exclusive cache lock in dir, waiting up to wait. The
// returned func releases it.
func lockCache(ctx context.Context, dir string, wait time.Duration) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create dir: %v", ErrCache, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, lockFileName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%w: open lock: %v", ErrCache, err)
	}
	deadline := time.Now().Add(wait)
	for {
		err := filelock.TryLock(f, true)
		if err == nil {
			return func() { _ = filelock.Unlock(f); _ = f.Close() }, nil
		}
		if !errors.Is(err, filelock.ErrWouldBlock) {
			f.Close()
			return nil, fmt.Errorf("%w: lock: %v", ErrCache, err)
		}
		if cerr := ctx.Err(); cerr != nil {
			f.Close()
			return nil, cerr
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, ErrWarmBusy
		}
		select {
		case <-ctx.Done():
		case <-time.After(lockPoll):
		}
	}
}
