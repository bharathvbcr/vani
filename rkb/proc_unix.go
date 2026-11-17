//go:build darwin || linux || freebsd || netbsd || openbsd

package main

import (
	"os/exec"
	"syscall"
)

// ownGroup puts cmd in a new process group and makes cancellation kill that
// group, not just the leader.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
