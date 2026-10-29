package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/daemon"
	"github.com/bharathvbcr/Vani/internal/ledger"
)

// announceDaemon writes daemon.json for this test process, which is alive.
func announceDaemon(t *testing.T, stateDir string) {
	t.Helper()
	if err := writeDaemonRecord(stateDir, os.Getpid(), time.Now()); err != nil {
		t.Fatal(err)
	}
}

// writeRequestFile writes raw as the pending request.
func writeRequestFile(t *testing.T, stateDir string, raw []byte) string {
	t.Helper()
	path := runsPath(stateDir, runRequestFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func requestJSON(t *testing.T, r runRequest) []byte {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func requestExists(stateDir, name string) bool {
	_, err := os.Lstat(runsPath(stateDir, name))
	return err == nil
}

// With no daemon known, or one whose process is gone, nobody would pick a
// request up: run-request refuses, says to use run-once, and writes nothing.
func TestRunRequestRefusedWithoutADaemon(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	_, e := mustCode(t, exitRuntime, "run-request")
	if !strings.Contains(e, "no running vani daemon") || !strings.Contains(e, "run-once") {
		t.Errorf("stderr %q", e)
	}
	o, _ := mustCode(t, exitRuntime, "run-request", "--json")
	var reply struct{ Error string }
	if err := json.Unmarshal([]byte(o), &reply); err != nil || !strings.Contains(reply.Error, "no running vani daemon") {
		t.Errorf("--json reply %q (%v)", o, err)
	}

	if err := writeDaemonRecord(stateDir, 1<<30, time.Now()); err != nil {
		t.Fatal(err)
	}
	old := pidAlive
	t.Cleanup(func() { pidAlive = old })
	pidAlive = func(int) bool { return false }
	_, e = mustCode(t, exitRuntime, "run-request")
	if !strings.Contains(e, "is gone") {
		t.Errorf("a dead daemon: stderr %q", e)
	}
	if err := os.WriteFile(runsPath(stateDir, daemonRecordFile), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustCode(t, exitRuntime, "run-request")
	if requestExists(stateDir, runRequestFile) {
		t.Error("a refused request was written")
	}
	s := statusOf(t)
	if s.Daemon.OK || s.Daemon.Running {
		t.Errorf("a corrupt daemon record reads as %+v", s.Daemon)
	}
}

// Seeing a pid is not enough for run-request to treat it as the daemon.
func TestRunRequestRefusedWhenThePidIsNotThisVani(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	at := time.Now().UTC().Truncate(time.Millisecond)
	if err := writeDaemonRecord(stateDir, 4242, at); err != nil {
		t.Fatal(err)
	}
	oldAlive, oldID := pidAlive, daemonIdentity
	t.Cleanup(func() { pidAlive, daemonIdentity = oldAlive, oldID })
	pidAlive = func(int) bool { return true }
	for _, tc := range []struct {
		name string
		id   procIdentity
	}{
		{"wrong executable", procIdentity{OK: true, Exe: "/usr/bin/not-vani", Started: at.Add(-time.Hour)}},
		{"uninspectable", procIdentity{}},
		{"reused pid", procIdentity{OK: true, Exe: mustExe(t), Started: at.Add(5 * time.Second)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			daemonIdentity = func(int) procIdentity { return tc.id }
			_, e := mustCode(t, exitRuntime, "run-request")
			if !strings.Contains(e, "no running vani daemon") {
				t.Errorf("stderr %q", e)
			}
			if requestExists(stateDir, runRequestFile) {
				t.Error("a refused request was written")
			}
		})
	}
}

func mustExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// A request is written once with its options and time, status reports it as
// pending, and a second request while it waits is refused.
func TestRunRequestIsWrittenOnce(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	addBoards(t)
	announceDaemon(t, stateDir)
	o, _ := mustCode(t, 0, "run-request", "--json", "--advance", "7", "--board", "gh:acme", "--no-digest")
	var reply runRequestReply
	if err := json.Unmarshal([]byte(o), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.RequestedAt.IsZero() || reply.DaemonPID != os.Getpid() || reply.Options.Advance != 7 || !reply.Options.NoDigest || len(reply.Options.Boards) != 1 {
		t.Errorf("reply %+v", reply)
	}
	req, err := readRunRequest(runsPath(stateDir, runRequestFile), time.Now())
	if err != nil || !req.RequestedAt.Equal(reply.RequestedAt) || req.Options.Advance != 7 {
		t.Fatalf("written %+v, %v", req, err)
	}
	if fi, err := os.Stat(runsPath(stateDir, runRequestFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("request file mode: %v %v", fi, err)
	}
	s := statusOf(t)
	if !s.Daemon.OK || !s.Daemon.Running || s.Daemon.PID != os.Getpid() || s.Daemon.Since.IsZero() {
		t.Errorf("daemon facet %+v", s.Daemon)
	}
	rr := s.RunRequest
	if !rr.OK || !rr.Pending || rr.Stale || rr.Claimed || !rr.RequestedAt.Equal(reply.RequestedAt) || rr.Options == nil || rr.Options.Advance != 7 {
		t.Errorf("run_request facet %+v", rr)
	}
	_, e := mustCode(t, exitRuntime, "run-request")
	if !strings.Contains(e, "already waiting") {
		t.Errorf("second request: stderr %q", e)
	}
	if again, _ := readRunRequest(runsPath(stateDir, runRequestFile), time.Now()); again.Options.Advance != 7 {
		t.Errorf("the second request replaced the first: %+v", again)
	}
}

// Under --json a board the watchlist lacks is a reply on stdout with exit 1,
// not a usage exit 2 (which the app reads as "this vani is too old"); without
// --json it stays a usage error, as for run-once.
func TestRunRequestOptionErrorsUnderJSON(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	announceDaemon(t, stateDir)
	o, _ := mustCode(t, exitRuntime, "run-request", "--json", "--board", "gh:nobody")
	if !strings.Contains(o, `"error"`) || !strings.Contains(o, "not on the watchlist") {
		t.Errorf("stdout %q", o)
	}
	mustCode(t, exitUsage, "run-request", "--board", "gh:nobody")
	mustCode(t, exitUsage, "run-request", "--list-only", "--advance", "3")
	mustCode(t, exitUsage, "run-request", "stray")
	if requestExists(stateDir, runRequestFile) {
		t.Error("a refused request was written")
	}
}

// A stale request (no daemon picked it up) and an unreadable one are replaced
// by a new request rather than blocking it.
func TestRunRequestReplacesAStaleOne(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	announceDaemon(t, stateDir)
	writeRequestFile(t, stateDir, requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-2 * time.Hour), Options: runOptions{Advance: 3}}))
	if s := statusOf(t); !s.RunRequest.Pending || !s.RunRequest.Stale {
		t.Errorf("a stale request reads as %+v", s.RunRequest)
	}
	mustCode(t, 0, "run-request", "--list-only")
	if r, err := readRunRequest(runsPath(stateDir, runRequestFile), time.Now()); err != nil || !r.Options.ListOnly || r.Options.Advance != 0 {
		t.Errorf("after replacing: %+v %v", r, err)
	}
	writeRequestFile(t, stateDir, []byte("{broken"))
	mustCode(t, 0, "run-request")
}

// serveFor serves one request with a run that records what it was asked and
// what status said while it ran.
type servedRun struct {
	calls   int
	opts    runOptions
	claimed bool // the claimed file existed while the run went
	pending bool // request.json still existed while the run went
}

func serveOnce(t *testing.T, stateDir string, runErr error) (*servedRun, error) {
	t.Helper()
	got := &servedRun{}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	var mu sync.Mutex
	err := e.serveRunRequest(context.Background(), func(_ context.Context, o runOptions) error {
		got.calls++
		got.opts = o
		got.claimed = requestExists(stateDir, runClaimedFile)
		got.pending = requestExists(stateDir, runRequestFile)
		return runErr
	}, &mu)
	return got, err
}

// The daemon claims a request once: it runs it with its options, status shows
// it claimed (not pending) during the run, and afterwards neither file is left,
// so the next poll runs nothing.
func TestServeRunRequestClaimsOnce(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	announceDaemon(t, stateDir)
	mustCode(t, 0, "run-request", "--advance", "4", "--no-digest")
	got, err := serveOnce(t, stateDir, nil)
	if err != nil || got.calls != 1 || got.opts.Advance != 4 || !got.opts.NoDigest {
		t.Fatalf("served %+v, %v", got, err)
	}
	if !got.claimed || got.pending {
		t.Errorf("during the run: claimed %v pending %v", got.claimed, got.pending)
	}
	if requestExists(stateDir, runRequestFile) || requestExists(stateDir, runClaimedFile) {
		t.Error("a request file outlived its run")
	}
	if again, err := serveOnce(t, stateDir, nil); err != nil || again.calls != 0 {
		t.Errorf("second poll: %+v, %v", again, err)
	}
	// A failed run is the run's error, and the claim is still let go.
	mustCode(t, 0, "run-request")
	boom := errors.New("pipeline: boom")
	if _, err := serveOnce(t, stateDir, boom); !errors.Is(err, boom) {
		t.Errorf("run error %v", err)
	}
	if requestExists(stateDir, runClaimedFile) {
		t.Error("a failed run left its claim")
	}
}

// Status shows a claimed request while its run goes, also past the age a
// pending request goes stale at (a run can take that long).
func TestStatusShowsAClaimedRequest(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	at := time.Now().Add(-90 * time.Minute)
	raw := requestJSON(t, runRequest{V: 1, RequestedAt: at})
	if err := os.MkdirAll(filepath.Join(stateDir, runRecordDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runsPath(stateDir, runClaimedFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	s := statusOf(t)
	if !s.RunRequest.OK || s.RunRequest.Pending || !s.RunRequest.Claimed || !s.RunRequest.ClaimedRequestedAt.Equal(at) {
		t.Errorf("run_request %+v", s.RunRequest)
	}
}

// A request no daemon picked up within the hour is dropped without a run: the
// user asked for a run then, not now.
func TestServeRunRequestDropsAStaleOne(t *testing.T) {
	_, stateDir := setup(t)
	writeRequestFile(t, stateDir, requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-61 * time.Minute)}))
	got, err := serveOnce(t, stateDir, nil)
	if got.calls != 0 || !errors.Is(err, errRequestStale) {
		t.Errorf("served %+v, %v", got, err)
	}
	if requestExists(stateDir, runRequestFile) || requestExists(stateDir, runClaimedFile) {
		t.Error("a stale request was kept")
	}
}

// Request files that are not what run-request writes never run, are removed,
// and never take the daemon down.
func TestServeRunRequestAdversarialFiles(t *testing.T) {
	fresh := time.Now()
	cases := []struct {
		name string
		raw  []byte
	}{
		{"empty", nil},
		{"corrupt", []byte(`{"v":1,"requested_at":`)},
		{"not an object", []byte(`[1,2]`)},
		{"trailing data", append(requestJSON(t, runRequest{V: 1, RequestedAt: fresh}), []byte(` {}`)...)},
		{"unknown key", []byte(`{"v":1,"requested_at":"` + fresh.UTC().Format(time.RFC3339Nano) + `","options":{},"run_twice":true}`)},
		{"unknown option", []byte(`{"v":1,"requested_at":"` + fresh.UTC().Format(time.RFC3339Nano) + `","options":{"advance":3,"boards_all":true}}`)},
		{"wrong version", requestJSON(t, runRequest{V: 2, RequestedAt: fresh})},
		{"no time", requestJSON(t, runRequest{V: 1})},
		{"from the future", requestJSON(t, runRequest{V: 1, RequestedAt: fresh.Add(time.Hour)})},
		{"advance out of bounds", requestJSON(t, runRequest{V: 1, RequestedAt: fresh, Options: runOptions{Advance: config.MaxAdvancePerRun + 1}})},
		{"negative advance", requestJSON(t, runRequest{V: 1, RequestedAt: fresh, Options: runOptions{Advance: -1}})},
		{"list-only with a cap", requestJSON(t, runRequest{V: 1, RequestedAt: fresh, Options: runOptions{Advance: 3, ListOnly: true}})},
		{"too many boards", requestJSON(t, runRequest{V: 1, RequestedAt: fresh, Options: runOptions{Boards: make([]string, maxRunBoards+1)}})},
		{"oversized", append([]byte(`{"v":1,"pad":"`), bytes.Repeat([]byte("x"), maxRunRequest)...)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, stateDir := setup(t)
			writeRequestFile(t, stateDir, c.raw)
			got, err := serveOnce(t, stateDir, nil)
			if got.calls != 0 || err == nil || !strings.Contains(err.Error(), "dropped") {
				t.Errorf("served %+v, %v", got, err)
			}
			if requestExists(stateDir, runRequestFile) || requestExists(stateDir, runClaimedFile) {
				t.Error("the bad request was kept")
			}
		})
	}
}

// A symlink at request.json is never followed: the run is not made, the link
// is removed, and its target is left exactly as it was. Status does not read
// through it either.
func TestServeRunRequestRefusesASymlink(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	body := requestJSON(t, runRequest{V: 1, RequestedAt: time.Now(), Options: runOptions{Advance: 9}})
	if err := os.WriteFile(target, body, 0o600); err != nil {
		t.Fatal(err)
	}
	link := runsPath(stateDir, runRequestFile)
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if s := statusOf(t); s.RunRequest.OK || s.RunRequest.Pending {
		t.Errorf("status read through a symlink: %+v", s.RunRequest)
	}
	got, err := serveOnce(t, stateDir, nil)
	if got.calls != 0 || err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("served %+v, %v", got, err)
	}
	if requestExists(stateDir, runRequestFile) || requestExists(stateDir, runClaimedFile) {
		t.Error("the link was kept")
	}
	if after, err := os.ReadFile(target); err != nil || !bytes.Equal(after, body) {
		t.Errorf("the link's target changed: %q %v", after, err)
	}
	// A directory in its place is refused the same way.
	if err := os.Mkdir(link, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := serveOnce(t, stateDir, nil); got.calls != 0 || err == nil {
		t.Errorf("a directory: %+v, %v", got, err)
	}
}

// A panicking run is contained: the daemon goes on and the claim is let go.
func TestServeRunRequestContainsAPanic(t *testing.T) {
	_, stateDir := setup(t)
	writeRequestFile(t, stateDir, requestJSON(t, runRequest{V: 1, RequestedAt: time.Now()}))
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}
	var mu sync.Mutex
	err := e.serveRunRequest(context.Background(), func(context.Context, runOptions) error { panic("boom") }, &mu)
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Errorf("err %v", err)
	}
	if requestExists(stateDir, runClaimedFile) {
		t.Error("a panicked run left its claim")
	}
}

