//go:build darwin || linux || freebsd || netbsd || openbsd

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// w3aadv: adversarial tests for slice w3a (config validation, runCmd,
// scratch cleanup, claims hash). Every test here fails against the w3a fix (a real break); the
// attacks the fix withstands are not kept.

func w3aadvAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func w3aadvReap(t *testing.T, pidFile string) {
	t.Cleanup(func() {
		if pid := w3aPid(pidFile); pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
}

// A tool that succeeds but leaves a background helper holding its stdout (a
// headless browser does this) must not turn a success into an error, and must
// not leave the helper running.
func TestW3AAdvRunCmdSuccessWithLingeringHelper(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "helper.pid")
	w3aadvReap(t, pidFile)
	st := time.Now()
	out, err := runCmd(30*time.Second, runOpts{}, "sh", "-c", "sleep 300 & echo $! > '"+pidFile+"'; echo done")
	d := time.Since(st)
	if err != nil {
		t.Errorf("exit 0 with a lingering helper returned error after %v: %v", d, err)
	}
	if !bytes.Contains(out, []byte("done")) {
		t.Errorf("output lost: %q", out)
	}
	if pid := w3aPid(pidFile); pid > 0 && w3aadvAlive(pid) {
		t.Errorf("helper %d outlived a finished command (orphaned in its own process group)", pid)
	}
}

// rkb (the parent) killed by SIGINT/SIGTERM mid-command: the child lives in its
// own process group, so the terminal's Ctrl-C no longer reaches it. It must not
// be left running.
func TestW3AAdvParentSignalDoesNotOrphanChild(t *testing.T) {
	if os.Getenv("W3AADV_PARENT") == "1" {
		_, _ = runCmd(time.Minute, runOpts{}, "sh", "-c", "sleep 300 & echo $! > \""+os.Getenv("W3AADV_PID")+"\"; wait")
		return
	}
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	w3aadvReap(t, pidFile)
	cmd := exec.Command(os.Args[0], "-test.run=^TestW3AAdvParentSignalDoesNotOrphanChild$")
	cmd.Env = append(os.Environ(), "W3AADV_PARENT=1", "W3AADV_PID="+pidFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // stand-in for the terminal's foreground group
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var pid int
	for i := 0; i < 100 && pid == 0; i++ {
		time.Sleep(100 * time.Millisecond)
		pid = w3aPid(pidFile)
	}
	if pid == 0 {
		_ = cmd.Process.Kill()
		t.Fatal("helper never started")
	}
	// what Ctrl-C does: SIGINT to the foreground group, i.e. the parent's group only
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
	_ = cmd.Wait()
	time.Sleep(300 * time.Millisecond)
	if w3aadvAlive(pid) {
		t.Errorf("child %d survived its parent's SIGINT (no signal handler forwards or kills the child's group)", pid)
	}
}

// rkb killed by Ctrl-C or SIGTERM mid-finalize: the staging folder holds the
// person's resume text and contact lines and is only removed by exitNow, the
// deferred cleanup and a panic, none of which a default-handled signal runs.
func TestW3AAdvSignalRemovesStagingFolder(t *testing.T) {
	if os.Getenv("W3AADV_STAGE") == "1" {
		dir, cleanup, err := scratchDir("rkb-finalize-")
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		_ = os.WriteFile(filepath.Join(dir, "Resume.txt"), []byte("x\n"), 0o600)
		_ = os.WriteFile(os.Getenv("W3AADV_DIRFILE"), []byte(dir), 0o600)
		time.Sleep(time.Minute)
		return
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		dirFile := filepath.Join(t.TempDir(), "dir")
		cmd := exec.Command(os.Args[0], "-test.run=^TestW3AAdvSignalRemovesStagingFolder$")
		cmd.Env = append(os.Environ(), "W3AADV_STAGE=1", "W3AADV_DIRFILE="+dirFile)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		var dir string
		for i := 0; i < 100 && dir == ""; i++ {
			time.Sleep(100 * time.Millisecond)
			if b, err := os.ReadFile(dirFile); err == nil {
				dir = string(b)
			}
		}
		if dir == "" {
			_ = cmd.Process.Kill()
			t.Fatal("staging folder never created")
		}
		_ = cmd.Process.Signal(sig)
		_ = cmd.Wait()
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("%v left the staging folder %s behind", sig, dir)
			_ = os.RemoveAll(dir)
		}
	}
}
