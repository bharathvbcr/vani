package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
)

// A request a newer vani wrote, with a field this binary's strict decode
// refuses, is still valid. It is put back and the daemon restarts. It is not
// deleted as corrupt, and it is not run.
func TestUnknownFieldIsRequeuedNotDropped(t *testing.T) {
	_, stateDir := setup(t)
	at := time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	raw := []byte(`{"v":1,"requested_at":"` + at + `","options":{"advance":7},"later":true}`)
	writeRequestFile(t, stateDir, raw)
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	ctx, cancel := context.WithCancelCause(context.Background())
	e.bindRestart(cancel)
	calls := 0
	var mu sync.Mutex
	err := e.serveRunRequest(ctx, func(context.Context, runOptions) error { calls++; return nil }, &mu)
	if err != nil {
		t.Fatalf("err %v\n%s", err, stderr.String())
	}
	if calls != 0 {
		t.Fatalf("ran %d times", calls)
	}
	if !errors.Is(context.Cause(ctx), errConfigRestart) {
		t.Fatalf("cause %v, want errConfigRestart", context.Cause(ctx))
	}
	got, rerr := os.ReadFile(runsPath(stateDir, runRequestFile))
	if rerr != nil || !bytes.Equal(got, raw) {
		t.Fatalf("request was not put back: %v\n%s", rerr, got)
	}
	if requestExists(stateDir, runClaimedFile) {
		t.Fatal("the claim outlived the restart")
	}
}

// Malformed JSON is corrupt. The claim is dropped and nothing is run.
func TestCorruptRequestIsStillDropped(t *testing.T) {
	_, stateDir := setup(t)
	writeRequestFile(t, stateDir, []byte(`{"v":1,"requested_at":`))
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}
	calls := 0
	var mu sync.Mutex
	err := e.serveRunRequest(context.Background(), func(context.Context, runOptions) error { calls++; return nil }, &mu)
	if err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("err %v, want a corrupt drop", err)
	}
	if calls != 0 || requestExists(stateDir, runRequestFile) || requestExists(stateDir, runClaimedFile) {
		t.Fatalf("calls %d pending %v claimed %v", calls, requestExists(stateDir, runRequestFile), requestExists(stateDir, runClaimedFile))
	}
}

// An unknown field on a request that is also stale is not "still valid".
func TestStaleUnknownFieldIsDropped(t *testing.T) {
	_, stateDir := setup(t)
	at := time.Now().Add(-runRequestMaxAge - time.Hour).UTC().Format(time.RFC3339Nano)
	writeRequestFile(t, stateDir, []byte(`{"v":1,"requested_at":"`+at+`","later":true}`))
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}
	var mu sync.Mutex
	err := e.serveRunRequest(context.Background(), func(context.Context, runOptions) error { t.Error("ran"); return nil }, &mu)
	if err == nil {
		t.Fatal("stale unknown-field request was kept")
	}
	if requestExists(stateDir, runRequestFile) || requestExists(stateDir, runClaimedFile) {
		t.Fatal("stale request was put back")
	}
}

// A signal cancels first. dailyJob then sees a config restart and returns nil,
// because the context is already canceled. The claim is still put back: the
// run did not finish.
func TestSignalCancelThenConfigRestartRequeues(t *testing.T) {
	_, stateDir := setup(t)
	raw := requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-time.Second), Options: runOptions{Advance: 7}})
	writeRequestFile(t, stateDir, raw)
	// The binary still matches at the claim. It is replaced only after the signal
	// has already cancelled, which is when dailyJob's reload sees it.
	exe := &fakeExe{id: oldImage}
	exe.install(t, nil)
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr, binary: &binWatch{path: fakeExePath, start: oldImage}}
	ctx, cancel := context.WithCancelCause(context.Background())
	e.bindRestart(cancel)
	var mu sync.Mutex
	err := e.serveRunRequest(ctx, func(ctx context.Context, opts runOptions) error {
		cancel(context.Canceled)
		exe.set(newImage, nil)
		return e.dailyJob(&App{}, config.Config{}, triggerManual, opts)(ctx)
	}, &mu)
	if err != nil {
		t.Fatalf("err %v\n%s", err, stderr.String())
	}
	if !errors.Is(context.Cause(ctx), context.Canceled) {
		t.Fatalf("cause %v, want the signal", context.Cause(ctx))
	}
	got, rerr := os.ReadFile(runsPath(stateDir, runRequestFile))
	if rerr != nil || !bytes.Equal(got, raw) {
		t.Fatalf("request was not put back: %v\n%s", rerr, got)
	}
	if requestExists(stateDir, runClaimedFile) {
		t.Fatal("the claim outlived the restart")
	}
}

