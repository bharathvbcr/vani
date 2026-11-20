//go:build unix

package main

import "syscall"

func init() { makeFIFO = func(path string) error { return syscall.Mkfifo(path, 0o600) } }
