// Package filelock takes advisory, non-blocking locks on an open file: flock
// on Unix, LockFileEx on Windows. A lock is shared (many holders) or
// exclusive (one), is held by the open file, and is released by Unlock or by
// closing the file; callers Unlock before Close because Windows releases a
// closed handle's locks only eventually.
//
// TryAcquire and Acquire (lock.go) are the whole-file form: a lock file of its
// own, taken exclusively without waiting or waiting until a context ends, for
// work that must not run twice at once.
//
// A file that is replaced by rename cannot carry its own lock, so its lock
// lives beside it at path+".lock". The caller resolves path once (see
// atomicfile.ResolveLeaf and atomicfile.Resolve) and uses that exact path for
// both the lock and the write, so two runners cannot lock one file and write
// another when a link is retargeted between the two steps.
package filelock

import "errors"

// ErrWouldBlock means another open file holds a conflicting lock.
var ErrWouldBlock = errors.New("filelock: held elsewhere")
