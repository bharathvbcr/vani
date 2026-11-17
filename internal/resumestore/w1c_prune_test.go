package resumestore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/filelock"
)

// w1cFull fills a store's root with MaxJobs empty job folders, the first the
// least recently saved, and returns the root and each folder's name.
func w1cFull(t *testing.T, state string) (string, func(int) string) {
	t.Helper()
	root := filepath.Join(state, "resumes")
	name := func(i int) string { return fmt.Sprintf("%064x", i+1) }
	for i := range MaxJobs {
		d := filepath.Join(root, name(i))
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		at := t0.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(d, at, at); err != nil {
			t.Fatal(err)
		}
	}
	return root, name
}

func w1cHook(t *testing.T, fn func(dir string)) {
	t.Helper()
	beforePruneLock = fn
	t.Cleanup(func() { beforePruneLock = nil })
}

// A second holder takes the oldest candidate between the listing and makeRoom's
// lock and applies a version (the used pointer): the folder is kept and the
// next oldest goes.
func TestW1cMakeRoomKeepsAFolderUsedInTheWindow(t *testing.T) {
	s, state := newStore(t)
	root, name := w1cFull(t, state)
	var calls int
	w1cHook(t, func(dir string) {
		calls++
		if filepath.Base(dir) != name(0) {
			return
		}
		l, err := filelock.TryAcquire(filepath.Join(dir, lockFile))
		if err != nil {
			t.Error(err)
			return
		}
		defer l.Release()
		if err := os.WriteFile(filepath.Join(dir, usedFile), []byte("1\n"), 0o600); err != nil {
			t.Error(err)
		}
	})
	mustSave(t, s, "fresh", goodMeta(), pdfBytes("1"))
	if _, err := os.Stat(filepath.Join(root, name(0))); err != nil {
		t.Fatalf("a folder that became used in the window was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, name(1))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the next candidate survived: %v", err)
	}
	if calls != 2 {
		t.Errorf("hook ran %d times, want 2 (the skipped candidate and the pruned one)", calls)
	}
}

// The same window, but the second holder commits a just-saved version.
func TestW1cMakeRoomKeepsAFolderSavedInTheWindow(t *testing.T) {
	s, state := newStore(t)
	root, name := w1cFull(t, state)
	w1cHook(t, func(dir string) {
		if filepath.Base(dir) != name(0) {
			return
		}
		l, err := filelock.TryAcquire(filepath.Join(dir, lockFile))
		if err != nil {
			t.Error(err)
			return
		}
		defer l.Release()
		if err := os.WriteFile(filepath.Join(dir, "v1.json"), []byte("{}"), 0o600); err != nil {
			t.Error(err)
		}
	})
	mustSave(t, s, "fresh", goodMeta(), pdfBytes("1"))
	if _, err := os.Stat(filepath.Join(root, name(0), "v1.json")); err != nil {
		t.Fatalf("a folder saved in the window was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, name(1))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the next candidate survived: %v", err)
	}
}

// A candidate whose job lock is held is skipped, not deleted.
func TestW1cMakeRoomSkipsAHeldFolder(t *testing.T) {
	s, state := newStore(t)
	root, name := w1cFull(t, state)
	held, err := filelock.TryAcquire(filepath.Join(root, name(0), lockFile))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	mustSave(t, s, "fresh", goodMeta(), pdfBytes("1"))
	if _, err := os.Stat(filepath.Join(root, name(0))); err != nil {
		t.Fatalf("a held folder was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, name(1))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the next candidate survived: %v", err)
	}
}

// Every candidate becomes used in the window: nothing is deleted and the store
// is full, as when they were used from the start.
func TestW1cMakeRoomFullWhenEveryFolderBecomesUsed(t *testing.T) {
	s, state := newStore(t)
	root, _ := w1cFull(t, state)
	w1cHook(t, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, usedFile), []byte("1\n"), 0o600); err != nil {
			t.Error(err)
		}
	})
	if _, _, err := s.Save(context.Background(), "fresh", goodMeta(), pdfBytes("1")); !errors.Is(err, ErrFull) {
		t.Fatalf("err = %v, want ErrFull", err)
	}
	if fs, _ := s.jobFolders(); len(fs) != MaxJobs {
		t.Fatalf("%d folders, want %d: a used one was deleted", len(fs), MaxJobs)
	}
	if _, err := os.Stat(filepath.Join(root, jobFolder("fresh"))); err == nil {
		t.Fatal("a refused Save left its folder")
	}
}

const (
	w1cRoleEnv  = "W1C_RESUMESTORE_ROLE"
	w1cStateEnv = "W1C_RESUMESTORE_STATE"
	w1cReals    = 12
	w1cPrunes   = 10
)

func w1cRealKey(i int) string { return fmt.Sprintf("real-%d", i) }

