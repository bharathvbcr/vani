package manualtriage

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

func TestReadMissingFileIsEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "retriage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m, err := Read(dir)
	if err != nil || len(m) != 0 {
		t.Fatalf("Read = %v, %v; want an empty map", m, err)
	}
	if _, err := os.Lstat(file(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Read created the store: %v", err)
	}
}

func TestReadRefusesSymlinkedReadings(t *testing.T) {
	real := filepath.Join(t.TempDir(), "retriage")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Set(real, reading("gh:acme:1", t0)); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.Rename(file(real), target); err != nil {
		t.Fatal(err)
	}
	kept, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "retriage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, file(dir)); err != nil {
		t.Fatal(err)
	}
	m, err := Read(dir)
	if !errors.Is(err, atomicfile.ErrLinkedLeaf) {
		t.Fatalf("Read = %v, %v; want ErrLinkedLeaf", m, err)
	}
	if len(m) != 0 {
		t.Fatalf("Read returned the target's readings: %v", m)
	}
	if b, rerr := os.ReadFile(target); rerr != nil || string(b) != string(kept) {
		t.Fatalf("target changed: %q %v", b, rerr)
	}
	st, lerr := os.Lstat(file(dir))
	if lerr != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("leaf = %v %v, want a symlink", st, lerr)
	}
}

// A symlink planted after compaction and before reopen is not written through.
func TestReopenAfterCompactRefusesSwappedSymlink(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "retriage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for i := 0; buf.Len() < MaxFileBytes-200; i++ {
		buf.Write(lineOf(t, event{V: Version, At: t0.Add(time.Duration(i) * time.Second), Key: "gh:acme:1", Op: opSet, Slots: goodSlots(), Model: "claude-sonnet-5-5", Text: hash('a'), Prompt: hash('b')}))
	}
	if err := os.WriteFile(file(dir), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.jsonl")
	const keep = "keep-target\n"
	if err := os.WriteFile(target, []byte(keep), 0o600); err != nil {
		t.Fatal(err)
	}
	old := afterCompact
	t.Cleanup(func() { afterCompact = old })
	afterCompact = func(p string) {
		if err := os.Remove(p); err != nil {
			t.Errorf("remove compacted file: %v", err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Errorf("symlink: %v", err)
		}
	}
	err := Set(dir, reading("gh:acme:2", t0.Add(time.Hour)))
	if !errors.Is(err, atomicfile.ErrLinkedLeaf) {
		t.Fatalf("Set = %v, want ErrLinkedLeaf", err)
	}
	if b, rerr := os.ReadFile(target); rerr != nil || string(b) != keep {
		t.Fatalf("target = %q, %v", b, rerr)
	}
	st, lerr := os.Lstat(file(dir))
	if lerr != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("leaf = %v %v, want a symlink", st, lerr)
	}
}
