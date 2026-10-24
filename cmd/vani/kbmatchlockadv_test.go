package main

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/decide"
)

// TestKBMatchRunHelper is the child of the cross-process tests: a real `vani
// kbmatch run` whose model is a fake that logs each call to a file (one line per
// call, so the parent can count across processes) and sleeps. It does nothing
// in a normal run.
func TestKBMatchRunHelper(t *testing.T) {
	logPath := os.Getenv("VANI_KBM_HELPER_LOG")
	if logPath == "" {
		t.Skip("helper process only")
	}
	fixedNow(t)
	// This child is a separate process, so the parent's fake rkb is not here: the
	// digest is built from eligible bullets, which needs rkb's claims.
	rkbRunner = claimsRKB{eligibleTestClaims}
	sleep, _ := time.ParseDuration(os.Getenv("VANI_KBM_HELPER_SLEEP"))
	oldD, oldF := kbmatchDecider, kbmatchFocus
	defer func() { kbmatchDecider, kbmatchFocus = oldD, oldF }()
	f := &sleepyKB{log: logPath, sleep: sleep}
	kbmatchDecider = func(context.Context, *wired, string) (decide.Decider, error) { return f, nil }
	var o, e bytes.Buffer
	code := run([]string{"kbmatch", "run"}, &o, &e)
	os.Stdout.WriteString("EXIT " + strconv.Itoa(code) + "\n" + o.String() + e.String())
	os.Exit(0)
}

type sleepyKB struct {
	log   string
	sleep time.Duration
}

func (s *sleepyKB) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	lf, err := os.OpenFile(s.log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return decide.Answer{}, err
	}
	_, _ = lf.WriteString("call\n")
	_ = lf.Close()
	select {
	case <-time.After(s.sleep):
	case <-ctx.Done():
	}
	return decide.Answer{Backend: "llm", Slots: map[string]decide.SlotReading{kbmatch.Slot: {Reading: decide.Answered, Value: "core"}}}, nil
}

func callCount(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(raw), "call\n")
}

func startHelper(t *testing.T, logPath, sleep string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestKBMatchRunHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "VANI_KBM_HELPER_LOG="+logPath, "VANI_KBM_HELPER_SLEEP="+sleep, "VANI_NO_KEYCHAIN=1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL) })
	return cmd, &out
}

func waitCalls(t *testing.T, path string, n int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for callCount(t, path) < n {
		if time.Now().After(deadline) {
			t.Fatalf("model was called %d times within %v, want %d", callCount(t, path), within, n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Two real processes race `kbmatch run`: the first holds the lock while its
// (slow) model answers; the second waits, then reads what the first kept and
// asks nothing. Exactly 2 model calls in all; both answers persisted.
func TestKBMatchTwoProcessesDoNotDoubleSpend(t *testing.T) {
	k := twoPostings(t)
	logPath := filepath.Join(t.TempDir(), "calls.log")
	c1, o1 := startHelper(t, logPath, "700ms")
	waitCalls(t, logPath, 1, 20*time.Second)
	c2, o2 := startHelper(t, logPath, "0s")
	if err := c1.Wait(); err != nil {
		t.Fatalf("first: %v\n%s", err, o1)
	}
	if err := c2.Wait(); err != nil {
		t.Fatalf("second: %v\n%s", err, o2)
	}
	for i, o := range []*bytes.Buffer{o1, o2} {
		if !strings.Contains(o.String(), "EXIT 0") {
			t.Errorf("process %d did not exit 0:\n%s", i+1, o)
		}
	}
	if n := callCount(t, logPath); n != 2 {
		t.Errorf("the model was called %d times across two processes, want 2\nfirst:\n%s\nsecond:\n%s", n, o1, o2)
	}
	if !strings.Contains(o2.String(), "0 postings to ask about") {
		t.Errorf("the second process should have found both answers kept:\n%s", o2)
	}
	st, err := kbmatch.OpenStore(filepath.Join(k.state, kbmatch.FileName))
	if err != nil || st.Len() != 2 {
		t.Fatalf("store: %v", err)
	}
}

// A run killed with SIGKILL while it holds the lock (and has kept nothing) must
// not wedge the next run: the OS drops a flock with its process.
func TestKBMatchSIGKILLedRunDoesNotWedgeTheNext(t *testing.T) {
	k := twoPostings(t)
	logPath := filepath.Join(t.TempDir(), "calls.log")
	c1, o1 := startHelper(t, logPath, "1m")
	waitCalls(t, logPath, 1, 20*time.Second)
	if _, err := os.Stat(filepath.Join(k.state, kbmatch.FileName+".lock")); err != nil {
		t.Fatalf("the run holds no lock file: %v", err)
	}
	if err := syscall.Kill(c1.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = c1.Wait()
	_ = o1

	f := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "partial"})}
	useFake(t, f)
	old := kbmatchLockWait
	t.Cleanup(func() { kbmatchLockWait = old })
	kbmatchLockWait = 500 * time.Millisecond
	o, e := mustCode(t, 0, "kbmatch", "run")
	if !strings.Contains(o, "asked 2, answered 2") || len(f.contexts) != 2 {
		t.Errorf("next run after SIGKILL: asked %d\n%s\n%s", len(f.contexts), o, e)
	}
}

func listing(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		s := rel
		if !d.IsDir() {
			raw, _ := os.ReadFile(p)
			s += " " + strconv.Itoa(len(raw)) + " " + string(raw)
		}
		out = append(out, s)
		return nil
	})
	sort.Strings(out)
	return out
}

