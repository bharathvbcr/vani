//go:build !unix

package rkbresume

import "os/exec"

// configureProcess leaves rkb as exec.CommandContext starts it: Windows has no
// process group to signal here, so a cancelled build stops rkb alone, as it
// always did.
func configureProcess(*exec.Cmd) {}

// reapGroup has no group to reap.
func reapGroup(*exec.Cmd) {}
