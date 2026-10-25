//go:build unix

package rkbexec

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
)

// spawnScript writes a fake rkb that starts a long-running child (as rkb's fit
// starts Chrome), records the child's PID in the file named by $1 and waits.
// ignoreTerm makes both of them ignore SIGTERM.
func spawnScript(t *testing.T, ignoreTerm bool) (script, pidFile string) {
	t.Helper()
	dir := t.TempDir()
	body := "#!/bin/sh\n"
	if ignoreTerm {
		body += "trap '' TERM\n"
	}
	body += "sleep 300 &\necho $! > \"$1\"\nwait\n"
	script = filepath.Join(dir, "fake-rkb.sh")
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return script, filepath.Join(dir, "child.pid")
}

// childPID waits for the script to record its child's PID and registers a
// cleanup that kills exactly that PID, which this test started, should the
// code under test have left it alive.
func childPID(t *testing.T, pidFile string) int {
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

func gone(pid int) bool {
	deadline := time.Now().Add(2 * time.Second) // init reaps the orphan
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func runSpawn(ctx context.Context, script, pidFile string, timeout time.Duration) error {
	_, err := ExecRunner{}.Run(ctx, Command{Path: script, Args: []string{pidFile}, Env: []string{"PATH=/bin:/usr/bin"}}, timeout)
	return err
}

func TestCancelKillsTheWholeProcessGroup(t *testing.T) {
	script, pidFile := spawnScript(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- runSpawn(ctx, script, pidFile, time.Minute) }()
	child := childPID(t, pidFile)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if !gone(child) {
		t.Fatalf("child %d of the fake rkb survived the cancel (orphaned, as Chrome would be)", child)
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("cancel took %s", d)
	}
}

func TestStepTimeoutKillsTheWholeProcessGroup(t *testing.T) {
	script, pidFile := spawnScript(t, false)
	done := make(chan error, 1)
	go func() { done <- runSpawn(context.Background(), script, pidFile, 400*time.Millisecond) }()
	child := childPID(t, pidFile)
	select {
	case err := <-done:
		if !errors.Is(err, ErrStepTimeout) {
			t.Fatalf("err = %v, want ErrStepTimeout", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after its step timeout")
	}
	if !gone(child) {
		t.Fatalf("child %d of the fake rkb survived the step timeout", child)
	}
}

func TestGroupThatIgnoresSigtermIsKilledWithinTheGrace(t *testing.T) {
	script, pidFile := spawnScript(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runSpawn(ctx, script, pidFile, time.Minute) }()
	child := childPID(t, pidFile)
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return: a group that ignores SIGTERM was not killed")
	}
	if !gone(child) {
		t.Fatalf("child %d that ignores SIGTERM survived", child)
	}
	// waitDelay for rkb, groupGrace for the rest, plus slack for a loaded machine.
	if d := time.Since(start); d > waitDelay+groupGrace+3*time.Second {
		t.Fatalf("stopping the group took %s, over its %s grace", d, waitDelay+groupGrace)
	}
}

func TestFinishedRunDoesNotSignalAnyGroup(t *testing.T) {
	// A step that ends on its own takes the same path as before: no cancel, no
	// group signal, the exit code is the answer.
	out, err := ExecRunner{}.Run(context.Background(), Command{Path: "/bin/sh", Args: []string{"-c", "exit 4"}, Env: []string{}}, 5*time.Second)
	if err != nil || out.Exit != 4 {
		t.Fatalf("out = %+v err = %v", out, err)
	}
}
