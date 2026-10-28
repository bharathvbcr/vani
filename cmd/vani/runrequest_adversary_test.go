package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/filelock"
)

// requestRunResult is one racer's requestRun outcome. stderr is that caller's
// alone, so a loser's warning cannot be mistaken for the winner's.
type requestRunResult struct {
	opts   runOptions
	reply  runRequestReply
	err    error
	stderr string
}

// pinNow fixes requestRun's clock for the rest of the test.
func pinNow(t *testing.T, at time.Time) {
	t.Helper()
	old := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = old })
}

// raceRequestRuns starts every caller, holds them in requestWriteGate until
// each has passed the unlocked read, then lets them contend for the lock.
// The gate is restored the way TestRequestRunRaceExactlyOneSucceeds restores
// it: release waiters, wait for them, then put the previous gate back.
func raceRequestRuns(t *testing.T, cfgPath, stateDir string, callers []runOptions) []requestRunResult {
	t.Helper()
	n := len(callers)
	if n < 2 {
		t.Fatal("race needs at least two callers")
	}
	if requestWriteGate != nil {
		t.Fatal("requestWriteGate leaked into this test")
	}
	entered := make(chan struct{}, n)
	release := make(chan struct{})
	var once sync.Once
	letGo := func() { once.Do(func() { close(release) }) }
	oldGate := requestWriteGate
	requestWriteGate = func() {
		entered <- struct{}{}
		<-release
	}
	var wg sync.WaitGroup
	// Last registered runs first: unblock, then wait, then restore the gate.
	t.Cleanup(func() { requestWriteGate = oldGate })
	t.Cleanup(func() { wg.Wait() })
	t.Cleanup(letGo)

	results := make(chan requestRunResult, n)
	for _, opts := range callers {
		wg.Add(1)
		go func(opts runOptions) {
			defer wg.Done()
			var stderr bytes.Buffer
			e := &env{configPath: cfgPath, stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
			reply, err := e.requestRun(opts)
			results <- requestRunResult{opts: opts, reply: reply, err: err, stderr: stderr.String()}
		}(opts)
	}
	timeout := time.After(5 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case <-entered:
		case <-timeout:
			t.Fatal("a caller did not reach requestWriteGate")
		}
	}
	letGo()
	out := make([]requestRunResult, 0, n)
	for i := 0; i < n; i++ {
		select {
		case r := <-results:
			out = append(out, r)
		case <-time.After(5 * time.Second):
			t.Fatal("requestRun did not return")
		}
	}
	return out
}

// exactlyOneWinner requires one nil error, already-waiting for every other
// caller, and on-disk bytes equal to the winner's body — not a loser's, and
// not a truncation of the winner's.
func exactlyOneWinner(t *testing.T, stateDir string, at time.Time, results []requestRunResult) requestRunResult {
	t.Helper()
	var winner requestRunResult
	success, waiting := 0, 0
	for _, r := range results {
		switch {
		case r.err == nil:
			success++
			winner = r
		case strings.Contains(r.err.Error(), "already waiting"):
			waiting++
		default:
			t.Fatalf("unexpected error: %v", r.err)
		}
	}
	if success != 1 || waiting != len(results)-1 {
		t.Fatalf("want exactly one success and %d already-waiting, got %d success and %d already-waiting", len(results)-1, success, waiting)
	}
	path := runsPath(stateDir, runRequestFile)
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("request file mode %v, want a 0600 regular file", fi.Mode())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := requestBody(t, at, winner.opts)
	if !bytes.Equal(got, want) {
		t.Fatalf("request file is not the winner's bytes (a loser replaced or truncated it)\n got: %s\nwant: %s", got, want)
	}
	for _, r := range results {
		if r.err == nil {
			continue
		}
		loser := requestBody(t, at, r.opts)
		if bytes.Equal(loser, want) {
			t.Fatal("callers wrote identical bytes; the race cannot tell who won")
		}
		if bytes.Equal(got, loser) {
			t.Fatalf("request file is a loser's bytes:\n%s", got)
		}
	}
	var decoded runRequest
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.RequestedAt.Equal(at) || !optionsMatch(decoded.Options, winner.opts) {
		t.Fatalf("file unmarshals as %+v, winner opts %+v at %s", decoded, winner.opts, at)
	}
	if !winner.reply.RequestedAt.Equal(at) || !optionsMatch(winner.reply.Options, winner.opts) || winner.reply.DaemonPID != os.Getpid() {
		t.Fatalf("winner reply %+v, want opts %+v at %s pid %d", winner.reply, winner.opts, at, os.Getpid())
	}
	// Returned callers must not still hold request.json.lock.
	lk, err := filelock.TryAcquire(path + ".lock")
	if err != nil {
		t.Fatalf("lock still held after requestRun returned: %v", err)
	}
	if err := lk.Release(); err != nil {
		t.Fatal(err)
	}
	return winner
}

