//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Two regular files can share a size and an mtime. The file index from
// GetFileInformationByHandle, not the stat, is what tells them apart. A
// missing path is an error, not an identity.
func TestWindowsSameSizeSameMtimeHasDistinctFileIndex(t *testing.T) {
	dir := t.TempDir()
	body := []byte("same-size-image")
	mtime := time.Unix(1_700_000_000, 0)
	write := func(name string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, body, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return path
	}
	a, err := statExecutable(write("a.exe"))
	if err != nil {
		t.Fatalf("stat a: %v", err)
	}
	b, err := statExecutable(write("b.exe"))
	if err != nil {
		t.Fatalf("stat b: %v", err)
	}
	if a.size != b.size || a.mtime != b.mtime {
		t.Fatalf("size or mtime differ: %+v vs %+v", a, b)
	}
	if a.ino == b.ino {
		t.Fatalf("same size and mtime but one file index: %+v vs %+v", a, b)
	}
	missing := filepath.Join(dir, "missing.exe")
	if _, err := statExecutable(missing); err == nil {
		t.Fatal("missing path returned an identity")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing path: %v", err)
	}
}
