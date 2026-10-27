//go:build !windows && !darwin && !linux

package main

// readProcIdentity has no kernel lookup on this OS. An unknown kernel is not
// this daemon; guessing from the pid alone would repeat the bug.
func readProcIdentity(int) procIdentity { return procIdentity{} }
