package kbmatch

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestLockHolderHelper is the child of the SIGKILL test: it takes the store lock,
// says so, and holds it until it is killed. It does nothing in a normal run.
func TestLockHolderHelper(t *testing.T) {
	path := os.Getenv("KBMATCH_LOCK_HELPER")
	if path == "" {
		t.Skip("helper process only")
	}
	if _, err := Lock(path); err != nil {
		os.Stdout.WriteString("ERR " + err.Error() + "\n")
		os.Exit(3)
	}
	os.Stdout.WriteString("locked\n")
	time.Sleep(time.Minute)
	os.Exit(0)
}

func TestLockReleasedWhenHolderIsKilled(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHolderHelper$")
	cmd.Env = append(os.Environ(), "KBMATCH_LOCK_HELPER="+path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL); _, _ = cmd.Process.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("helper did not lock: %q %v", line, err)
	}
	if _, err := Lock(path); !errors.Is(err, ErrBusy) {
		t.Fatalf("lock held by another process: %v, want ErrBusy", err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_, _ = cmd.Process.Wait()
	unlock, err := LockWait(path, 2*time.Second)
	if err != nil {
		t.Fatalf("the OS did not release the lock of a killed process: %v", err)
	}
	unlock()
	// The stale .lock file is not the lock: a later run is not refused by it.
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatalf("lock file: %v", err)
	}
}

func TestLockWaitBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	held, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, wait := range []time.Duration{0, -time.Second, -time.Hour} {
		start := time.Now()
		if _, err := LockWait(path, wait); !errors.Is(err, ErrBusy) {
			t.Errorf("wait %v: %v, want ErrBusy", wait, err)
		}
		if el := time.Since(start); el > 500*time.Millisecond {
			t.Errorf("wait %v took %v", wait, el)
		}
	}
	// wait <= 0 on a free lock still makes the one try.
	held()
	for _, wait := range []time.Duration{0, -time.Second} {
		unlock, err := LockWait(path, wait)
		if err != nil {
			t.Fatalf("free lock with wait %v: %v", wait, err)
		}
		unlock()
	}
	// A bounded wait ends near the bound, not far past it.
	held, _ = Lock(path)
	defer held()
	start := time.Now()
	if _, err := LockWait(path, 200*time.Millisecond); !errors.Is(err, ErrBusy) {
		t.Fatalf("got %v", err)
	}
	if el := time.Since(start); el < 200*time.Millisecond || el > 700*time.Millisecond {
		t.Errorf("a 200ms wait took %v", el)
	}
}

func TestLockWaitGetsLockWhenReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	held, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(150 * time.Millisecond); held() }()
	unlock, err := LockWait(path, 5*time.Second)
	if err != nil {
		t.Fatalf("waiter did not get the lock: %v", err)
	}
	unlock()
}

// Recovery: only unreadable-content stores are removed, and only under the lock;
// an I/O failure never replaces the store.
func TestOpenLockedRecovery(t *testing.T) {
	p0 := filepath.Join(t.TempDir(), FileName)
	s0 := NewStore(p0)
	if err := s0.Put("k", Entry{Value: "core", KB: strings.Repeat("a", 64), Text: strings.Repeat("b", 64), Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if err := s0.Save(); err != nil {
		t.Fatal(err)
	}
	valid, err := os.ReadFile(p0)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{
		"garbage":   []byte("{not json"),
		"truncated": valid[:len(valid)/2],
		"empty":     {},
		"trailing":  append(append([]byte{}, valid...), []byte("{}")...),
		"oversized": []byte(strings.Repeat(" ", maxFileBytes+10)),
		"nul":       {0, 0, 0},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			st, unlock, recovered, err := OpenLocked(path, 0)
			if err != nil {
				t.Fatalf("OpenLocked: %v", err)
			}
			if !errors.Is(recovered, ErrCorrupt) || st.Len() != 0 {
				t.Errorf("recovered=%v len=%d", recovered, st.Len())
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("corrupt store left in place: %v", err)
			}
			// Still locked after recovery.
			if _, err := Lock(path); !errors.Is(err, ErrBusy) {
				t.Errorf("lock not held after recovery: %v", err)
			}
			unlock()
			if u, err := Lock(path); err != nil {
				t.Errorf("lock not released: %v", err)
			} else {
				u()
			}
		})
	}

	t.Run("busy leaves a corrupt store alone", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), FileName)
		if err := os.WriteFile(path, []byte("{bad"), 0o600); err != nil {
			t.Fatal(err)
		}
		held, _ := Lock(path)
		defer held()
		if _, _, _, err := OpenLocked(path, 0); !errors.Is(err, ErrBusy) {
			t.Fatalf("got %v", err)
		}
		if raw, err := os.ReadFile(path); err != nil || string(raw) != "{bad" {
			t.Errorf("store touched without the lock: %q %v", raw, err)
		}
	})

	t.Run("directory at the path is not removed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), FileName)
		if err := os.MkdirAll(filepath.Join(path, "inner"), 0o700); err != nil {
			t.Fatal(err)
		}
		_, _, recovered, err := OpenLocked(path, 0)
		if err == nil || recovered != nil {
			t.Fatalf("a directory at the store path: err=%v recovered=%v", err, recovered)
		}
		if _, serr := os.Stat(filepath.Join(path, "inner")); serr != nil {
			t.Errorf("directory contents removed: %v", serr)
		}
		if u, lerr := Lock(path); lerr != nil {
			t.Errorf("lock leaked on the error path: %v", lerr)
		} else {
			u()
		}
	})

	t.Run("permission denied is not replaced", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads anything")
		}
		path := filepath.Join(t.TempDir(), FileName)
		if err := os.WriteFile(path, valid, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
		_, _, recovered, err := OpenLocked(path, 0)
		if err == nil || recovered != nil || !errors.Is(err, os.ErrPermission) {
			t.Fatalf("err=%v recovered=%v, want a permission error", err, recovered)
		}
		if _, serr := os.Stat(path); serr != nil {
			t.Errorf("unreadable store was removed: %v", serr)
		}
		_ = os.Chmod(path, 0o600)
		if raw, _ := os.ReadFile(path); string(raw) != string(valid) {
			t.Errorf("unreadable store content changed")
		}
		if u, lerr := Lock(path); lerr != nil {
			t.Errorf("lock leaked: %v", lerr)
		} else {
			u()
		}
	})

	t.Run("symlinked corrupt store: link removed, target kept", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "elsewhere.json")
		if err := os.WriteFile(target, []byte("{bad"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, FileName)
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		_, unlock, recovered, err := OpenLocked(path, 0)
		if err != nil || recovered == nil {
			t.Fatalf("err=%v recovered=%v", err, recovered)
		}
		unlock()
		if _, err := os.Stat(target); err != nil {
			t.Errorf("os.Remove followed the symlink: %v", err)
		}
	})
}
