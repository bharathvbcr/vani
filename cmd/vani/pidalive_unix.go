//go:build !windows

package main

import "syscall"

// processExists reports whether this process can see pid. kill(pid, 0)
// returns EPERM when the pid exists but this process may not signal it;
// that is not visibility.
func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
