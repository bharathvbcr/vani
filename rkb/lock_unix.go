//go:build darwin || linux || freebsd || netbsd || openbsd

package main

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// lockFile takes an exclusive flock on path (created if needed, never
// deleted), retrying until timeout. Where the filesystem does not support
// flock (some network mounts) it proceeds unlocked: compare-and-swap still
// guards every write.
func lockFile(path string, timeout time.Duration) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return func() {}, nil // read-only or exotic mount: rely on CAS
	}
	deadline := time.Now().Add(timeout)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			// Unlocking and closing the lock file are best-effort: the kernel
			// drops the flock when the process exits either way.
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN && err != syscall.EINTR {
			_ = f.Close()
			return func() {}, nil // flock unsupported here: rely on CAS
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("%s is locked by another rkb process for over %s; if none is running, retry", path, timeout)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
