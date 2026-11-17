//go:build !(darwin || linux || freebsd || netbsd || openbsd)

package main

import "os/exec"

// ownGroup is a no-op where process groups are not available: exec's default
// cancellation kills the process itself.
func ownGroup(*exec.Cmd) {}
