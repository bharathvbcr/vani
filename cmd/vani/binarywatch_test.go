package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
)

// fakeExe stands in for the daemon's executable on disk: the test sets what a
// stat of it returns (an identity, or an error as a mid-install path gives).
type fakeExe struct {
	mu  sync.Mutex
	id  binIdentity
	err error
}

const fakeExePath = "/fake/Vani Apply.app/Contents/Helpers/vani"

func (f *fakeExe) set(id binIdentity, err error) {
	f.mu.Lock()
	f.id, f.err = id, err
	f.mu.Unlock()
}

// install replaces the two file-system seams for the test. pathErr is what
// os.Executable would return when it cannot name the binary.
func (f *fakeExe) install(t *testing.T, pathErr error) {
	t.Helper()
	oldPath, oldStat := executablePath, statExecutable
	t.Cleanup(func() { executablePath, statExecutable = oldPath, oldStat })
	executablePath = func() (string, error) { return fakeExePath, pathErr }
	statExecutable = func(path string) (binIdentity, error) {
		if path != fakeExePath {
			t.Errorf("stat of %q, want the recorded path %q", path, fakeExePath)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.id, f.err
	}
}

var (
	oldImage = binIdentity{dev: 1, ino: 100, size: 4096, mtime: 1_000}
	newImage = binIdentity{dev: 1, ino: 101, size: 4096, mtime: 1_000} // same bytes' size and time, new inode
)

const maxBinaryDaemonRuns = 8

// wireBinaryDaemon runs `vani daemon` on a clock that fires every wait at
// once, so each loop of the daemon is one job. run is called with the 1-based
// run number; stop ends the daemon as a signal does.
func wireBinaryDaemon(t *testing.T, run func(ctx context.Context, n int, stop context.CancelFunc) error) (stateDir string) {
	t.Helper()
	_, stateDir = setup(t)
	mustCode(t, 0, "init")
	oldSig := signalContext
	t.Cleanup(func() { signalContext = oldSig })
	parent, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	signalContext = func() (context.Context, context.CancelFunc) { return parent, func() {} }
	n := 0
	wireDaemon(t, &brokenLedger{memLedger: fixture()}, func(ctx context.Context) error {
		n++
		if n > maxBinaryDaemonRuns {
			stop() // a daemon that should have restarted is still going: end it so the test fails, not hangs
			return nil
		}
		return run(ctx, n, stop)
	})
	return stateDir
}

// The binary is replaced while a run is going: that run finishes untouched,
// and the daemon exits 75 before it starts another.
func TestReplacedBinaryRestartsTheDaemonAfterTheRunNotDuring(t *testing.T) {
	exe := &fakeExe{id: oldImage}
	exe.install(t, nil)
	runs, finished := 0, 0
	var cancelledDuring error
	stateDir := wireBinaryDaemon(t, func(ctx context.Context, n int, stop context.CancelFunc) error {
		runs = n
		if n == 1 {
			exe.set(newImage, nil) // the reinstall lands mid-run
			time.Sleep(20 * time.Millisecond)
			cancelledDuring = ctx.Err()
			finished++
		}
		return nil
	})
	code, _, stderr := do(t, "daemon")
	if code != exitConfigRestart {
		t.Fatalf("exit %d, want %d\n%s", code, exitConfigRestart, stderr)
	}
	if runs != 1 || finished != 1 || cancelledDuring != nil {
		t.Errorf("runs %d, finished %d, ctx error during the run %v; want one run that completed uncancelled", runs, finished, cancelledDuring)
	}
	const line = "vani: daemon: the vani binary was replaced on disk; restarting so the new one runs\n"
	if strings.Count(stderr, line) != 1 {
		t.Errorf("want the replaced line exactly once:\n%s", stderr)
	}
	if strings.Contains(stderr, "job failed") {
		t.Errorf("a binary replacement was logged as a failed job:\n%s", stderr)
	}
	rec, err := readRunRecord(stateDir)
	if err != nil || rec == nil || rec.Outcome != outcomeOK {
		t.Fatalf("run record %+v, %v; want the finished run's ok record left in place", rec, err)
	}
}

// Nothing replaced the binary: the daemon runs until it is stopped and exits 0
// without the restart line.
func TestUnchangedBinaryDoesNotRestartTheDaemon(t *testing.T) {
	exe := &fakeExe{id: oldImage}
	exe.install(t, nil)
	runs := 0
	wireBinaryDaemon(t, func(_ context.Context, n int, stop context.CancelFunc) error {
		runs = n
		if n == 4 {
			stop()
		}
		return nil
	})
	code, _, stderr := do(t, "daemon")
	if code != exitOK || runs != 4 {
		t.Fatalf("exit %d after %d runs, want 0 after 4\n%s", code, runs, stderr)
	}
	if strings.Contains(stderr, "replaced on disk") || strings.Contains(stderr, "could not identify") {
		t.Errorf("unexpected binary-watch line:\n%s", stderr)
	}
}

// An install in progress deletes the old copy first: while the path is missing
// (or unreadable) the daemon stays up, and it restarts once a different file
// is there.
func TestMissingBinaryMidInstallDoesNotRestartButAReplacementDoes(t *testing.T) {
	exe := &fakeExe{id: oldImage}
	exe.install(t, nil)
	runs := 0
	wireBinaryDaemon(t, func(_ context.Context, n int, stop context.CancelFunc) error {
		runs = n
		switch n {
		case 1:
			exe.set(binIdentity{}, &os.PathError{Op: "stat", Path: fakeExePath, Err: os.ErrNotExist})
		case 2:
			exe.set(binIdentity{}, &os.PathError{Op: "stat", Path: fakeExePath, Err: os.ErrPermission})
		case 3:
			exe.set(newImage, nil)
		case 6:
			stop() // not reached when the daemon restarts as it should
		}
		return nil
	})
	code, _, stderr := do(t, "daemon")
	if code != exitConfigRestart {
		t.Fatalf("exit %d, want %d\n%s", code, exitConfigRestart, stderr)
	}
	if runs != 3 {
		t.Errorf("%d runs, want 3: two with the path unreadable, then the replacement ends it", runs)
	}
}

// The binary's identity could not be recorded at start: the daemon says so
// once, runs on, and never restarts for a replacement it cannot see.
func TestUnrecordableBinaryDisablesTheCheckLoudly(t *testing.T) {
	for name, tc := range map[string]struct {
		pathErr error
		statErr error
	}{
		"os.Executable fails": {pathErr: errors.New("executable path unavailable")},
		"stat fails":          {statErr: &os.PathError{Op: "stat", Path: fakeExePath, Err: os.ErrNotExist}},
	} {
		t.Run(name, func(t *testing.T) {
			exe := &fakeExe{id: oldImage, err: tc.statErr}
			exe.install(t, tc.pathErr)
			runs := 0
			wireBinaryDaemon(t, func(_ context.Context, n int, stop context.CancelFunc) error {
				runs = n
				if n == 2 {
					exe.set(newImage, nil) // a replacement the daemon has no baseline for
				}
				if n == 4 {
					stop()
				}
				return nil
			})
			code, _, stderr := do(t, "daemon")
			if code != exitOK || runs != 4 {
				t.Fatalf("exit %d after %d runs, want 0 after 4\n%s", code, runs, stderr)
			}
			const warn = "vani: daemon: could not identify its own binary ("
			if strings.Count(stderr, warn) != 1 || !strings.Contains(stderr, "will not restart when the binary is replaced") {
				t.Errorf("want one warning that the check is off:\n%s", stderr)
			}
			if strings.Contains(stderr, "replaced on disk") {
				t.Errorf("restarted on an identity it never recorded:\n%s", stderr)
			}
		})
	}
}

// A process that is not the daemon (run-once and the other commands) has no
// watch and is never restarted by one.
func TestNilBinaryWatchNeverReportsReplaced(t *testing.T) {
	var w *binWatch
	var buf bytes.Buffer
	if w.replaced(&buf) || buf.Len() != 0 {
		t.Fatalf("nil watch: replaced or wrote %q", buf.String())
	}
	e := &env{stderr: &buf}
	if _, err := e.reloadConfig(&App{}, config.Default(), nil); err != nil {
		t.Fatalf("reloadConfig with no watch: %v", err)
	}
}

// What differs decides: a new inode, a new size or a new mtime each count,
// including the in-place overwrite that keeps the inode; an equal identity
// does not.
func TestBinWatchComparesInodeSizeAndMtime(t *testing.T) {
	cases := []struct {
		name string
		cur  binIdentity
		want bool
	}{
		{"identical", oldImage, false},
		{"new inode only", binIdentity{dev: 1, ino: 999, size: 4096, mtime: 1_000}, true},
		{"new device only", binIdentity{dev: 2, ino: 100, size: 4096, mtime: 1_000}, true},
		{"same inode, new size (overwritten in place)", binIdentity{dev: 1, ino: 100, size: 8192, mtime: 1_000}, true},
		{"same inode and size, new mtime (overwritten in place)", binIdentity{dev: 1, ino: 100, size: 4096, mtime: 2_000}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exe := &fakeExe{id: oldImage}
			exe.install(t, nil)
			w := watchOwnBinary(&bytes.Buffer{})
			if w == nil {
				t.Fatal("no watch for a statable binary")
			}
			exe.set(tc.cur, nil)
			var buf bytes.Buffer
			if got := w.replaced(&buf); got != tc.want {
				t.Fatalf("replaced = %v, want %v", got, tc.want)
			}
			if tc.want != strings.Contains(buf.String(), "replaced on disk") {
				t.Fatalf("stderr %q does not match replaced = %v", buf.String(), tc.want)
			}
		})
	}
}

