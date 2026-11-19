//go:build unix

package atomicfile_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

func fdCount(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skipf("no /dev/fd: %v", err)
	}
	return len(ents)
}

// A symlink to an outside file flips in and out of the leaf as fast as a
// goroutine can. No OpenRead may ever hand back the outside file's contents.
func TestAttackOpenReadSymlinkFlipNeverYieldsTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("OUTSIDE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				tmp := path + ".l"
				_ = os.Remove(tmp)
				if os.Symlink(outside, tmp) == nil {
					_ = os.Rename(tmp, path)
				}
			} else {
				tmp := path + ".r"
				if os.WriteFile(tmp, []byte("inside"), 0o600) == nil {
					_ = os.Rename(tmp, path)
				}
			}
		}
	}()
	var ok, linked, changing int
	for i := 0; i < 3000; i++ {
		f, err := atomicfile.OpenRead(path)
		switch {
		case err == nil:
			b, _ := io.ReadAll(f)
			f.Close()
			if string(b) != "inside" {
				t.Errorf("read %q through a flipping link", b)
			}
			ok++
		case errors.Is(err, atomicfile.ErrLinkedLeaf):
			linked++
		case errors.Is(err, atomicfile.ErrChanging):
			changing++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	close(stop)
	wg.Wait()
	t.Logf("ok=%d linked=%d changing=%d", ok, linked, changing)
}

// A swap during the window to a FIFO or a directory is refused at once and
// never blocks.
func TestAttackOpenReadSwappedToFIFOOrDirIsRefused(t *testing.T) {
	for _, kind := range []string{"fifo", "dir"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state")
			if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			defer atomicfile.SetAfterOpenHookForTest(func() {
				calls++
				_ = os.Remove(path)
				if kind == "fifo" {
					if err := syscall.Mkfifo(path, 0o600); err != nil {
						t.Error(err)
					}
				} else if err := os.Mkdir(path, 0o700); err != nil {
					t.Error(err)
				}
			})()
			done := make(chan error, 1)
			go func() {
				f, err := atomicfile.OpenRead(path)
				if f != nil {
					f.Close()
				}
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, atomicfile.ErrNotRegular) {
					t.Fatalf("got %v, want ErrNotRegular", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("OpenRead blocked")
			}
			if calls != 1 {
				t.Fatalf("retried a non-regular leaf: %d", calls)
			}
		})
	}
}

// A hardlink is a regular file: Lstat says regular and SameFile holds, so it is
// accepted, exactly as before the retry change (the old code had the same two
// checks). Pinned so a change is noticed.
func TestAttackOpenReadHardlinkIsAcceptedAsBefore(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(outside, []byte("linked"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state")
	if err := os.Link(outside, path); err != nil {
		t.Skip(err)
	}
	f, err := atomicfile.OpenRead(path)
	if err != nil {
		t.Fatalf("hardlink refused: %v", err)
	}
	f.Close()
}

// A hardlink to an outside file renamed in during the window is a different
// regular file: retried, and the next attempt reads the hardlinked content (the
// same acceptance as a pre-placed hardlink, never the old file's handle).
func TestAttackOpenReadHardlinkRenamedInDuringOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	outside := filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(outside, []byte("linked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	fired := false
	defer atomicfile.SetAfterOpenHookForTest(func() {
		if fired {
			return
		}
		fired = true
		tmp := path + ".h"
		if err := os.Link(outside, tmp); err != nil {
			t.Error(err)
			return
		}
		if err := os.Rename(tmp, path); err != nil {
			t.Error(err)
		}
	})()
	f, err := atomicfile.OpenRead(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	if string(b) != "linked" {
		t.Fatalf("read %q", b)
	}
}

// Deleted during the window: the retry reports a missing file, not ErrChanging.
func TestAttackOpenReadRemovedDuringOpenIsNotExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer atomicfile.SetAfterOpenHookForTest(func() { _ = os.Remove(path) })()
	f, err := atomicfile.OpenRead(path)
	if f != nil {
		f.Close()
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v, want ErrNotExist", err)
	}
}

// No descriptor outlives a call: not on success, a retry, a refusal or
// ErrChanging.
func TestAttackOpenReadDoesNotLeakDescriptors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := fdCount(t)
	for i := 0; i < 1000; i++ {
		restore := atomicfile.SetAfterOpenHookForTest(func() { replaceByRename(t, path, "y") })
		f, err := atomicfile.OpenRead(path) // every attempt loses: ErrChanging
		restore()
		if f != nil {
			f.Close()
		}
		if !errors.Is(err, atomicfile.ErrChanging) {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = atomicfile.Write(path, []byte{byte('a' + i%26)})
		}
	}()
	for i := 0; i < 1000; i++ {
		f, err := atomicfile.OpenRead(path)
		if err == nil {
			f.Close()
		} else if !errors.Is(err, atomicfile.ErrChanging) {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	if after := fdCount(t); after > before+2 {
		t.Fatalf("descriptors %d -> %d", before, after)
	}
}

// Under a writer that renames without pause, measure ErrChanging and bound the
// time. The rate is logged; the test fails only on a time bound break or a
// non-sentinel error.
func TestAttackOpenReadUnderContinuousRenamer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			tmp := path + string(rune('a'+w))
			for {
				select {
				case <-stop:
					return
				default:
				}
				if os.WriteFile(tmp, []byte("y"), 0o600) == nil {
					_ = os.Rename(tmp, path)
				}
			}
		}(w)
	}
	const n = 3000
	var changing int
	var worst time.Duration
	for i := 0; i < n; i++ {
		s := time.Now()
		f, err := atomicfile.OpenRead(path)
		if d := time.Since(s); d > worst {
			worst = d
		}
		if err == nil {
			f.Close()
		} else if errors.Is(err, atomicfile.ErrChanging) {
			changing++
		} else {
			t.Errorf("unexpected: %v", err)
		}
	}
	close(stop)
	wg.Wait()
	t.Logf("ErrChanging %d/%d, worst call %v", changing, n, worst)
	if worst > 2*time.Second {
		t.Fatalf("a call took %v", worst)
	}
}