// The claim waits for the ledger mutex: while the scheduled run holds it the
// request stays pending (status says so), and nothing runs until it is free.
func TestServeRunRequestWaitsForTheLedger(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	writeRequestFile(t, stateDir, requestJSON(t, runRequest{V: 1, RequestedAt: time.Now()}))
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}
	var mu sync.Mutex
	mu.Lock()
	var ran atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- e.serveRunRequest(context.Background(), func(context.Context, runOptions) error { ran.Store(true); return nil }, &mu)
	}()
	time.Sleep(30 * time.Millisecond)
	if ran.Load() {
		t.Fatal("the request ran while the ledger was held")
	}
	if s := statusOf(t); !s.RunRequest.Pending || s.RunRequest.Claimed {
		t.Errorf("while waiting: %+v", s.RunRequest)
	}
	mu.Unlock()
	if err := <-done; err != nil || !ran.Load() {
		t.Errorf("after the ledger was free: ran %v, %v", ran.Load(), err)
	}
}

// A daemon shutting down leaves a waiting request for the next daemon.
func TestServeRunRequestLeavesItOnShutdown(t *testing.T) {
	_, stateDir := setup(t)
	writeRequestFile(t, stateDir, requestJSON(t, runRequest{V: 1, RequestedAt: time.Now()}))
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var mu sync.Mutex
	if err := e.serveRunRequest(ctx, func(context.Context, runOptions) error { t.Error("ran"); return nil }, &mu); err != nil {
		t.Error(err)
	}
	if !requestExists(stateDir, runRequestFile) {
		t.Error("the request was taken by a daemon on its way out")
	}
}

