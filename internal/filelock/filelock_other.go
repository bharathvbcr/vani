//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows)

package filelock

import (
	"errors"
	"fmt"
	"os"
	"runtime"
)

// TryLock fails on a platform with no file locking Vani knows how to take:
// running without the lock would let two processes write one store.
func TryLock(*os.File, bool) error {
	return fmt.Errorf("filelock: no file locking on %s: %w", runtime.GOOS, errors.ErrUnsupported)
}

// Unlock fails as TryLock does.
func Unlock(*os.File) error {
	return fmt.Errorf("filelock: no file locking on %s: %w", runtime.GOOS, errors.ErrUnsupported)
}
