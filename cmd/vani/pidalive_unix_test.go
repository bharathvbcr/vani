//go:build !windows

package main

import (
	"errors"
	"syscall"
	"testing"
)

// An uninspectable pid is not a process this check can see. kill(pid, 0)
// returns EPERM when the pid exists but this process may not signal it.
func TestProcessExistsEPERMIsNotAlive(t *testing.T) {
	err := syscall.Kill(1, 0)
	if !errors.Is(err, syscall.EPERM) {
		t.Fatalf("pid 1: kill returned %v, want EPERM so this check can run", err)
	}
	if processExists(1) {
		t.Error("EPERM from kill(pid, 0) reads as alive")
	}
	if readProcIdentity(1).OK {
		t.Error("an uninspectable pid reads as this process")
	}
}