// restartingServe serves the waiting request with a run that does what
// dailyJob does when its reload finds a replaced binary: in a daemon (bound)
// it cancels with errConfigRestart and returns nil; unbound it returns the
// error. before runs inside the run, while the request is claimed.
func restartingServe(t *testing.T, stateDir string, bound bool, before func()) (string, error) {
	t.Helper()
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	if bound {
		e.bindRestart(cancel)
	}
	var mu sync.Mutex
	err := e.serveRunRequest(ctx, func(context.Context, runOptions) error {
		if before != nil {
			before()
		}
		if bound {
			e.requestConfigRestart()
			return nil
		}
		return errConfigRestart
	}, &mu)
	return stderr.String(), err
}

// A run that stops because the daemon must restart (its binary was replaced, a
// pinned setting changed) made no run, so the request goes back as it was and
// the next daemon runs it; the log says so. Unbound (run-once wiring) the
// returned errConfigRestart does the same.
func TestServeRunRequestRequeuesOnRestart(t *testing.T) {
	for _, bound := range []bool{true, false} {
		t.Run(fmt.Sprintf("bound=%v", bound), func(t *testing.T) {
			_, stateDir := setup(t)
			raw := requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-time.Second)})
			writeRequestFile(t, stateDir, raw)
			stderr, err := restartingServe(t, stateDir, bound, nil)
			if err != nil {
				t.Errorf("err %v", err)
			}
			got, rerr := os.ReadFile(runsPath(stateDir, runRequestFile))
			if rerr != nil || !bytes.Equal(got, raw) {
				t.Errorf("the request did not go back as it was: %v\n%s", rerr, got)
			}
			if requestExists(stateDir, runClaimedFile) {
				t.Error("the claim outlived the restart")
			}
			if !strings.Contains(stderr, "waits for the next daemon") {
				t.Errorf("stderr %q", stderr)
			}
		})
	}
}

