package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
)

// The Windows index is two DWORD halves. Adding them, or shifting the high
// half by the wrong width, makes indexHigh=1,indexLow=0 the same file as
// indexHigh=0,indexLow=1. A zero pack is the zero identity, and a different
// volume or index must not land on the same dev and ino.
func TestAdvPackWindowsFileIDHighHalfDoesNotCollide(t *testing.T) {
	dev, ino := packWindowsFileID(0, 0, 0)
	if dev != 0 || ino != 0 {
		t.Fatalf("packWindowsFileID(0,0,0) = dev %#x ino %#x, want the zero identity", dev, ino)
	}
	hiDev, hiIno := packWindowsFileID(7, 1, 0)
	loDev, loIno := packWindowsFileID(7, 0, 1)
	if hiDev != 7 || loDev != 7 {
		t.Fatalf("volume serial is not dev: high (%#x,%#x) low (%#x,%#x)", hiDev, hiIno, loDev, loIno)
	}
	if hiIno == loIno {
		t.Fatalf("indexHigh=1,indexLow=0 packed equal to indexHigh=0,indexLow=1 (ino %#x); the high half was not shifted", hiIno)
	}
	if hiIno != uint64(1)<<32 || loIno != 1 {
		t.Fatalf("got high ino %#x low ino %#x, want %#x and 1", hiIno, loIno, uint64(1)<<32)
	}
	volDev, volIno := packWindowsFileID(8, 1, 0)
	if volDev == hiDev && volIno == hiIno {
		t.Fatal("a different volume serial packed to the same dev and ino")
	}
	idxDev, idxIno := packWindowsFileID(7, 1, 2)
	if idxDev == hiDev && idxIno == hiIno {
		t.Fatal("a different file index packed to the same dev and ino")
	}
	_, all := packWindowsFileID(1, 0xFFFFFFFF, 0xFFFFFFFF)
	if all != ^uint64(0) {
		t.Fatalf("full index packed to %#x, want both halves", all)
	}
}

// Two files with the same size and mtime still differ when the packed file
// index differs. The restart line is said once; a second check still reports
// the replacement. An untouched pack is not a replacement.
func TestAdvPackedIndexSameSizeMtimeReplacedOnce(t *testing.T) {
	cases := []struct {
		name        string
		aVol, bVol  uint32
		aHigh, aLow uint32
		bHigh, bLow uint32
	}{
		{"high half versus low half", 0xA1B2C3D4, 0xA1B2C3D4, 1, 0, 0, 1},
		{"low half only", 0xA1B2C3D4, 0xA1B2C3D4, 0x01020304, 0x05060708, 0x01020304, 0x05060709},
		{"volume only", 1, 2, 1, 2, 1, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d1, i1 := packWindowsFileID(tc.aVol, tc.aHigh, tc.aLow)
			d2, i2 := packWindowsFileID(tc.bVol, tc.bHigh, tc.bLow)
			if d1 == d2 && i1 == i2 {
				t.Fatal("packs collided, so this case does not change the identity")
			}
			start := binIdentity{dev: d1, ino: i1, size: 4096, mtime: 1_000}
			next := binIdentity{dev: d2, ino: i2, size: start.size, mtime: start.mtime}
			exe := &fakeExe{id: start}
			exe.install(t, nil)
			w := watchOwnBinary(&bytes.Buffer{})
			if w == nil {
				t.Fatal("no watch for a statable binary")
			}
			var quiet bytes.Buffer
			if w.replaced(&quiet) || quiet.Len() != 0 {
				t.Fatalf("untouched packed identity: replaced, stderr %q", quiet.String())
			}
			exe.set(next, nil)
			var buf bytes.Buffer
			if !w.replaced(&buf) {
				t.Fatal("same size and mtime with a different packed index was not replaced")
			}
			if !w.replaced(&buf) {
				t.Fatal("a second check dropped the replacement")
			}
			const line = "vani: daemon: the vani binary was replaced on disk; restarting so the new one runs\n"
			if n := strings.Count(buf.String(), line); n != 1 {
				t.Fatalf("said %d times, want once:\n%s", n, buf.String())
			}
		})
	}
}

