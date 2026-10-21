// Package filelock takes advisory, non-blocking locks on an open file: flock
// on Unix, LockFileEx on Windows. A lock is shared (many holders) or
// exclusive (one), is held by the open file, and is released by Unlock or by
// closing the file; callers Unlock before Close because Windows releases a
// closed handle's locks only eventually.
package filelock

import (
	"errors"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// LockPath is the advisory lock file for a file that is replaced by rename and
// so cannot carry its own lock: the resolved path (atomicfile.Resolve) plus
// ".lock". Resolving first makes a symlink and its target, or two spellings of
// one directory, take the same lock. A directory-scoped lock (dir/lock) needs
// no helper: the OS already resolves the directory.
func LockPath(path string) (string, error) {
	r, err := atomicfile.Resolve(path)
	if err != nil {
		return "", err
	}
	return r + ".lock", nil
}

// ErrWouldBlock means another open file holds a conflicting lock.
var ErrWouldBlock = errors.New("filelock: held elsewhere")