// A newer request written while the claimed one waited on the restart is the
// owner's latest ask: it stays, and the claimed one is dropped, not run twice.
func TestServeRunRequestRequeueKeepsANewerRequest(t *testing.T) {
	_, stateDir := setup(t)
	writeRequestFile(t, stateDir, requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-time.Minute)}))
	newer := requestJSON(t, runRequest{V: 1, RequestedAt: time.Now()})
	stderr, err := restartingServe(t, stateDir, true, func() { writeRequestFile(t, stateDir, newer) })
	if err != nil {
		t.Errorf("err %v", err)
	}
	if got, rerr := os.ReadFile(runsPath(stateDir, runRequestFile)); rerr != nil || !bytes.Equal(got, newer) {
		t.Errorf("the newer request was not kept: %v\n%s", rerr, got)
	}
	if requestExists(stateDir, runClaimedFile) {
		t.Error("the claim outlived the restart")
	}
	if !strings.Contains(stderr, "newer request") {
		t.Errorf("stderr %q", stderr)
	}
}

// A run that finished before a signal stopped the daemon was made: the request
// is not put back, or the next daemon would make it a second time.
func TestServeRunRequestDoesNotRequeueAFinishedRun(t *testing.T) {
	_, stateDir := setup(t)
	writeRequestFile(t, stateDir, requestJSON(t, runRequest{V: 1, RequestedAt: time.Now()}))
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	err := e.serveRunRequest(ctx, func(context.Context, runOptions) error { cancel(); return nil }, &mu)
	if err != nil || requestExists(stateDir, runRequestFile) || requestExists(stateDir, runClaimedFile) {
		t.Errorf("err %v, pending %v, claimed %v", err, requestExists(stateDir, runRequestFile), requestExists(stateDir, runClaimedFile))
	}
}

