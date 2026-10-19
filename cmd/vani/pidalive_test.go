package main

import (
	"os"
	"os/exec"
	"testing"
)

// The real liveness check, not the fake the status tests install.
func TestProcessExists(t *testing.T) {
	if !processExists(os.Getpid()) {
		t.Error("this process reads as gone")
	}
	for _, pid := range []int{0, -1} {
		if processExists(pid) {
			t.Errorf("pid %d reads as alive", pid)
		}
	}
	// A child that has exited and been reaped is gone.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if processExists(cmd.Process.Pid) {
		t.Errorf("reaped child %d reads as alive", cmd.Process.Pid)
	}
}