// Many checks racing (the poll, a run and a manual run can all reload) say it
// once, and each still reports the replacement.
func TestBinWatchSaysReplacedOnce(t *testing.T) {
	exe := &fakeExe{id: oldImage}
	exe.install(t, nil)
	w := watchOwnBinary(&bytes.Buffer{})
	exe.set(newImage, nil)
	var buf syncBuffer
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !w.replaced(&buf) {
				t.Error("a check missed the replacement")
			}
		}()
	}
	wg.Wait()
	if n := strings.Count(buf.String(), "replaced on disk"); n != 1 {
		t.Fatalf("said %d times, want once:\n%s", n, buf.String())
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// The Windows file index is two DWORD halves. A pack that drops them (both
// zeros, or the volume only) makes two files with the same size and mtime
// look like one file.
func TestPackWindowsFileIDDiffersWhenOnlyIndexDiffers(t *testing.T) {
	dev, ino := packWindowsFileID(0xA1B2C3D4, 0x01020304, 0x05060708)
	if dev == 0 && ino == 0 {
		t.Fatal("packWindowsFileID returned a zero identity for a non-zero index")
	}
	dev2, ino2 := packWindowsFileID(0xA1B2C3D4, 0x01020304, 0x05060709)
	if dev == dev2 && ino == ino2 {
		t.Fatal("same volume and a different file index packed to the same identity")
	}
	if dev != dev2 {
		t.Fatalf("volume serial changed with the index: %#x vs %#x", dev, dev2)
	}
	const wantIno = uint64(0x01020304)<<32 | 0x05060708
	if dev != 0xA1B2C3D4 || ino != wantIno {
		t.Fatalf("got dev %#x ino %#x, want volume and high<<32|low", dev, ino)
	}
}

// The real stat on real files: a delete-and-rewrite (how a reinstall puts a
// new copy at the path), an overwrite in place, a repointed symlink and a
// missing path, with the default statExecutable.
func TestBinWatchOnRealFiles(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "vani")
	write := func(path, body string, mtime time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Unix(1_700_000_000, 0)
	watch := func(path string) *binWatch {
		t.Helper()
		oldPath := executablePath
		t.Cleanup(func() { executablePath = oldPath })
		executablePath = func() (string, error) { return path, nil }
		w := watchOwnBinary(&bytes.Buffer{})
		if w == nil {
			t.Fatalf("no watch for %s", path)
		}
		return w
	}
	quiet := &bytes.Buffer{}

	write(exe, "old image", t0)
	w := watch(exe)
	if w.replaced(quiet) {
		t.Fatal("an untouched file reads as replaced")
	}

	// In place: same inode, same size, only the mtime moves.
	write(exe, "new image", t0.Add(time.Hour))
	if !w.replaced(quiet) {
		t.Error("an in-place overwrite was not seen")
	}

	// Delete first, as an installer does: the missing path is not a replacement.
	write(exe, "old image", t0)
	w = watch(exe)
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	if w.replaced(quiet) {
		t.Error("a missing path restarted the daemon")
	}
	write(exe, "a different, longer image", t0.Add(2*time.Hour))
	if !w.replaced(quiet) {
		t.Error("the file written after the delete was not seen")
	}

	// A symlink is followed on every check, so repointing it counts.
	a, b, link := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "link")
	write(a, "image a", t0)
	write(b, "image b", t0.Add(time.Hour))
	if err := os.Symlink(a, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	w = watch(link)
	if w.replaced(quiet) {
		t.Fatal("an untouched symlink reads as replaced")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b, link); err != nil {
		t.Fatal(err)
	}
	if !w.replaced(quiet) {
		t.Error("a repointed symlink was not seen")
	}

	// Not a file the daemon could be running from: no baseline, no check.
	var warned bytes.Buffer
	executablePath = func() (string, error) { return dir, nil }
	if got := watchOwnBinary(&warned); got != nil || !strings.Contains(warned.String(), "could not identify its own binary") {
		t.Errorf("a directory as the executable: watch %v, stderr %q", got, warned.String())
	}
}
