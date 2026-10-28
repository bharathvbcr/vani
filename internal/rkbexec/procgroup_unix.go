//go:build unix

package rkbexec

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// configureProcess puts rkb in its own process group and makes cancellation
// signal the whole group. rkb's `fit` starts headless Chrome on a context of its
// own, so killing rkb alone (exec.CommandContext's default) orphans Chrome and
// leaves the page, which carries the owner's header, in Chrome's temp folder.
func configureProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

// adoptGroup is where Windows assigns the child to its job before the child
// runs. On unix the group was set before exec.
func adoptGroup(*exec.Cmd) error { return nil }

// dropGroup releases the Windows job. Unix has no handle to close.
func dropGroup(*exec.Cmd) {}

// reapGroup is called once rkb has been waited for after a cancel or timeout. It
// gives what is left of the group groupGrace to exit, then kills it, so nothing
// of a stopped build runs on. A group that is already gone is not signalled
// again: signal 0 finds out.
func reapGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	deadline := time.Now().Add(groupGrace)
	for syscall.Kill(-pgid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
