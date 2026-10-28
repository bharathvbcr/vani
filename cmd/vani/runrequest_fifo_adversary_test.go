//go:build !windows

package main

import (
	"encoding/json"
	"io"
	"os"
	"syscall"
	"testing"
	"time"
)

// A FIFO at the request path must not block and must not be followed.
// Opening it here would follow it, so the test only Lstats. An error that
// leaves the pipe in place, or a replacement by a 0600 regular file, both
// meet the contract; success while the path is still a pipe does not.
func TestRequestRunFIFODoesNotBlock(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	announceDaemon(t, stateDir)
	if requestWriteGate != nil {
		t.Fatal("requestWriteGate leaked into this test")
	}
	fifo := runsPath(stateDir, runRequestFile)
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(fifo)
	if err != nil || before.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("fifo setup: %v", err)
	}
	e := &env{configPath: cfgPath, stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}
	type result struct {
		reply runRequestReply
		err   error
	}
	done := make(chan result, 1)
	go func() {
		reply, err := e.requestRun(runOptions{ListOnly: true})
		done <- result{reply, err}
	}()
	var got result
	select {
	case got = <-done:
	case <-time.After(time.Second):
		t.Fatal("requestRun blocked on a FIFO at the request path")
	}
	after, err := os.Lstat(fifo)
	if err != nil {
		t.Fatalf("request path after FIFO: %v", err)
	}
	if after.Mode()&os.ModeNamedPipe != 0 {
		if got.err == nil || got.reply.DaemonPID != 0 || !got.reply.RequestedAt.IsZero() {
			t.Fatalf("followed the FIFO and claimed success: reply %+v err %v", got.reply, got.err)
		}
		if !os.SameFile(before, after) {
			t.Fatalf("FIFO changed identity without being replaced")
		}
		return
	}
	if !after.Mode().IsRegular() || after.Mode().Perm() != 0o600 || after.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("FIFO replacement mode %v, want a 0600 regular file (err %v)", after.Mode(), got.err)
	}
	if got.err != nil {
		return // replaced, and the error is the other half of the contract
	}
	raw, err := os.ReadFile(fifo)
	if err != nil {
		t.Fatal(err)
	}
	var decoded runRequest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("replacement did not unmarshal: %v\n%s", err, raw)
	}
	if !decoded.Options.ListOnly {
		t.Fatalf("replacement options %+v", decoded.Options)
	}
}
