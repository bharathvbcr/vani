//go:build unix

package kbsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/rkbexec"
)

// startFakeRKB writes an rkb that starts a long-running child (as rkb's fit
// starts Chrome), records the child's PID in a file and waits.
func startFakeRKB(t *testing.T) (script, pidFile string) {
	t.Helper()
	dir := t.TempDir()
	script = filepath.Join(dir, "rkb")
	pidFile = filepath.Join(dir, "child.pid")
	body := "#!/bin/sh\nsleep 300 &\necho $! > " + pidFile + "\nwait\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return script, pidFile
}

// recordedChild waits for the fake rkb to record its child and registers a
// cleanup that kills exactly that PID, which this test started, should the code
// under test have left it alive.
func recordedChild(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(pidFile); err == nil && strings.HasSuffix(string(raw), "\n") {
			pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil || pid <= 1 {
				t.Fatalf("bad child pid %q", raw)
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the fake rkb never recorded its child")
	return 0
}

func childGone(pid int) bool {
	deadline := time.Now().Add(3 * time.Second) // init reaps the orphan
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// The claims export runs through the one runner, so a timeout or a cancel stops
// rkb's whole process group, not rkb alone: a Chrome started by rkb is not left
// running.
func TestClaimsExportTimeoutKillsTheProcessGroup(t *testing.T) {
	script, pidFile := startFakeRKB(t)
	done := make(chan error, 1)
	go func() {
		_, err := RKB{Binary: script, Timeout: 400 * time.Millisecond}.Claims(context.Background())
		done <- err
	}()
	child := recordedChild(t, pidFile)
	select {
	case err := <-done:
		if !errors.Is(err, ErrResolve) || !errors.Is(err, rkbexec.ErrStepTimeout) {
			t.Fatalf("err = %v, want an ErrResolve that is ErrStepTimeout", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Claims did not return after its timeout")
	}
	if !childGone(child) {
		t.Fatalf("child %d of the fake rkb survived the claims export timeout", child)
	}
}

func TestClaimsExportCancelKillsTheProcessGroup(t *testing.T) {
	script, pidFile := startFakeRKB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := RKB{Binary: script}.Claims(ctx)
		done <- err
	}()
	child := recordedChild(t, pidFile)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrResolve) || !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want an ErrResolve that is context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Claims did not return after cancel")
	}
	if !childGone(child) {
		t.Fatalf("child %d of the fake rkb survived the cancel", child)
	}
}
