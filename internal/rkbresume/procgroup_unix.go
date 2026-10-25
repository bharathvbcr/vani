//go:build unix

package rkbresume

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// groupGrace is how long the members of rkb's process group that outlived rkb
// get to exit after SIGTERM before they are killed. With waitDelay (how long
// rkb itself gets) the whole stop is bounded by 3 s.
const groupGrace = time.Second

// configureProcess puts rkb in its own process group and makes cancellation
// signal the whole group. rkb's `fit` starts headless Chrome on a context of its
// own, so killing rkb alone (exec.CommandContext's default) orphans Chrome and
// leaves the page, which carries the owner's header, in Chrome's temp folder.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}

// reapGroup is called once rkb has been waited for after a cancel or timeout. It
// gives what is left of the group groupGrace to exit, then kills it, so nothing
// of a stopped build runs on. A group that is already gone is not signalled
// again: signal 0 finds out.
func reapGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
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