// daemonRun is a daemon test's view of the runs its app made.
type daemonRun struct {
	mu        sync.Mutex
	lastTune  int
	busy      atomic.Int32
	overlap   atomic.Int32
	manual    int
	scheduled int
}

// wireRequestDaemon runs the daemon on a jump clock with request polling at a
// few milliseconds. Its app's RunOnce tells a manual run from a scheduled one
// by the advance cap the run was tuned with (the request asks for 7). The
// record clock stays real, so a request is fresh by the wall clock.
func wireRequestDaemon(t *testing.T, l *brokenLedger, onRun func(manual bool, d *daemonRun) error) *daemonRun {
	t.Helper()
	wireDaemon(t, l, nil)
	now = time.Now
	oldPoll := requestPollInterval
	requestPollInterval = 2 * time.Millisecond
	t.Cleanup(func() { requestPollInterval = oldPoll })
	d := &daemonRun{}
	buildApp = func(config.Config, string, io.Writer) (*App, error) {
		return &App{Ledger: l, Notifier: &fakeNotifier{},
			Tune: func(o runOptions) { d.mu.Lock(); d.lastTune = o.Advance; d.mu.Unlock() },
			RunOnce: func(context.Context) error {
				if d.busy.Add(1) > 1 {
					d.overlap.Add(1)
				}
				defer d.busy.Add(-1)
				d.mu.Lock()
				manual := d.lastTune == 7
				if manual {
					d.manual++
				} else {
					d.scheduled++
				}
				d.mu.Unlock()
				time.Sleep(10 * time.Millisecond)
				return onRun(manual, d)
			}}, nil
	}
	return d
}