// A dry run changes nothing under the state directory: no store, no lock file,
// no removal of a corrupt store, whatever the store looks like.
func TestKBMatchDryRunLeavesStateByteIdentical(t *testing.T) {
	cases := map[string]func(t *testing.T, path string){
		"no store":  func(*testing.T, string) {},
		"corrupt":   func(t *testing.T, p string) { writeFile(t, p, "{not json") },
		"empty":     func(t *testing.T, p string) { writeFile(t, p, "") },
		"truncated": func(t *testing.T, p string) { writeFile(t, p, `{"version":1,"entries":{"a":`) },
		"directory": func(t *testing.T, p string) {
			if err := os.MkdirAll(filepath.Join(p, "x"), 0o700); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			k := twoPostings(t)
			useFake(t, &fakeKB{answer: byTitle(nil)})
			if err := os.MkdirAll(k.state, 0o700); err != nil {
				t.Fatal(err)
			}
			mk(t, filepath.Join(k.state, kbmatch.FileName))
			before := listing(t, k.state)
			code, _, e := do(t, "kbmatch", "run", "--dry-run")
			if name == "directory" {
				if code == 0 {
					t.Errorf("a directory at the store path should be an error")
				}
			} else if code != 0 {
				t.Fatalf("dry run: exit %d: %s", code, e)
			}
			after := listing(t, k.state)
			if strings.Join(before, "\n") != strings.Join(after, "\n") {
				t.Errorf("a dry run changed the state directory\nbefore: %q\nafter:  %q", before, after)
			}
		})
	}
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A dry run answers while a non-dry run holds the lock mid-run, and the held
// run's store is not disturbed.
func TestKBMatchDryRunWorksWhileARunHoldsTheLock(t *testing.T) {
	k := twoPostings(t)
	f := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "partial"})}
	useFake(t, f)
	oldWait, oldHook := kbmatchLockWait, kbmatchAfterOpen
	t.Cleanup(func() { kbmatchLockWait, kbmatchAfterOpen = oldWait, oldHook })
	kbmatchLockWait = time.Second
	reached, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	kbmatchAfterOpen = func() {
		if first.CompareAndSwap(false, true) {
			close(reached)
			<-release
		}
	}
	done := make(chan int, 1)
	go func() { var o, e bytes.Buffer; done <- run([]string{"kbmatch", "run"}, &o, &e) }()
	<-reached
	start := time.Now()
	code, o, e := do(t, "kbmatch", "run", "--dry-run")
	if code != 0 || !strings.Contains(o, "2 postings to ask about") {
		t.Errorf("dry run while locked: exit %d\n%s\n%s", code, o, e)
	}
	if el := time.Since(start); el > 900*time.Millisecond {
		t.Errorf("dry run waited %v for the lock", el)
	}
	close(release)
	if c := <-done; c != 0 {
		t.Fatalf("held run exit %d", c)
	}
	if st, err := kbmatch.OpenStore(filepath.Join(k.state, kbmatch.FileName)); err != nil || st.Len() != 2 {
		t.Errorf("store after: %v", err)
	}
}

