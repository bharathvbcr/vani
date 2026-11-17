//go:build unix

package rkbresume

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A planted FIFO at a key's path must not hang the build (os.Open on a FIFO with
// no writer blocks forever).
func TestW3dadvFIFOAtKeyPathDoesNotHang(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "choices")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key := sha256Hex([]byte("fifo"))
	p := filepath.Join(dir, key+".json")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skip("no mkfifo:", err)
	}
	done := make(chan bool, 1)
	go func() {
		_, ok := (choiceKeep{dir: dir}).load(key, time.Now())
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Error("a FIFO was reused as a kept choice")
		}
	case <-time.After(3 * time.Second):
		if rw, err := os.OpenFile(p, os.O_RDWR, 0); err == nil { // release the blocked reader
			rw.Close()
		}
		t.Fatal("load blocked on a FIFO planted at the key path")
	}
}

// A symlink at the key path must not be followed to a file outside the folder.
func TestW3dadvSymlinkAtKeyPathIsNotFollowed(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "choices")
	outside := filepath.Join(root, "outside.json")
	k := choiceKeep{dir: filepath.Join(root, "other")}
	key := sha256Hex([]byte("link"))
	k.save(key, "planted reply", time.Now())
	src, _ := k.path(key)
	if err := os.Rename(src, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, key+".json")); err != nil {
		t.Fatal(err)
	}
	if text, ok := (choiceKeep{dir: dir}).load(key, time.Now()); ok {
		t.Errorf("load followed a symlink out of the folder and returned %q", text)
	}
}