// A statFileID failure (the Windows open or file-index query) is an unreadable
// path: replaced is false, nothing is announced, and the daemon does not
// restart. The one-shot line is still available for a later real replacement.
func TestAdvStatFileIDErrorStaysQuiet(t *testing.T) {
	if exitConfigRestart != 75 {
		t.Fatalf("exitConfigRestart = %d, want 75", exitConfigRestart)
	}
	statErr := &os.PathError{Op: "GetFileInformationByHandle", Path: fakeExePath, Err: os.ErrPermission}
	exe := &fakeExe{id: oldImage}
	exe.install(t, nil)
	w := watchOwnBinary(&bytes.Buffer{})
	if w == nil {
		t.Fatal("baseline was not recorded")
	}
	exe.set(binIdentity{}, statErr)
	var buf bytes.Buffer
	if w.replaced(&buf) || buf.Len() != 0 {
		t.Fatalf("statFileID error: replaced, stderr %q", buf.String())
	}
	if w.replaced(&buf) || buf.Len() != 0 {
		t.Fatalf("second statFileID error: replaced, stderr %q", buf.String())
	}
	e := &env{stderr: &buf, binary: w}
	if _, err := e.reloadConfig(&App{}, config.Default(), nil); err != nil || buf.Len() != 0 {
		t.Fatalf("reloadConfig on a statFileID error: %v stderr %q", err, buf.String())
	}
	exe.set(newImage, nil)
	if !w.replaced(&buf) {
		t.Fatal("a later replacement was missed after the statFileID error")
	}
	if n := strings.Count(buf.String(), "replaced on disk"); n != 1 {
		t.Fatalf("replacement after the error said %d times:\n%s", n, buf.String())
	}

	exe.set(oldImage, nil)
	runs := 0
	wireBinaryDaemon(t, func(_ context.Context, n int, stop context.CancelFunc) error {
		runs = n
		if n == 1 {
			exe.set(binIdentity{}, statErr)
		}
		if n == 4 {
			stop()
		}
		return nil
	})
	code, _, stderr := do(t, "daemon")
	if code != exitOK || runs != 4 {
		t.Fatalf("exit %d after %d runs, want 0 after 4 (a statFileID error is not a restart)\n%s", code, runs, stderr)
	}
	if strings.Contains(stderr, "replaced on disk") || strings.Contains(stderr, "could not identify") {
		t.Fatalf("statFileID error was not quiet:\n%s", stderr)
	}
}

// Delete and rewrite the same bytes at the same mtime. Size and mtime match;
// the new file's device or inode (on Windows, the packed file index) must
// still count, and the line is said once.
func TestAdvSameSizeMtimeNewFileIsReplacedOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vani")
	body := []byte("same-bytes")
	mtime := time.Unix(1_700_000_000, 0)
	write := func() {
		t.Helper()
		if err := os.WriteFile(path, body, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	write()
	oldPath := executablePath
	t.Cleanup(func() { executablePath = oldPath })
	executablePath = func() (string, error) { return path, nil }
	w := watchOwnBinary(&bytes.Buffer{})
	if w == nil {
		t.Fatal("no watch for a regular file")
	}
	before, err := statExecutable(path)
	if err != nil {
		t.Fatal(err)
	}
	var quiet bytes.Buffer
	if w.replaced(&quiet) || quiet.Len() != 0 {
		t.Fatalf("untouched file: replaced, stderr %q", quiet.String())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// A filesystem that recycles the just-freed inode would make this rewrite
	// the same file. Hold another inode across the create so the new file
	// cannot be handed that one back.
	burn := path + ".burn"
	if err := os.WriteFile(burn, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	write()
	if err := os.Remove(burn); err != nil {
		t.Fatal(err)
	}
	after, err := statExecutable(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.size != before.size || after.mtime != before.mtime {
		t.Fatalf("rewrite changed size or mtime: before %+v after %+v", before, after)
	}
	if after.dev == before.dev && after.ino == before.ino {
		t.Fatalf("rewrite kept device %#x inode %#x; identity is not coming from the file", before.dev, before.ino)
	}
	var buf bytes.Buffer
	if !w.replaced(&buf) {
		t.Fatal("same size and mtime with a new file was not replaced")
	}
	if !w.replaced(&buf) {
		t.Fatal("a second check dropped the replacement")
	}
	if n := strings.Count(buf.String(), "replaced on disk"); n != 1 {
		t.Fatalf("said %d times, want once:\n%s", n, buf.String())
	}
}

// A directory is not a regular file. The check returns an error before opening
// it, so replaced is false, the check is quiet, and it comes back within a second.
func TestAdvDirectoryIsNotAReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vani")
	if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
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
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	var (
		statErr  error
		replaced bool
		buf      bytes.Buffer
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, statErr = statExecutable(path)
		replaced = w.replaced(&buf)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("directory check blocked past one second")
	}
	if statErr == nil || !strings.Contains(statErr.Error(), "not a regular file") {
		t.Fatalf("directory stat: %v, want not a regular file", statErr)
	}
	if replaced || buf.Len() != 0 {
		t.Fatalf("directory: replaced %v, stderr %q", replaced, buf.String())
	}
}