func modeOf(fi os.FileInfo) os.FileMode {
	if fi == nil {
		return 0
	}
	return fi.Mode()
}

func optionsMatch(got, want runOptions) bool {
	return got.Advance == want.Advance && got.ListOnly == want.ListOnly && got.NoDigest == want.NoDigest && len(got.Boards) == len(want.Boards)
}

// Three callers, not two. Exactly one writes; the file is that caller's bytes.
func TestRequestRunThreeCallersExactlyOneWins(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	announceDaemon(t, stateDir)
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	pinNow(t, at)
	exactlyOneWinner(t, stateDir, at, raceRequestRuns(t, cfgPath, stateDir, []runOptions{
		{Advance: 1},
		{ListOnly: true},
		{NoDigest: true},
	}))
}

// A corrupt file is replaced by exactly one of two racers. The other must not
// truncate or replace those bytes. The winner's own stderr still says why.
func TestRequestRunCorruptFileTwoRacersKeepTheWinner(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	announceDaemon(t, stateDir)
	writeRequestFile(t, stateDir, []byte("{broken"))
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	pinNow(t, at)
	results := raceRequestRuns(t, cfgPath, stateDir, []runOptions{
		{Advance: 7},
		{NoDigest: true},
	})
	winner := exactlyOneWinner(t, stateDir, at, results)
	if !strings.Contains(winner.stderr, "replacing the waiting run request") {
		t.Fatalf("winner stderr %q, want the replace warning", winner.stderr)
	}
	got, err := os.ReadFile(runsPath(stateDir, runRequestFile))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(got, []byte("{broken")) {
		t.Fatal("corrupt bytes still on disk")
	}
}

// Two callers and a symlink at the request path: one success, a 0600 regular
// file, and the outside target unchanged. The write must not follow the link.
func TestRequestRunSymlinkTwoCallersDoNotFollow(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	announceDaemon(t, stateDir)
	outside := filepath.Join(t.TempDir(), "outside.json")
	outsideBody := []byte("outside-bytes-not-a-request")
	if err := os.WriteFile(outside, outsideBody, 0o600); err != nil {
		t.Fatal(err)
	}
	link := runsPath(stateDir, runRequestFile)
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	pinNow(t, at)
	exactlyOneWinner(t, stateDir, at, raceRequestRuns(t, cfgPath, stateDir, []runOptions{
		{ListOnly: true},
		{Advance: 3},
	}))
	after, err := os.ReadFile(outside)
	if err != nil || !bytes.Equal(after, outsideBody) {
		t.Fatalf("outside file changed: %q %v", after, err)
	}
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		t.Fatalf("request path after symlink race: mode %v err %v", modeOf(fi), err)
	}
}

// A dangling symlink is replaced. Nothing is created at the target, including
// when a second caller races the replacement.
func TestRequestRunDanglingSymlinkIsReplaced(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	announceDaemon(t, stateDir)
	target := filepath.Join(t.TempDir(), "missing-parent", "outside.json")
	link := runsPath(stateDir, runRequestFile)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	pinNow(t, at)
	exactlyOneWinner(t, stateDir, at, raceRequestRuns(t, cfgPath, stateDir, []runOptions{
		{Advance: 4},
		{NoDigest: true},
	}))
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dangling target was created: %v", err)
	}
	if _, err := os.Lstat(filepath.Dir(target)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dangling target's parent was created: %v", err)
	}
}

