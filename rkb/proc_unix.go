//go:build darwin || linux || freebsd || netbsd || openbsd

package main

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// ownGroup puts cmd in a new process group and makes cancellation kill that
// group, not just the leader.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		killGroup(cmd)
		return nil
	}
}

// groupGone bounds how long killGroup waits for the killed members to be
// reaped: they are orphans, so init reaps them, and until it does they still
// answer signal 0.
const groupGone = 2 * time.Second

// killGroup SIGKILLs cmd's whole process group and waits (bounded by
// groupGone) until no member is left. A group with no member (ESRCH) is the
// normal case after a clean exit and is not an error.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pg := -cmd.Process.Pid
	if err := syscall.Kill(pg, syscall.SIGKILL); err != nil {
		if !errors.Is(err, syscall.ESRCH) {
			_ = cmd.Process.Kill() // the group is unreachable; take the leader at least
		}
		return
	}
	for end := time.Now().Add(groupGone); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if errors.Is(syscall.Kill(pg, 0), syscall.ESRCH) {
			return
		}
	}
}
