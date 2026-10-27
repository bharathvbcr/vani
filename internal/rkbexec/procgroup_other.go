//go:build !unix && !windows

package rkbexec

import "os/exec"

// configureProcess leaves the child as exec.CommandContext starts it.
// js, wasip1 and plan9 have no process group or job object to signal.
func configureProcess(*exec.Cmd) error { return nil }

// adoptGroup is the Windows hook that assigns a suspended child to its job.
func adoptGroup(*exec.Cmd) error { return nil }

// dropGroup releases any job held for the child. There is none here.
func dropGroup(*exec.Cmd) {}

// reapGroup has no tree to reap. A nil process is a no-op.
func reapGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
}