// The lock is held for the whole run: a second non-dry run while the first is
// mid-run (slow model) is told the store is busy after kbmatchLockWait, exits
// nonzero, and does not ask the model. This pins the behaviour, and that the
// wait is bounded.
func TestKBMatchSecondRunWhileSlowRunIsBusyAfterTheWait(t *testing.T) {
	twoPostings(t)
	f := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "partial"})}
	useFake(t, f)
	oldWait, oldHook := kbmatchLockWait, kbmatchAfterOpen
	t.Cleanup(func() { kbmatchLockWait, kbmatchAfterOpen = oldWait, oldHook })
	kbmatchLockWait = 300 * time.Millisecond
	reached, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	var hooks atomic.Int32
	kbmatchAfterOpen = func() {
		hooks.Add(1)
		if first.CompareAndSwap(false, true) {
			close(reached)
			<-release
		}
	}
	done := make(chan int, 1)
	go func() { var o, e bytes.Buffer; done <- run([]string{"kbmatch", "run"}, &o, &e) }()
	<-reached
	start := time.Now()
	code, _, e := do(t, "kbmatch", "run")
	el := time.Since(start)
	if code == 0 || !strings.Contains(e, "another run is using the store") {
		t.Errorf("second run: exit %d %s", code, e)
	}
	if el < 300*time.Millisecond || el > 2*time.Second {
		t.Errorf("second run waited %v, want about 300ms", el)
	}
	if hooks.Load() != 1 {
		t.Errorf("the busy run got past the lock")
	}
	close(release)
	<-done
}

// Lock order: kbmatch's lock is a leaf. While a run holds it, the ledger can be
// appended to (the writer holds the ledger's exclusive lock all along: kbmSetup
// keeps it open), `jobs list` (which reads the store without the lock) answers,
// and a run that has the ledger's lock held by someone else still completes.
// No path takes the kbmatch lock while holding another, so no inversion.
func TestKBMatchLockIsALeafAgainstLedgerAndListing(t *testing.T) {
	twoPostings(t)
	f := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "partial"})}
	useFake(t, f)
	oldWait, oldHook := kbmatchLockWait, kbmatchAfterOpen
	t.Cleanup(func() { kbmatchLockWait, kbmatchAfterOpen = oldWait, oldHook })
	reached, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	kbmatchAfterOpen = func() {
		if first.CompareAndSwap(false, true) {
			close(reached)
			<-release
		}
	}
	done := make(chan int, 1)
	go func() { var o, e bytes.Buffer; done <- run([]string{"kbmatch", "run"}, &o, &e) }()
	<-reached
	listed := make(chan int, 1)
	go func() { var o, e bytes.Buffer; listed <- run([]string{"jobs", "list", "--json"}, &o, &e) }()
	select {
	case c := <-listed:
		if c != 0 {
			t.Errorf("jobs list while a run holds the kbmatch lock: exit %d", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("jobs list blocked behind the kbmatch lock")
	}
	close(release)
	select {
	case c := <-done:
		if c != 0 {
			t.Errorf("run exit %d", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run never finished")
	}
}

// An unreadable (permission-denied) store is not a corrupt one: neither run replaces it.
func TestKBMatchPermissionDeniedStoreIsNeverReplaced(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads anything")
	}
	k := twoPostings(t)
	f := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core"})}
	useFake(t, f)
	if err := os.MkdirAll(k.state, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(k.state, kbmatch.FileName)
	writeFile(t, path, `{"version":1,"entries":{}}`)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	for _, args := range [][]string{{"kbmatch", "run"}, {"kbmatch", "run", "--dry-run"}} {
		if code, _, e := do(t, args...); code == 0 {
			t.Errorf("%v: exit 0 on an unreadable store: %s", args, e)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the unreadable store was removed: %v", err)
	}
	if len(f.contexts) != 0 {
		t.Errorf("asked the model with no readable store")
	}
}
