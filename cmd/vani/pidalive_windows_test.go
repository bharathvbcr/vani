//go:build windows

package main

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// The System process is pid 4. Opening it is ERROR_ACCESS_DENIED for a normal
// user. That refusal is not a living daemon and not a visible process.
func TestProcessExistsAccessDeniedIsNotAlive(t *testing.T) {
	const systemPID = 4
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, systemPID)
	if err == nil {
		_ = syscall.CloseHandle(h)
		t.Fatal("opened pid 4; this test needs ERROR_ACCESS_DENIED")
	}
	if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		t.Fatalf("OpenProcess(4) = %v, want ERROR_ACCESS_DENIED", err)
	}
	if processExists(systemPID) {
		t.Fatal("ERROR_ACCESS_DENIED reads as alive")
	}
	if readProcIdentity(systemPID).OK {
		t.Fatal("an uninspectable pid reads as this process")
	}
	if processExists(0) || processExists(-1) {
		t.Fatal("a non-pid reads as alive")
	}
	id := readProcIdentity(os.Getpid())
	if !id.OK || id.Exe == "" || id.Started.IsZero() {
		t.Fatalf("this process: %+v", id)
	}
}
