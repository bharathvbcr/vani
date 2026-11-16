//go:build !(darwin || linux || freebsd || netbsd || openbsd)

package main

import "time"

// lockFile is a no-op where flock is unavailable (e.g. Windows); every shared
// write is still guarded by compare-and-swap in guard.commit.
func lockFile(path string, timeout time.Duration) (func(), error) { return func() {}, nil }
