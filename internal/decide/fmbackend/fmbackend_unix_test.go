//go:build unix

package fmbackend

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

	"github.com/bharathvbcr/decide"
)

func pidAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// recordedPID reads the pid the hanging helper wrote, if it wrote all of it.
// When it returns one it also registers a cleanup that kills exactly that pid,
// which this test started, should the code under test have left it alive.
func recordedPID(t *testing.T, path string) (int, bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || !strings.HasSuffix(string(raw), "\n") {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		t.Fatalf("bad helper pid %q", raw)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid, true
}

// The timeout starts when the helper starts, and on a loaded machine the helper
// can take longer than a short timeout to record its pid; it is then killed
// before there is anything to check, and the test would have checked nothing.
// So the timeout grows until the helper recorded its pid before it fired. Only
// a run that never gets that far is skipped, and says so: it is not a pass.
func TestHungHelperIsKilledAtTheTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{400 * time.Millisecond, 2 * time.Second, 10 * time.Second} {
		pidFile := filepath.Join(t.TempDir(), "pid")
		// A fresh backend each: a timeout takes a backend down for the run.
		b := backend(t, func(c *Config) { c.Timeout = timeout })
		start := time.Now()
		rd := one(t, b, "MODE:hang:"+pidFile)
		if rd.Reading != decide.Unavailable || rd.Detail != "timeout" {
			t.Fatalf("%+v", rd)
		}
		if d := time.Since(start); d > timeout+4*time.Second {
			t.Fatalf("took %v with a %v timeout", d, timeout)
		}
		pid, ok := recordedPID(t, pidFile)
		if !ok {
			continue // timed out before the helper recorded its pid: nothing to check yet
		}
		if pidAlive(pid) {
			t.Fatalf("helper %d is still running after the %v timeout", pid, timeout)
		}
		return
	}
	t.Skip("the helper never recorded its pid before even a 10s timeout fired: the machine is too loaded to check the kill")
}

// The cancel comes once the helper is running (it has recorded its pid), so the
// test always has a helper whose kill it checks, however slowly it started.
func TestContextCancelKillsTheHelperAndReturnsTheContextError(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := backend(t, func(c *Config) { c.Timeout = time.Minute }).Decide(ctx, question("MODE:hang:"+pidFile))
		done <- err
	}()
	var pid int
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if p, ok := recordedPID(t, pidFile); ok {
			pid = p
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Decide returned before the helper recorded its pid: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the helper never recorded its pid")
		}
	}
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Decide did not return after cancel")
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("returning after the cancel took %v", d)
	}
	if pidAlive(pid) {
		t.Fatalf("helper %d is still running", pid)
	}
}