// TestW1cPruneChild is the child of the two-process test (a no-op unless the
// parent re-executes the test binary). The pruner saves new jobs into a full
// store, so each Save prunes; the acquirer applies the oldest jobs' versions
// meanwhile and lists the ones it was granted.
func TestW1cPruneChild(t *testing.T) {
	role, state := os.Getenv(w1cRoleEnv), os.Getenv(w1cStateEnv)
	if role == "" {
		t.Skip("child role only")
	}
	s, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(time.Millisecond) {
		if _, err := os.Stat(filepath.Join(state, "go")); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatal("no go signal")
		}
	}
	ctx := context.Background()
	switch role {
	case "prune":
		// Widen the window between makeRoom's listing and its job lock until the
		// acquirer has applied every real job, so every real job is a candidate
		// whose used pointer appears only after the listing. Only the under-lock
		// re-check keeps them. (The acquirer cannot go through Use here: Use
		// needs the store lock the pruner holds across the window.)
		beforePruneLock = func(string) {
			_ = os.WriteFile(filepath.Join(state, "window"), nil, 0o600)
			for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
				if _, err := os.Stat(filepath.Join(state, "acquired")); err == nil {
					return
				}
			}
			t.Error("the acquirer never finished applying")
		}
		for i := range w1cPrunes {
			if _, _, err := s.Save(ctx, fmt.Sprintf("new-%d", i), goodMeta(), pdfBytes("n")); err != nil {
				t.Fatalf("prune Save %d: %v", i, err)
			}
		}
	case "acquire":
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(time.Millisecond) {
			if _, err := os.Stat(filepath.Join(state, "window")); err == nil {
				break
			} else if time.Now().After(deadline) {
				t.Fatal("the pruner never reached the window")
			}
		}
		// Inside the window: take each real job's lock as Use does and bind its
		// version for applying, which is a used pointer the pruner's listing has
		// not seen.
		var got []string
		for i := range w1cReals {
			dir := s.dir(w1cRealKey(i))
			var l *filelock.Lock
			for deadline := time.Now().Add(10 * time.Second); l == nil; time.Sleep(time.Millisecond) {
				var err error
				if l, err = filelock.TryAcquire(filepath.Join(dir, lockFile)); err != nil && time.Now().After(deadline) {
					t.Fatalf("job %d lock: %v", i, err)
				}
			}
			err := os.WriteFile(filepath.Join(dir, usedFile), []byte("1\n"), 0o600)
			_ = l.Release()
			if err != nil {
				t.Fatalf("used %d: %v", i, err)
			}
			got = append(got, w1cRealKey(i))
		}
		if err := os.WriteFile(filepath.Join(state, "acquired"), []byte(strings.Join(got, "\n")), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// One process prunes a full store while another applies the versions it would
// prune first, over and over: every version applied is still readable after.
func TestW1cPruneAndUseAcrossProcesses(t *testing.T) {
	rounds := 50
	if testing.Short() {
		rounds = 8
	}
	for round := range rounds {
		state := t.TempDir()
		s, err := Open(state)
		if err != nil {
			t.Fatal(err)
		}
		for i := range w1cReals {
			mustSave(t, s, w1cRealKey(i), goodMeta(), pdfBytes(fmt.Sprint(i)))
		}
		// Fillers are saved later than the real jobs, so the real ones are pruned first.
		root := filepath.Join(state, "resumes")
		for i := range MaxJobs - w1cReals {
			d := filepath.Join(root, fmt.Sprintf("%064x", i+1))
			if err := os.MkdirAll(d, 0o700); err != nil {
				t.Fatal(err)
			}
			at := time.Now().Add(time.Hour + time.Duration(i)*time.Second)
			if err := os.Chtimes(d, at, at); err != nil {
				t.Fatal(err)
			}
		}
		var cmds []*exec.Cmd
		var outs []*bytes.Buffer
		for _, role := range []string{"prune", "acquire"} {
			c := exec.Command(os.Args[0], "-test.run=^TestW1cPruneChild$", "-test.timeout=120s")
			c.Env = append(os.Environ(), w1cRoleEnv+"="+role, w1cStateEnv+"="+state)
			out := &bytes.Buffer{}
			c.Stdout, c.Stderr = out, out
			if err := c.Start(); err != nil {
				t.Fatal(err)
			}
			cmds, outs = append(cmds, c), append(outs, out)
		}
		if err := os.WriteFile(filepath.Join(state, "go"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		for i, c := range cmds {
			if err := c.Wait(); err != nil {
				t.Fatalf("round %d child %d: %v\n%s", round, i, err, outs[i].String())
			}
		}
		b, err := os.ReadFile(filepath.Join(state, "acquired"))
		if err != nil {
			t.Fatalf("round %d: the acquirer reported nothing: %v", round, err)
		}
		for _, key := range strings.Fields(string(b)) {
			if _, err := s.Used(key); err != nil {
				t.Fatalf("round %d: %s was applied and is not readable as used: %v", round, key, err)
			}
			if _, _, err := s.Get(key, 1); err != nil {
				t.Fatalf("round %d: %s was applied and its version is gone: %v", round, key, err)
			}
		}
	}
}