// A lock another Acquire already holds past the 5s bound is an error.
// requestRun must not claim success and must not leave a request file behind.
// The gate runs before that wait. The holder's lock is released afterwards.
func TestRequestRunHeldLockIsNotSuccess(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	announceDaemon(t, stateDir)
	if requestWriteGate != nil {
		t.Fatal("requestWriteGate leaked into this test")
	}
	lockPath := runsPath(stateDir, runRequestFile) + ".lock"
	held, err := filelock.Acquire(context.Background(), lockPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Release() })

	entered := make(chan struct{}, 1)
	oldGate := requestWriteGate
	t.Cleanup(func() { requestWriteGate = oldGate })
	requestWriteGate = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
	}

	var reply runRequestReply
	var runErr error
	done := make(chan struct{})
	start := time.Now()
	go func() {
		reply, runErr = (&env{configPath: cfgPath, stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}).requestRun(runOptions{Advance: 1})
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("requestWriteGate did not run before the lock")
	}
	select {
	case <-done:
	case <-time.After(7 * time.Second):
		t.Fatal("requestRun did not return within the 5s lock bound")
	}
	if elapsed := time.Since(start); elapsed > 7*time.Second {
		t.Fatalf("requestRun took %s, bound is 5s", elapsed)
	}
	if runErr == nil || reply.DaemonPID != 0 || !reply.RequestedAt.IsZero() {
		t.Fatalf("held lock claimed success: reply %+v err %v", reply, runErr)
	}
	if !errors.Is(runErr, filelock.ErrWouldBlock) {
		t.Fatalf("held lock: %v, want ErrWouldBlock", runErr)
	}
	if strings.Contains(runErr.Error(), "already waiting") {
		t.Fatalf("held lock reported a waiting request: %v", runErr)
	}
	if _, err := os.Lstat(runsPath(stateDir, runRequestFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock failure left a request file: %v", err)
	}
	if _, err := filelock.TryAcquire(lockPath); !errors.Is(err, filelock.ErrWouldBlock) {
		t.Fatalf("requestRun took the lock the holder still owns: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	restored, err := filelock.TryAcquire(lockPath)
	if err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	if err := restored.Release(); err != nil {
		t.Fatal(err)
	}
}

// An empty state dir has no daemon and no request. requestRun takes no
// context; this path must fail without waiting out the 5s lock bound, and
// must not create a request file.
func TestRequestRunEmptyStateDirFailsWithoutWaiting(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	if requestWriteGate != nil {
		t.Fatal("requestWriteGate leaked into this test")
	}
	empty := filepath.Join(t.TempDir(), "empty-state")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	e := &env{configPath: cfgPath, stateDir: empty, stdout: io.Discard, stderr: io.Discard}
	start := time.Now()
	reply, err := e.requestRun(runOptions{})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("empty state dir took %s; the 5s bound is the lock, and there is nothing to lock", elapsed)
	}
	if err == nil || reply.DaemonPID != 0 || !errors.Is(err, errNoDaemon) {
		t.Fatalf("empty state dir: reply %+v err %v", reply, err)
	}
	entries, rerr := os.ReadDir(empty)
	if rerr != nil || len(entries) != 0 {
		t.Fatalf("empty state dir gained %v (%v)", entries, rerr)
	}
}

// The state directory's name is not ASCII. The request still lands in it,
// and two callers still leave exactly the winner's bytes.
func TestRequestRunUnicodeStateDir(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	stateDir := filepath.Join(t.TempDir(), "état-状态")
	announceDaemon(t, stateDir)
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	pinNow(t, at)
	exactlyOneWinner(t, stateDir, at, raceRequestRuns(t, cfgPath, stateDir, []runOptions{
		{Advance: 8},
		{ListOnly: true},
	}))
	rel, err := filepath.Rel(stateDir, runsPath(stateDir, runRequestFile))
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("request file %s is not inside %s", runsPath(stateDir, runRequestFile), stateDir)
	}
}
