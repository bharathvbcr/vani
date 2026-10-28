//go:build unix

package rkbresume

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// hostileDir is an absolute directory that must not count as holding pdftotext
// until a test puts a real executable there. ToolDirs is an empty slice, not
// nil, so the default absolute tool directories are not consulted.
func hostileDir(t *testing.T) (string, *builder) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	return dir, &builder{cfg: Config{ToolDirs: []string{}}}
}

func requireMissing(t *testing.T, b *builder) {
	t.Helper()
	_, err := b.childEnv(true)
	if err == nil || !strings.Contains(err.Error(), "pdftotext") {
		t.Fatalf("pdftotext was accepted: %v", err)
	}
}

func TestHostilePdftotextPaths(t *testing.T) {
	parent := t.TempDir()
	rel := filepath.Join(parent, "relbin")
	if err := os.Mkdir(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	tool := []byte("#!/bin/sh\n")
	if err := os.WriteFile(filepath.Join(rel, "pdftotext"), tool, 0o755); err != nil {
		t.Fatal(err)
	}
	// A tool in the working directory must not be picked up from an empty,
	// relative, or dot PATH entry.
	if err := os.WriteFile(filepath.Join(parent, "pdftotext"), tool, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(parent)
	b := &builder{cfg: Config{ToolDirs: []string{}}}
	for _, path := range []string{"", "relbin", ".", ":", ":relbin:", " "} {
		t.Setenv("PATH", path)
		requireMissing(t, b)
	}
	if _, err := b.childEnv(false); err != nil {
		t.Fatalf("a build that does not fit does not need pdftotext: %v", err)
	}

	abs := filepath.Join(parent, "abs")
	if err := os.Mkdir(abs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(abs, "pdftotext.exe.bak"), tool, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(abs, "pdftotext.exe"), tool, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", abs)
	requireMissing(t, b)
	if isExecutable(filepath.Join(abs, "pdftotext.exe")) {
		t.Fatal("mode 0644 pdftotext.exe was accepted on unix")
	}
	asDir := filepath.Join(parent, "asdir")
	if err := os.MkdirAll(filepath.Join(asDir, "pdftotext"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(asDir, "pdftotext.exe"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", asDir)
	requireMissing(t, b)
	if isExecutable(filepath.Join(asDir, "pdftotext")) || isExecutable(filepath.Join(asDir, "pdftotext.exe")) {
		t.Fatal("a directory named pdftotext or pdftotext.exe was accepted")
	}

	uni := filepath.Join(t.TempDir(), "工具目录")
	if err := os.Mkdir(uni, 0o755); err != nil {
		t.Fatal(err)
	}
	uniTool := filepath.Join(uni, "pdftotext")
	if err := os.WriteFile(uniTool, tool, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", uni)
	if _, err := b.childEnv(true); err != nil {
		t.Fatalf("unicode directory: %v", err)
	}
	if !isExecutable(uniTool) {
		t.Fatal("unicode path was not executable")
	}
}

func TestPdftotextSymlinksFollowTheTargetMode(t *testing.T) {
	dir, b := hostileDir(t)
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	execFile := filepath.Join(dir, "exec")
	if err := os.WriteFile(execFile, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "pdftotext")
	if err := os.Symlink(plain, link); err != nil {
		t.Fatal(err)
	}
	if isExecutable(link) {
		t.Fatal("symlink to a mode 0644 file was accepted")
	}
	requireMissing(t, b)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(execFile, link); err != nil {
		t.Fatal(err)
	}
	if !isExecutable(link) {
		t.Fatal("symlink to a mode 0755 file was not accepted")
	}
	if _, err := b.childEnv(true); err != nil {
		t.Fatalf("symlink to an executable: %v", err)
	}

	cycle := filepath.Join(dir, "cycle")
	if err := os.Symlink(cycle, cycle); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if isExecutable(cycle) {
		t.Fatal("a symlink cycle was accepted")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("symlink cycle took %s", time.Since(start))
	}
}

func TestPdftotextFifoIsNotExecutableAndDoesNotBlock(t *testing.T) {
	dir, b := hostileDir(t)
	fifo := filepath.Join(dir, "pdftotext")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() {
		done <- isExecutable(fifo)
	}()
	select {
	case got := <-done:
		if got {
			t.Fatal("a FIFO named pdftotext was accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stat on a FIFO blocked")
	}
	requireMissing(t, b)

	pipeLink := filepath.Join(dir, "via")
	if err := os.Symlink(fifo, pipeLink); err != nil {
		t.Fatal(err)
	}
	if isExecutable(pipeLink) {
		t.Fatal("a symlink to a FIFO was accepted")
	}
}
