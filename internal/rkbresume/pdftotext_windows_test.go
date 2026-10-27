//go:build windows

package rkbresume

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsPdftotextExeIsFoundWithoutTheExecuteBit(t *testing.T) {
	tools := t.TempDir()
	exe := filepath.Join(tools, "pdftotext.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isExecutable(exe) {
		t.Fatal("mode 0644 pdftotext.exe was not accepted")
	}
	hold := t.TempDir()
	if err := os.Mkdir(filepath.Join(hold, "pdftotext.exe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if isExecutable(filepath.Join(hold, "pdftotext.exe")) {
		t.Fatal("a directory named pdftotext.exe was accepted")
	}

	t.Setenv("PATH", tools)
	b := &builder{cfg: Config{ToolDirs: []string{}}}
	if _, err := b.childEnv(true); err != nil {
		t.Fatalf("pdftotext.exe on PATH: %v", err)
	}

	empty := t.TempDir()
	t.Setenv("PATH", empty)
	b.cfg.ToolDirs = []string{filepath.Join(t.TempDir(), "nowhere"), tools}
	env, err := b.childEnv(true)
	if err != nil {
		t.Fatalf("pdftotext.exe in a tool dir: %v", err)
	}
	path := ""
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "PATH="); ok {
			path = v
		}
	}
	if !strings.HasPrefix(path, empty) || !strings.Contains(path, tools) {
		t.Fatalf("PATH = %q, want %s then %s", path, empty, tools)
	}

	bare := t.TempDir()
	if err := os.WriteFile(filepath.Join(bare, "pdftotext"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bare)
	b.cfg.ToolDirs = []string{}
	if _, err := b.childEnv(true); err == nil || !strings.Contains(err.Error(), "pdftotext") {
		t.Fatalf("bare pdftotext without the execute bit: %v", err)
	}

	t.Setenv("PATH", empty)
	if _, err := b.childEnv(true); err == nil || !strings.Contains(err.Error(), "pdftotext") {
		t.Fatalf("missing tool: %v", err)
	}
	if _, err := b.childEnv(false); err != nil {
		t.Fatalf("a build that does not fit does not need pdftotext: %v", err)
	}

	// A name that merely has pdftotext.exe as a prefix is not the tool, and a
	// relative or empty PATH must not be searched as the working directory.
	pref := t.TempDir()
	if err := os.WriteFile(filepath.Join(pref, "pdftotext.exe.bak"), []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pref, "PDFTOTEXT.EXE"), []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isExecutable(filepath.Join(pref, "pdftotext.exe")) {
		t.Fatal("mode 0644 PDFTOTEXT.EXE was not accepted")
	}
	t.Setenv("PATH", "")
	b.cfg.ToolDirs = []string{}
	if _, err := b.childEnv(true); err == nil || !strings.Contains(err.Error(), "pdftotext") {
		t.Fatalf("empty PATH: %v", err)
	}
	t.Setenv("PATH", "relbin")
	if _, err := b.childEnv(true); err == nil || !strings.Contains(err.Error(), "pdftotext") {
		t.Fatalf("relative PATH: %v", err)
	}
	t.Setenv("PATH", pref)
	if _, err := b.childEnv(true); err != nil {
		t.Fatalf("PDFTOTEXT.EXE on PATH: %v", err)
	}
}
