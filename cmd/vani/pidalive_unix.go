//go:build !windows

package main

import (
	"errors"
	"syscall"
)

// processExists signals pid with 0. A pid it cannot signal for lack of
// permission still exists.
func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