// The daemon makes a requested run between its scheduled runs, never beside
// one, records it as manual with its options, and leaves no request, claim or
// daemon record behind when it exits.
func TestDaemonServesARequestSerialisedWithTheScheduledRun(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l := &brokenLedger{memLedger: fixture()}
	var sawManualRecord atomic.Bool
	d := wireRequestDaemon(t, l, func(manual bool, d *daemonRun) error {
		if manual {
			if r, err := readRunRecord(stateDir); err == nil && r.Trigger == triggerManual && r.Options != nil && r.Options.Advance == 7 && r.PID == os.Getpid() {
				sawManualRecord.Store(true)
			}
			return nil
		}
		d.mu.Lock()
		scheduled, manualRuns := d.scheduled, d.manual
		d.mu.Unlock()
		if scheduled == 1 {
			// Asked for while the scheduled run holds the ledger.
			mustCode(t, 0, "run-request", "--advance", "7", "--no-digest")
		}
		if manualRuns > 0 && scheduled > 2 {
			l.poison(errors.New("stop the daemon"))
			return errors.New("stop")
		}
		if scheduled > 500 {
			l.poison(errors.New("the request was never served"))
			return errors.New("stop")
		}
		return nil
	})
	_, stderr := mustCode(t, exitRuntime, "daemon")
	if d.manual != 1 {
		t.Fatalf("%d manual runs, want 1\n%s", d.manual, stderr)
	}
	if d.overlap.Load() != 0 {
		t.Errorf("%d runs overlapped", d.overlap.Load())
	}
	if !sawManualRecord.Load() {
		t.Error("the manual run was not recorded as manual, by the daemon, with its options")
	}
	for _, name := range []string{runRequestFile, runClaimedFile, daemonRecordFile} {
		if requestExists(stateDir, name) {
			t.Errorf("%s outlived the daemon", name)
		}
	}
}

