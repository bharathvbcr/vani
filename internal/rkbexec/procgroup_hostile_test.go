package rkbexec

import (
	"os/exec"
	"testing"
)

func TestNilCmdAndDoubleReapDropDoNotPanic(t *testing.T) {
	reapGroup(nil)
	reapGroup(nil)
	reapGroup(&exec.Cmd{})
	reapGroup(&exec.Cmd{})
	dropGroup(nil)
	dropGroup(nil)
	cmd := &exec.Cmd{}
	dropGroup(cmd)
	dropGroup(cmd)
	if err := adoptGroup(nil); err != nil {
		t.Fatal(err)
	}
	if err := adoptGroup(&exec.Cmd{}); err != nil {
		t.Fatal(err)
	}
}
