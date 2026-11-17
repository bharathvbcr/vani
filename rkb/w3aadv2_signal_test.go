//go:build darwin || linux || freebsd || netbsd || openbsd

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// w3aadv2: a signal that interrupts a running command must end the process
// with 130/143 whatever the caller does with runCmd's error. The caller sees
// the killed command return and calls die()/stop (exit 1 or 2) on the main
// goroutine, concurrently with the watcher goroutine's exit 130.

// TestW3AAdv2RunChildMain is the child body: it runs a long command through
// runCmd and, like every rkb caller, dies on the error.
func TestW3AAdv2RunChildMain(t *testing.T) {
	ready := os.Getenv("W3AADV2_READY")
	if ready == "" {
		t.Skip("child body")
	}
	go func() {
		time.Sleep(300 * time.Millisecond) // runCmd is in Wait by now
		_ = os.WriteFile(ready, nil, 0o600)
	}()
	_, err := runCmd(time.Minute, runOpts{}, "sleep", "60")
	if err != nil {
		die("command failed: %v", err)
	}
}

func TestW3AAdv2SignalDuringRunCmdExitsWithSignalCode(t *testing.T) {
	for sig, want := range map[syscall.Signal]int{syscall.SIGINT: 130, syscall.SIGTERM: 143} {
		for i := 0; i < 10; i++ {
			ready := filepath.Join(t.TempDir(), "ready")
			cmd := exec.Command(os.Args[0], "-test.run=^TestW3AAdv2RunChildMain$")
			cmd.Env = append(os.Environ(), "W3AADV2_READY="+ready)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			for j := 0; j < 100; j++ {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			_ = cmd.Process.Signal(sig)
			if got := w3aExitCode(t, cmd); got != want {
				t.Errorf("%v while runCmd was running (run %d): exit code %d, want %d", sig, i, got, want)
			}
		}
	}
}
