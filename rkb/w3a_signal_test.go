//go:build darwin || linux || freebsd || netbsd || openbsd

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// w3a: the signal handler's exit codes, and that a second signal during a
// stuck cleanup exits at once instead of waiting for it.

func w3aSignalChild(t *testing.T, mode string) (*exec.Cmd, string) {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestW3ASignalChildMain$")
	cmd.Env = append(os.Environ(), "W3A_SIGCHILD="+mode, "W3A_READY="+ready)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(ready); err == nil {
			return cmd, ready
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	t.Fatal("child never became ready")
	return nil, ""
}

// TestW3ASignalChildMain is the child body for the tests below; run on its own
// it does nothing.
func TestW3ASignalChildMain(t *testing.T) {
	mode := os.Getenv("W3A_SIGCHILD")
	if mode == "" {
		t.Skip("child body")
	}
	_, _, err := scratchDir("rkb-w3a-")
	if err != nil {
		t.Fatal(err)
	}
	if mode == "stuck" {
		exitMu.Lock()
		exitCleanups = append(exitCleanups, func() { time.Sleep(time.Minute) }) // runs first (reverse order)
		exitMu.Unlock()
	}
	_ = os.WriteFile(os.Getenv("W3A_READY"), nil, 0o600)
	time.Sleep(time.Minute)
}

func w3aExitCode(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	err := cmd.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return 0
}

func TestW3ASignalExitCodes(t *testing.T) {
	for sig, want := range map[syscall.Signal]int{syscall.SIGINT: 130, syscall.SIGTERM: 143} {
		cmd, _ := w3aSignalChild(t, "plain")
		_ = cmd.Process.Signal(sig)
		if got := w3aExitCode(t, cmd); got != want {
			t.Errorf("%v: exit code %d, want %d", sig, got, want)
		}
	}
}

func TestW3ASecondSignalDoesNotWaitForCleanup(t *testing.T) {
	cmd, _ := w3aSignalChild(t, "stuck")
	_ = cmd.Process.Signal(syscall.SIGINT)
	time.Sleep(300 * time.Millisecond) // the first is now inside the stuck cleanup
	st := time.Now()
	_ = cmd.Process.Signal(syscall.SIGINT)
	got := w3aExitCode(t, cmd)
	if d := time.Since(st); d > 5*time.Second {
		t.Errorf("second signal took %v to end the process", d)
	}
	if got != 130 {
		t.Errorf("exit code %d, want 130", got)
	}
}
