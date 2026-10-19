// Package filelock takes advisory, non-blocking locks on an open file: flock
// on Unix, LockFileEx on Windows. A lock is shared (many holders) or
// exclusive (one), is held by the open file, and is released by Unlock or by
// closing the file; callers Unlock before Close because Windows releases a
// closed handle's locks only eventually.
package filelock

import "errors"

// ErrWouldBlock means another open file holds a conflicting lock.
var ErrWouldBlock = errors.New("filelock: held elsewhere")
