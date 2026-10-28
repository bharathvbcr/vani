//go:build !windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Unix identity is the device and inode os.Stat already returned. statFileID
// does not open the file: a FIFO with no writer would block an open, and this
// check has to come back within a second with replaced false.
func TestAdvUnixIdentityIsStatDevIno(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vani")
	if err := os.WriteFile(path, []byte("image"), 0o755); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("os.Stat Sys type %T, want *syscall.Stat_t", fi.Sys())
	}
	dev, ino, err := statFileID(path, fi)
	if err != nil {
		t.Fatal(err)
	}
	if dev != uint64(st.Dev) || ino != uint64(st.Ino) {
		t.Fatalf("statFileID dev %#x ino %#x, os.Stat dev %#x ino %#x", dev, ino, uint64(st.Dev), uint64(st.Ino))
	}
	oldPath := executablePath
	t.Cleanup(func() { executablePath = oldPath })
	executablePath = func() (string, error) { return path, nil }
	w := watchOwnBinary(&bytes.Buffer{})
	if w == nil {
		t.Fatal("no watch for a regular file")
	}
	var quiet bytes.Buffer
	if w.replaced(&quiet) || quiet.Len() != 0 {
		t.Fatalf("untouched file: replaced, stderr %q", quiet.String())
	}
}

// A FIFO is not a regular file, so the check errors before any open. Opening
// it for read would block with no writer. replaced stays false and quiet, and
// no reader is left on the pipe.
func TestAdvFIFOIsNotAReplacementAndDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vani")
	if err := os.WriteFile(path, []byte("image"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := executablePath
	t.Cleanup(func() { executablePath = oldPath })
	executablePath = func() (string, error) { return path, nil }
	w := watchOwnBinary(&bytes.Buffer{})
	if w == nil {
		t.Fatal("no watch for a regular file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("mode %v, want a FIFO", fi.Mode())
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("os.Lstat Sys type %T, want *syscall.Stat_t", fi.Sys())
	}

	type result struct {
		idErr          error
		dev, ino       uint64
		statErr        error
		replaced       bool
		replacedStderr string
		watch          *binWatch
		watchStderr    string
	}
	done := make(chan result, 1)
	go func() {
		var r result
		r.dev, r.ino, r.idErr = statFileID(path, fi)
		_, r.statErr = statExecutable(path)
		var replacedBuf bytes.Buffer
		r.replaced = w.replaced(&replacedBuf)
		r.replacedStderr = replacedBuf.String()
		var watchBuf bytes.Buffer
		r.watch = watchOwnBinary(&watchBuf)
		r.watchStderr = watchBuf.String()
		done <- r
	}()

	var r result
	select {
	case r = <-done:
	case <-time.After(time.Second):
		// A blocked read-open unblocks once a writer arrives, so the goroutine
		// can finish. The failure stands either way.
		if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
			_ = f.Close()
		}
		t.Fatal("FIFO check blocked past one second")
	}
	if r.idErr != nil {
		t.Fatalf("unix statFileID: %v", r.idErr)
	}
	if r.dev != uint64(st.Dev) || r.ino != uint64(st.Ino) {
		t.Fatalf("FIFO statFileID dev %#x ino %#x, stat dev %#x ino %#x", r.dev, r.ino, uint64(st.Dev), uint64(st.Ino))
	}
	if r.statErr == nil || !strings.Contains(r.statErr.Error(), "not a regular file") {
		t.Fatalf("FIFO statExecutable: %v, want not a regular file before any open", r.statErr)
	}
	if r.replaced || r.replacedStderr != "" {
		t.Fatalf("FIFO: replaced %v, stderr %q", r.replaced, r.replacedStderr)
	}
	if r.watch != nil || !strings.Contains(r.watchStderr, "not a regular file") || strings.Contains(r.watchStderr, "replaced on disk") {
		t.Fatalf("watch of a FIFO: watch %v, stderr %q", r.watch, r.watchStderr)
	}
	// A leaked read open would give this non-blocking writer a reader. ENXIO
	// (no reader) is what an unopened FIFO returns.
	f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err == nil {
		_ = f.Close()
		t.Fatal("FIFO gained a reader; the check opened it")
	}
}