// A requested run that leaves the ledger unusable ends the daemon non-zero, as
// a scheduled one does, rather than leaving it to fail every later run.
func TestFatalRequestedRunEndsTheDaemon(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l := &brokenLedger{memLedger: fixture()}
	var asked atomic.Bool
	d := wireRequestDaemon(t, l, func(manual bool, d *daemonRun) error {
		if manual {
			// Fatal by the error alone: the ledger reads healthy, so no later
			// scheduled run would end the daemon; only the request loop can.
			return fmt.Errorf("%w: ledger: fsync failed: input/output error", daemon.ErrFatal)
		}
		if asked.CompareAndSwap(false, true) {
			mustCode(t, 0, "run-request", "--advance", "7")
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.scheduled > 300 {
			return fmt.Errorf("%w: the daemon outlived its fatal manual run", daemon.ErrFatal)
		}
		return nil
	})
	_, stderr := mustCode(t, exitRuntime, "daemon")
	if d.manual != 1 || !strings.Contains(stderr, "fsync failed: input/output error") || strings.Contains(stderr, "outlived") {
		t.Errorf("manual runs %d, stderr:\n%s", d.manual, stderr)
	}
	if requestExists(stateDir, daemonRecordFile) {
		t.Error("the daemon record outlived the daemon")
	}
}

// A daemon starting after one that died mid-run drops that run's claim rather
// than make the run again unasked.
func TestDaemonStartDropsAnEarlierClaim(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	if err := os.MkdirAll(filepath.Join(stateDir, runRecordDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runsPath(stateDir, runClaimedFile), requestJSON(t, runRequest{V: 1, RequestedAt: time.Now()}), 0o600); err != nil {
		t.Fatal(err)
	}
	l := &brokenLedger{memLedger: fixture()}
	d := wireRequestDaemon(t, l, func(bool, *daemonRun) error {
		l.poison(errors.New("stop the daemon"))
		return errors.New("stop")
	})
	_, stderr := mustCode(t, exitRuntime, "daemon")
	if d.manual != 0 || requestExists(stateDir, runClaimedFile) || !strings.Contains(stderr, "not run again") {
		t.Errorf("manual %d, stderr:\n%s", d.manual, stderr)
	}
}

// The daemon record names the daemon while it runs and goes when it exits; a
// record another daemon wrote since is left alone.
func TestDaemonRecord(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l := &brokenLedger{memLedger: fixture()}
	var during daemonFacet
	wireDaemon(t, l, func(context.Context) error {
		during = statusOf(t).Daemon
		l.poison(errors.New("stop the daemon"))
		return errors.New("stop")
	})
	mustCode(t, exitRuntime, "daemon")
	if !during.OK || !during.Running || during.PID != os.Getpid() {
		t.Errorf("while running: %+v", during)
	}
	if s := statusOf(t); s.Daemon.Running || !s.Daemon.OK {
		t.Errorf("after exit: %+v", s.Daemon)
	}

	if err := writeDaemonRecord(stateDir, os.Getpid()+1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := removeDaemonRecord(stateDir, os.Getpid()); err != nil || !requestExists(stateDir, daemonRecordFile) {
		t.Errorf("another daemon's record: %v", err)
	}
}

// run-once refused by a running daemon points at run-request.
func TestRunOnceRefusedByTheDaemonPointsAtRunRequest(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	announceDaemon(t, stateDir)
	old := buildApp
	t.Cleanup(func() { buildApp = old })
	buildApp = func(config.Config, string, io.Writer) (*App, error) { return nil, errLockedForTest }
	_, e := mustCode(t, exitRuntime, "run-once")
	if !strings.Contains(e, "run-request") {
		t.Errorf("stderr %q", e)
	}
}

// pollRequestLoop returns a fatal run error and reports the others.
func TestPollRequestLoopStopsOnlyOnFatal(t *testing.T) {
	_, stateDir := setup(t)
	old := requestPollInterval
	requestPollInterval = time.Millisecond
	t.Cleanup(func() { requestPollInterval = old })
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	var mu sync.Mutex
	runs := 0
	run := func(context.Context, runOptions) error {
		runs++
		if runs == 1 {
			return errors.New("an ordinary failure")
		}
		return daemonFatal
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.pollRequestLoop(ctx, run, &mu) }()
	for i := 0; i < 2; i++ {
		for requestExists(stateDir, runRequestFile) || requestExists(stateDir, runClaimedFile) {
			time.Sleep(time.Millisecond)
		}
		mu.Lock()
		writeRequestFile(t, stateDir, requestJSON(t, runRequest{V: 1, RequestedAt: time.Now()}))
		mu.Unlock()
	}
	if err := <-done; !errors.Is(err, daemonFatal) || runs != 2 {
		t.Errorf("loop ended with %v after %d runs", err, runs)
	}
	if !strings.Contains(stderr.String(), "an ordinary failure") {
		t.Errorf("stderr %q", stderr.String())
	}
}

var (
	daemonFatal      = errors.Join(daemon.ErrFatal, errors.New("ledger poisoned"))
	errLockedForTest = ledger.ErrLocked
)

// requestBody is the bytes requestRun writes for at and opts.
func requestBody(t *testing.T, at time.Time, opts runOptions) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(runRequest{V: runRequestVersion, RequestedAt: at, Options: opts}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Two callers that both see a missing request must not both succeed: the
// later file would be the one the daemon runs, and the first caller would
// have been told its request is waiting.
func TestRequestRunRaceExactlyOneSucceeds(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	announceDaemon(t, stateDir)
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	oldNow := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = oldNow })

	optsA := runOptions{Advance: 1}
	optsB := runOptions{}
	bodyA := requestBody(t, at, optsA)
	bodyB := requestBody(t, at, optsB)

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var once sync.Once
	letGo := func() { once.Do(func() { close(release) }) }
	oldGate := requestWriteGate
	requestWriteGate = func() {
		entered <- struct{}{}
		<-release
	}
	type outcome struct {
		err error
	}
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	start := func(opts runOptions) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := &env{configPath: cfgPath, stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}
			_, err := e.requestRun(opts)
			results <- outcome{err}
		}()
	}
	start(optsA)
	start(optsB)
	// Release first, then wait, then drop the gate. Cleanup runs last-added first.
	t.Cleanup(func() { requestWriteGate = oldGate })
	t.Cleanup(func() { wg.Wait() })
	t.Cleanup(letGo)

	timeout := time.After(3 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-timeout:
			t.Fatal("the check did not run")
		}
	}
	letGo()

	var errs [2]error
	for i := 0; i < 2; i++ {
		select {
		case r := <-results:
			errs[i] = r.err
		case <-time.After(3 * time.Second):
			t.Fatal("requestRun did not return")
		}
	}
	success, waiting := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			success++
		case strings.Contains(err.Error(), "already waiting"):
			waiting++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if success != 1 || waiting != 1 {
		t.Fatalf("want one success and one already-waiting error, got %d success and %d already-waiting: %v", success, waiting, errs)
	}
	path := runsPath(stateDir, runRequestFile)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, bodyA) && !bytes.Equal(got, bodyB) {
		t.Fatalf("request file is neither caller:\n%s", got)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("request file mode %v, want a 0600 regular file", fi.Mode())
	}
}

