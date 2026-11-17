//go:build darwin || linux || freebsd || netbsd || openbsd

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A hanging helper: a script that starts a grandchild sleeper, records its pid
// and waits on it. rkb must give up within the bound, report why, and kill the
// whole process group (the grandchild too, as a Chrome helper would be).

const w3aBound = 400 * time.Millisecond

// w3aFakeTool installs a hanging stand-in for name on PATH and returns the file
// its grandchild's pid lands in.
func w3aFakeTool(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	script := "#!/bin/sh\nsleep 300 &\necho $! > '" + pidFile + "'\nwait\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() { // the sleeper is ours: make sure a failing test leaves nothing running
		if pid := w3aPid(pidFile); pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return pidFile
}

func w3aPid(pidFile string) int {
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

// w3aWithin runs f and fails if it does not return inside limit.
func w3aWithin(t *testing.T, limit time.Duration, f func()) time.Duration {
	t.Helper()
	done := make(chan struct{})
	start := time.Now()
	go func() { defer close(done); f() }()
	select {
	case <-done:
		return time.Since(start)
	case <-time.After(limit):
		t.Fatalf("the call was still running after %s: its subprocess has no bound", limit)
		return 0
	}
}

func w3aGone(t *testing.T, pidFile string) {
	t.Helper()
	pid := w3aPid(pidFile)
	if pid == 0 {
		t.Fatal("the fake tool never recorded its child")
	}
	for i := 0; i < 50; i++ {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("the grandchild (pid %d) survived the timeout: the process group was not killed", pid)
}

func w3aSetBound(t *testing.T, d *time.Duration) {
	t.Helper()
	old := *d
	*d = w3aBound
	t.Cleanup(func() { *d = old })
}

func TestW3APdftotextBBoxIsBounded(t *testing.T) {
	w3aSetBound(t, &timeoutPoppler)
	pidFile := w3aFakeTool(t, "pdftotext")
	var err error
	w3aWithin(t, 8*time.Second, func() { _, _, err = pdfWords("x.pdf") })
	if err == nil || !strings.Contains(err.Error(), "did not finish within") {
		t.Errorf("a hung pdftotext must come back as a timeout error, got %v", err)
	}
	w3aGone(t, pidFile)
}

func TestW3AQuotePdftotextIsBounded(t *testing.T) {
	w3aSetBound(t, &timeoutPoppler)
	pidFile := w3aFakeTool(t, "pdftotext")
	pdf := filepath.Join(t.TempDir(), "src.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok := true
	w3aWithin(t, 8*time.Second, func() { _, ok = fileText(pdf) })
	if ok {
		t.Error("a pdf whose text could not be extracted must not read as extracted")
	}
	w3aGone(t, pidFile)
}

func TestW3ADocxPageCountIsBounded(t *testing.T) {
	w3aSetBound(t, &timeoutOffice)
	pidFile := w3aFakeTool(t, "soffice")
	got := 0
	w3aWithin(t, 8*time.Second, func() { got = docxPages(filepath.Join(t.TempDir(), "r.docx")) })
	if got != -1 {
		t.Errorf("a hung office converter must read as 'page count unknown' (-1), got %d", got)
	}
	w3aGone(t, pidFile)
}

func TestW3ARunCmdOutcomes(t *testing.T) {
	out, err := runCmd(5*time.Second, runOpts{}, "sh", "-c", "echo out; echo err >&2")
	if err != nil || strings.TrimSpace(string(out)) != "out" {
		t.Errorf("stdout only: %q %v", out, err)
	}
	out, err = runCmd(5*time.Second, runOpts{Combined: true}, "sh", "-c", "echo out; echo err >&2; exit 3")
	if err == nil || !strings.Contains(string(out), "err") || !strings.Contains(string(out), "out") {
		t.Errorf("a failing command must return its error and both streams: %q %v", out, err)
	}
	if _, err := runCmd(5*time.Second, runOpts{}, "rkb-no-such-tool-w3a"); err == nil {
		t.Error("a missing tool must be an error")
	}
	out, err = runCmd(5*time.Second, runOpts{Dir: t.TempDir(), Env: []string{"W3A=1"}}, "sh", "-c", "pwd; echo $W3A")
	if err != nil || !strings.Contains(string(out), "1") {
		t.Errorf("Dir and Env must apply: %q %v", out, err)
	}
	// a grandchild holding the output pipe open must not stall Run past the bound plus the grace
	d := w3aWithin(t, 8*time.Second, func() {
		_, err = runCmd(300*time.Millisecond, runOpts{Combined: true}, "sh", "-c", "sleep 30 & wait")
	})
	if err == nil || d > 5*time.Second {
		t.Errorf("a pipe-holding child must still be cut off promptly: %v after %s", err, d)
	}
}

func TestW3AChromeIsBounded(t *testing.T) {
	w3aSetBound(t, &timeoutChrome)
	pidFile := w3aFakeTool(t, "chrome-w3a")
	t.Setenv("RKB_CHROME", filepath.Join(filepath.Dir(pidFile), "chrome-w3a"))
	var err error
	w3aWithin(t, 8*time.Second, func() { _, err = runChrome("--version") })
	if err == nil || !strings.Contains(err.Error(), "did not finish within") {
		t.Errorf("a hung chrome must come back as a timeout error, got %v", err)
	}
	w3aGone(t, pidFile)
}