// A finished run that a signal then stops is not put back.
func TestFinishedRunIsNotRequeuedWhenTheSignalComesAfter(t *testing.T) {
	_, stateDir := setup(t)
	writeRequestFile(t, stateDir, requestJSON(t, runRequest{V: 1, RequestedAt: time.Now()}))
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	err := e.serveRunRequest(ctx, func(context.Context, runOptions) error {
		cancel()
		return nil
	}, &mu)
	if err != nil || requestExists(stateDir, runRequestFile) || requestExists(stateDir, runClaimedFile) {
		t.Fatalf("err %v pending %v claimed %v", err, requestExists(stateDir, runRequestFile), requestExists(stateDir, runClaimedFile))
	}
}

// replaced() is false for a missing path. The request stays unclaimed and is
// not run.
func TestMissingBinaryBeforeClaimLeavesTheRequest(t *testing.T) {
	_, stateDir := setup(t)
	raw := requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-time.Second)})
	writeRequestFile(t, stateDir, raw)
	exe := &fakeExe{}
	exe.install(t, nil)
	exe.set(binIdentity{}, os.ErrNotExist)
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr, binary: &binWatch{path: fakeExePath, start: oldImage}}
	ctx, cancel := context.WithCancelCause(context.Background())
	e.bindRestart(cancel)
	calls := 0
	var mu sync.Mutex
	err := e.serveRunRequest(ctx, func(context.Context, runOptions) error { calls++; return nil }, &mu)
	if err != nil || calls != 0 {
		t.Fatalf("err %v calls %d\n%s", err, calls, stderr.String())
	}
	if !errors.Is(context.Cause(ctx), errConfigRestart) {
		t.Fatalf("cause %v", context.Cause(ctx))
	}
	got, rerr := os.ReadFile(runsPath(stateDir, runRequestFile))
	if rerr != nil || !bytes.Equal(got, raw) || requestExists(stateDir, runClaimedFile) {
		t.Fatalf("pending %v %s claimed %v", rerr, got, requestExists(stateDir, runClaimedFile))
	}
}

// The binary disappears after replaced() has already returned false and the
// request has been claimed. The claim is put back, not deleted.
func TestBinaryDisappearsAfterClaimIsRequeued(t *testing.T) {
	_, stateDir := setup(t)
	raw := requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-time.Second)})
	writeRequestFile(t, stateDir, raw)
	exe := &fakeExe{id: oldImage}
	exe.install(t, nil)
	n := 0
	statExecutable = func(path string) (binIdentity, error) {
		if path != fakeExePath {
			t.Errorf("stat %q", path)
		}
		n++
		// replaced(), then the pre-claim missing check, then the post-claim check.
		if n >= 3 {
			return binIdentity{}, os.ErrNotExist
		}
		return oldImage, nil
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr, binary: &binWatch{path: fakeExePath, start: oldImage}}
	ctx, cancel := context.WithCancelCause(context.Background())
	e.bindRestart(cancel)
	calls := 0
	var mu sync.Mutex
	err := e.serveRunRequest(ctx, func(context.Context, runOptions) error { calls++; return nil }, &mu)
	if err != nil || calls != 0 {
		t.Fatalf("err %v calls %d\n%s", err, calls, stderr.String())
	}
	got, rerr := os.ReadFile(runsPath(stateDir, runRequestFile))
	if rerr != nil || !bytes.Equal(got, raw) || requestExists(stateDir, runClaimedFile) {
		t.Fatalf("pending %v claimed %v\n%s", rerr, requestExists(stateDir, runClaimedFile), stderr.String())
	}
}

