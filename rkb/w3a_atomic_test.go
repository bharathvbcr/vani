//go:build darwin || linux || freebsd || netbsd || openbsd

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A directory that refuses new files must make writeAtomic fail naming the
// path, not truncate the existing target in place.
func TestW3AWriteAtomicNoFallbackWhenTempCannotBeCreated(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "doc.json")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o755) }()
	err := writeAtomic(p, []byte("new"))
	if err == nil {
		t.Fatal("writeAtomic succeeded although no temp file could be created (direct-write fallback)")
	}
	if !strings.Contains(err.Error(), p) {
		t.Errorf("error does not name the path: %v", err)
	}
	if got, _ := os.ReadFile(p); string(got) != "old" {
		t.Errorf("target was modified: %q", got)
	}
}

// The temp file is registered with the exit cleanups only while it exists.
func TestW3AWriteAtomicLeavesNoCleanupBehind(t *testing.T) {
	exitMu.Lock()
	before := len(exitCleanups)
	exitMu.Unlock()
	p := filepath.Join(t.TempDir(), "a", "doc.txt")
	if err := writeAtomic(p, []byte("x")); err != nil {
		t.Fatal(err)
	}
	exitMu.Lock()
	after := len(exitCleanups)
	exitMu.Unlock()
	if after != before {
		t.Errorf("exit cleanups grew from %d to %d after a finished write", before, after)
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".*rkb-tmp-*"))
	if len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
}

func TestW3AAddExitCleanupWithdraw(t *testing.T) {
	ran := 0
	exitMu.Lock()
	saved := exitCleanups
	exitCleanups = nil
	exitMu.Unlock()
	defer func() { exitMu.Lock(); exitCleanups = saved; exitMu.Unlock() }()

	rmA := addExitCleanup(func() { ran++ })
	rmB := addExitCleanup(func() { ran += 10 })
	rmA() // not last: neutralised, B's index must stay valid
	rmB()
	exitMu.Lock()
	fns := exitCleanups
	exitMu.Unlock()
	for _, f := range fns {
		f()
	}
	if ran != 0 {
		t.Errorf("a withdrawn cleanup ran (ran=%d)", ran)
	}
}