// A corrupt request is still replaced by the one caller, with the warning.
func TestRequestRunReplacesACorruptFile(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	announceDaemon(t, stateDir)
	writeRequestFile(t, stateDir, []byte("{broken"))
	var stderr bytes.Buffer
	e := &env{configPath: cfgPath, stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	opts := runOptions{Advance: 4}
	reply, err := e.requestRun(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "replacing the waiting run request") {
		t.Errorf("stderr %q", stderr.String())
	}
	raw, err := os.ReadFile(runsPath(stateDir, runRequestFile))
	if err != nil {
		t.Fatal(err)
	}
	var got runRequest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.V != runRequestVersion || !got.RequestedAt.Equal(reply.RequestedAt) || got.Options.Advance != opts.Advance || got.Options.ListOnly || len(got.Options.Boards) != 0 {
		t.Fatalf("file %+v, reply %+v", got, reply)
	}
}

// A symlink at the request path is replaced by the request file. The write
// does not follow the link, so the outside target is unchanged.
func TestRequestRunReplacesASymlinkWithoutFollowingIt(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	announceDaemon(t, stateDir)
	outside := filepath.Join(t.TempDir(), "outside.json")
	outsideBody := []byte("outside-bytes-not-a-request")
	if err := os.WriteFile(outside, outsideBody, 0o600); err != nil {
		t.Fatal(err)
	}
	link := runsPath(stateDir, runRequestFile)
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	e := &env{configPath: cfgPath, stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}
	opts := runOptions{ListOnly: true}
	reply, err := e.requestRun(opts)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		t.Fatalf("request path mode %v, want a regular file", fi.Mode())
	}
	after, err := os.ReadFile(outside)
	if err != nil || !bytes.Equal(after, outsideBody) {
		t.Fatalf("outside file changed: %q %v", after, err)
	}
	var got runRequest
	raw, err := os.ReadFile(link)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Options.ListOnly || got.Options.Advance != 0 || !got.RequestedAt.Equal(reply.RequestedAt) {
		t.Fatalf("file %+v, reply %+v", got, reply)
	}
}
