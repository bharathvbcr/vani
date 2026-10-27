package rkbresume

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestModeExecutableRules(t *testing.T) {
	if !modeExecutable("windows", "pdftotext.exe", 0o644) {
		t.Fatal("windows pdftotext.exe mode 0644 was not accepted")
	}
	if !modeExecutable("windows", "PDFTOTEXT.EXE", 0o644) {
		t.Fatal("windows .EXE was not accepted")
	}
	if modeExecutable("windows", "pdftotext", 0o644) {
		t.Fatal("windows bare pdftotext without the execute bit was accepted")
	}
	if !modeExecutable("windows", "pdftotext", 0o755) {
		t.Fatal("windows bare pdftotext with the execute bit was not accepted")
	}
	if modeExecutable("darwin", "pdftotext", 0o644) || modeExecutable("linux", "pdftotext.exe", 0o644) {
		t.Fatal("unix accepted a file without the execute bit")
	}
	if !modeExecutable("darwin", "pdftotext", 0o755) || !modeExecutable("linux", "pdftotext", 0o755) {
		t.Fatal("unix mode 0755 was not accepted")
	}
	win := pdfToolNames("windows")
	if len(win) != 2 || win[0] != "pdftotext.exe" || win[1] != "pdftotext" {
		t.Fatalf("windows names = %v", win)
	}
	if got := pdfToolNames("darwin"); len(got) != 1 || got[0] != "pdftotext" {
		t.Fatalf("darwin names = %v", got)
	}
	if got := pdfToolNames("linux"); len(got) != 1 || got[0] != "pdftotext" {
		t.Fatalf("linux names = %v", got)
	}
}

func TestPdftotextUnixExecuteBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix execute bit is not what makes a Windows file runnable")
	}
	dir := t.TempDir()
	plain := filepath.Join(dir, "pdftotext")
	if err := os.WriteFile(plain, []byte("not a tool"), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "pdftotext.exe")
	if err := os.WriteFile(exe, []byte("not a tool"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if isExecutable(plain) {
		t.Fatal("mode 0644 pdftotext was accepted")
	}
	if isExecutable(exe) {
		t.Fatal("mode 0644 pdftotext.exe was accepted on unix")
	}
	if isExecutable(filepath.Join(dir, "subdir")) || isExecutable(filepath.Join(dir, "missing")) {
		t.Fatal("a directory or a missing path was accepted")
	}
	t.Setenv("PATH", dir)
	b := &builder{cfg: Config{ToolDirs: []string{}}}
	if _, err := b.childEnv(true); err == nil || !strings.Contains(err.Error(), "pdftotext") {
		t.Fatalf("missing executable pdftotext: %v", err)
	}
	if _, err := (&builder{}).childEnv(false); err != nil {
		t.Fatalf("a build that does not fit does not need pdftotext: %v", err)
	}
	if err := os.Chmod(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if !isExecutable(plain) {
		t.Fatal("mode 0755 pdftotext was not accepted")
	}
	if _, err := b.childEnv(true); err != nil {
		t.Fatal(err)
	}
}