// A link error other than ErrExist must not delete the only copy. Rename puts
// it back as the pending request.
func TestLinkFailureRenamesTheClaimBack(t *testing.T) {
	_, stateDir := setup(t)
	raw := requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-time.Second)})
	writeRequestFile(t, stateDir, raw)
	oldLink := linkRequest
	t.Cleanup(func() { linkRequest = oldLink })
	linkRequest = func(string, string) error {
		return &os.PathError{Op: "link", Path: runsPath(stateDir, runClaimedFile), Err: os.ErrPermission}
	}
	stderr, err := restartingServe(t, stateDir, true, nil)
	if err != nil {
		t.Fatalf("err %v\n%s", err, stderr)
	}
	got, rerr := os.ReadFile(runsPath(stateDir, runRequestFile))
	if rerr != nil || !bytes.Equal(got, raw) {
		t.Fatalf("request was not put back: %v\n%s", rerr, got)
	}
	if requestExists(stateDir, runClaimedFile) {
		t.Fatal("the claim outlived the rename")
	}
	if strings.Contains(stderr, "dropped") {
		t.Fatalf("stderr calls this a drop: %s", stderr)
	}
}

// When link and rename both fail, the deferred remove must leave the claimed
// file. The error is not a drop.
func TestLinkAndRenameFailureKeepsTheClaim(t *testing.T) {
	_, stateDir := setup(t)
	raw := requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-time.Second)})
	writeRequestFile(t, stateDir, raw)
	oldLink, oldRename := linkRequest, renameRequest
	t.Cleanup(func() { linkRequest, renameRequest = oldLink, oldRename })
	linkRequest = func(string, string) error {
		return &os.PathError{Op: "link", Err: errors.ErrUnsupported}
	}
	renameRequest = func(string, string) error {
		return &os.PathError{Op: "rename", Err: os.ErrPermission}
	}
	stderr, err := restartingServe(t, stateDir, true, nil)
	if err == nil || strings.Contains(err.Error(), "dropped") {
		t.Fatalf("err %v, want the claim kept\n%s", err, stderr)
	}
	if !errors.Is(err, errClaimKept) {
		t.Fatalf("err %v, want errClaimKept", err)
	}
	if requestExists(stateDir, runRequestFile) || !requestExists(stateDir, runClaimedFile) {
		t.Fatalf("pending %v claimed %v", requestExists(stateDir, runRequestFile), requestExists(stateDir, runClaimedFile))
	}
	got, rerr := os.ReadFile(runsPath(stateDir, runClaimedFile))
	if rerr != nil || !bytes.Equal(got, raw) {
		t.Fatalf("claim bytes %v\n%s", rerr, got)
	}
}

