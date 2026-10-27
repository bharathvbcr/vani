//go:build windows

package rkbexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReapGroupNilProcessDoesNothing(t *testing.T) {
	reapGroup(nil)
	reapGroup(&exec.Cmd{})
}

func TestCancelStopsTheDescendantNotOnlyTheDirectProcess(t *testing.T) {
	switch os.Getenv("VANI_RKB_ROLE") {
	case "child":
		runTreeChild()
		return
	case "grand":
		runTreeGrand()
		return
	}

	pidFile := filepath.Join(t.TempDir(), "grand.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := ExecRunner{}.Run(ctx, Command{
			Path: os.Args[0],
			Args: []string{"-test.run=^TestCancelStopsTheDescendantNotOnlyTheDirectProcess$", "-test.count=1"},
			Env:  treeEnv("child", pidFile),
		}, time.Minute)
		done <- err
	}()
	pid := waitGrandPID(t, pidFile)
	t.Cleanup(func() { _ = terminatePID(pid) })
	cancel()
	start := time.Now()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	alive, err := processStillActive(pid)
	if err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Fatalf("descendant %d survived cancel", pid)
	}
	if d := time.Since(start); d > waitDelay+groupGrace+3*time.Second {
		t.Fatalf("stopping the tree took %s, over its grace", d)
	}
}

func runTreeChild() {
	grand := exec.Command(os.Args[0], "-test.run=^TestCancelStopsTheDescendantNotOnlyTheDirectProcess$", "-test.count=1")
	grand.Env = treeEnv("grand", os.Getenv("VANI_PIDFILE"))
	if err := grand.Start(); err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
	time.Sleep(10 * time.Minute)
}

func runTreeGrand() {
	pid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(os.Getenv("VANI_PIDFILE"), []byte(pid+"\n"), 0o644); err != nil {
		os.Exit(1)
	}
	time.Sleep(10 * time.Minute)
}

func treeEnv(role, pidFile string) []string {
	env := []string{"VANI_RKB_ROLE=" + role, "VANI_PIDFILE=" + pidFile}
	for _, k := range []string{"SystemRoot", "PATH", "TEMP", "TMP", "USERPROFILE", "WINDIR", "HOMEDRIVE", "HOMEPATH"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func waitGrandPID(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(pidFile)
		if err == nil && strings.HasSuffix(string(raw), "\n") {
			pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil || pid <= 4 {
				t.Fatalf("bad descendant pid %q", raw)
			}
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("descendant pid was not recorded")
	return 0
}

func processStillActive(pid int) (bool, error) {
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, syscall.Errno(87)) || errors.Is(err, syscall.Errno(6)) {
			return false, nil
		}
		return false, err
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false, err
	}
	return code == stillActive, nil
}

func terminatePID(pid int) error {
	if pid <= 4 {
		return nil
	}
	h, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, syscall.Errno(87)) || errors.Is(err, syscall.Errno(6)) {
			return nil
		}
		return err
	}
	defer syscall.CloseHandle(h)
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("TerminateProcess")
	r, _, err := proc.Call(uintptr(h), 1)
	if r == 0 && !errors.Is(err, syscall.Errno(6)) && !errors.Is(err, syscall.Errno(87)) {
		if err == nil || err == syscall.Errno(0) {
			return syscall.EINVAL
		}
		return err
	}
	return nil
}
