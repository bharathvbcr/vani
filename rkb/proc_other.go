//go:build !(darwin || linux || freebsd || netbsd || openbsd)

package main

import "os/exec"

// ownGroup is a no-op where process groups are not available: exec's default
// cancellation kills the process itself.
func ownGroup(*exec.Cmd) {}

// killGroup kills the process itself; there is no group to reach here.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill() // already-exited is the normal case
	}
}
