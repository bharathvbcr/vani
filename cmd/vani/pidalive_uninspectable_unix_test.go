//go:build unix

package main

import (
	"errors"
	"syscall"
	"testing"
)

// requireUninspectablePidIsNotLive: pid 1 exists but this process may not
// signal it, so kill(1, 0) is EPERM, and that must not read as alive.
func requireUninspectablePidIsNotLive(t *testing.T) {
	t.Helper()
	err := syscall.Kill(1, 0)
	if !errors.Is(err, syscall.EPERM) {
		t.Fatalf("pid 1: %v, want EPERM", err)
	}
	if processExists(1) {
		t.Fatal("EPERM reads as alive")
	}
	if id := readProcIdentity(1); id.OK || id.Exe != "" || !id.Started.IsZero() {
		t.Fatalf("EPERM identity: %+v", id)
	}
}
