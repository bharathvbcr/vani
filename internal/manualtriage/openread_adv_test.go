package manualtriage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

func TestReadMissingCreatesNothing(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "retriage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m, err := Read(dir)
	if err != nil || len(m) != 0 {
		t.Fatalf("Read = %v, %v; want an empty map", m, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("Read created %d entries", len(entries))
	}

	missingDir := filepath.Join(parent, "absent")
	m, err = Read(missingDir)
	if err != nil || len(m) != 0 {
		t.Fatalf("missing dir Read = %v, %v", m, err)
	}
	if _, err := os.Lstat(missingDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Read created the directory: %v", err)
	}
}

func TestReadDanglingAndLoopAreNotEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "retriage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("not-written", file(dir)); err != nil {
		t.Fatal(err)
	}
	m, err := Read(dir)
	if !errors.Is(err, atomicfile.ErrLinkedLeaf) {
		t.Fatalf("dangling Read = %v, %v; want ErrLinkedLeaf", m, err)
	}
	if len(m) != 0 {
		t.Fatalf("dangling Read returned %v", m)
	}
	if _, err := os.Lstat(filepath.Join(dir, "not-written")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dangling read created a file: %v", err)
	}
	if st, err := os.Lstat(file(dir)); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("leaf = %v %v", st, err)
	}

	if err := os.Remove(file(dir)); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other")
	if err := os.Symlink("other", file(dir)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fileName, other); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	m, err = Read(dir)
	if time.Since(start) > 800*time.Millisecond {
		t.Fatal("Read hung on a symlink loop")
	}
	if !errors.Is(err, atomicfile.ErrLinkedLeaf) || len(m) != 0 {
		t.Fatalf("loop Read = %v, %v", m, err)
	}
}

func TestReadThroughSymlinkedDir(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "状态", "retriage")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Set(real, reading("gh:acme:1", t0)); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	m, err := Read(link)
	if err != nil || m["gh:acme:1"].Key == "" {
		t.Fatalf("Read via dir symlink = %v %v", m, err)
	}

	empty := filepath.Join(root, "empty-real")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	emptyLink := filepath.Join(root, "empty-link")
	if err := os.Symlink(empty, emptyLink); err != nil {
		t.Fatal(err)
	}
	m, err = Read(emptyLink)
	if err != nil || len(m) != 0 {
		t.Fatalf("missing via dir symlink = %v %v", m, err)
	}
	if _, err := os.Lstat(file(empty)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Read created the store through the dir symlink: %v", err)
	}
}