// An unknown field does not rescue a request that fails a bound this binary
// already enforces. Those are still dropped.
func TestUnknownFieldThatFailsABoundIsDropped(t *testing.T) {
	nowStamp := time.Now().UTC().Format(time.RFC3339Nano)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	boards := make([]string, maxRunBoards+1)
	for i := range boards {
		boards[i] = strings.Repeat("b", i%3) + string(rune('a'+(i%26)))
	}
	rawBoards, err := json.Marshal(boards)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		raw  string
	}{
		{"future", `{"v":1,"requested_at":"` + future + `","later":true}`},
		{"bad advance", `{"v":1,"requested_at":"` + nowStamp + `","options":{"advance":` + strconv.Itoa(config.MaxAdvancePerRun+1) + `},"later":true}`},
		{"too many boards", `{"v":1,"requested_at":"` + nowStamp + `","options":{"boards":` + string(rawBoards) + `},"later":true}`},
		{"trailing", `{"v":1,"requested_at":"` + nowStamp + `","later":true}{}`},
		{"empty", ``},
		{"not an object", `[]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, stateDir := setup(t)
			writeRequestFile(t, stateDir, []byte(c.raw))
			var mu sync.Mutex
			err := (&env{stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}).serveRunRequest(
				context.Background(), func(context.Context, runOptions) error { t.Error("ran"); return nil }, &mu)
			if err == nil || !strings.Contains(err.Error(), "dropped") {
				t.Fatalf("err %v", err)
			}
			if requestExists(stateDir, runRequestFile) || requestExists(stateDir, runClaimedFile) {
				t.Fatal("the request was kept")
			}
		})
	}
}

// ErrExist keeps the newer pending file and does not try to rename over it.
func TestLinkErrExistDoesNotRenameOverANewerRequest(t *testing.T) {
	_, stateDir := setup(t)
	writeRequestFile(t, stateDir, requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-time.Minute)}))
	newer := requestJSON(t, runRequest{V: 1, RequestedAt: time.Now(), Options: runOptions{Advance: 4}})
	oldLink, oldRename := linkRequest, renameRequest
	t.Cleanup(func() { linkRequest, renameRequest = oldLink, oldRename })
	linkRequest = func(string, string) error { return os.ErrExist }
	renames := 0
	renameRequest = func(string, string) error {
		renames++
		return errors.New("rename must not run")
	}
	stderr, err := restartingServe(t, stateDir, true, func() { writeRequestFile(t, stateDir, newer) })
	if err != nil || renames != 0 {
		t.Fatalf("err %v renames %d\n%s", err, renames, stderr)
	}
	got, rerr := os.ReadFile(runsPath(stateDir, runRequestFile))
	if rerr != nil || !bytes.Equal(got, newer) || requestExists(stateDir, runClaimedFile) {
		t.Fatalf("pending %v claimed %v\n%s", rerr, requestExists(stateDir, runClaimedFile), got)
	}
}

// A stat error that is not "no such file" is not a missing binary. The request runs.
func TestNotARegularFileIsNotAMissingBinary(t *testing.T) {
	_, stateDir := setup(t)
	writeRequestFile(t, stateDir, requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-time.Second), Options: runOptions{Advance: 3}}))
	exe := &fakeExe{id: oldImage}
	exe.install(t, nil)
	exe.set(binIdentity{}, errors.New(fakeExePath+" is not a regular file"))
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr, binary: &binWatch{path: fakeExePath, start: oldImage}}
	ctx, cancel := context.WithCancelCause(context.Background())
	e.bindRestart(cancel)
	calls := 0
	var mu sync.Mutex
	err := e.serveRunRequest(ctx, func(context.Context, runOptions) error { calls++; return nil }, &mu)
	if err != nil || calls != 1 {
		t.Fatalf("err %v calls %d\n%s", err, calls, stderr.String())
	}
	if context.Cause(ctx) != nil {
		t.Fatalf("cause %v", context.Cause(ctx))
	}
	if requestExists(stateDir, runRequestFile) || requestExists(stateDir, runClaimedFile) {
		t.Fatal("a finished run left a request")
	}
}

// No binary watch does not invent a restart for a request this binary can run.
func TestNilBinaryWatchRunsTheRequest(t *testing.T) {
	_, stateDir := setup(t)
	writeRequestFile(t, stateDir, requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-time.Second)}))
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}
	ctx, cancel := context.WithCancelCause(context.Background())
	e.bindRestart(cancel)
	calls := 0
	var mu sync.Mutex
	err := e.serveRunRequest(ctx, func(context.Context, runOptions) error { calls++; return nil }, &mu)
	if err != nil || calls != 1 || context.Cause(ctx) != nil {
		t.Fatalf("err %v calls %d cause %v", err, calls, context.Cause(ctx))
	}
}

// The state directory's name is data. A non-ASCII path still round-trips the claim.
func TestUnicodeStateDirRequeuesOnRestart(t *testing.T) {
	cfgPath, _ := setup(t)
	stateDir := filepath.Join(filepath.Dir(cfgPath), "名前")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-time.Second)})
	writeRequestFile(t, stateDir, raw)
	stderr, err := restartingServe(t, stateDir, true, nil)
	if err != nil {
		t.Fatalf("err %v\n%s", err, stderr)
	}
	got, rerr := os.ReadFile(runsPath(stateDir, runRequestFile))
	if rerr != nil || !bytes.Equal(got, raw) || requestExists(stateDir, runClaimedFile) {
		t.Fatalf("pending %v claimed %v", rerr, requestExists(stateDir, runClaimedFile))
	}
}
