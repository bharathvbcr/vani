package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// keptClaimSetup makes link and rename both fail while a restart requeues the
// request, so serveRunRequest returns errClaimKept with the claim and its
// marker in place. It returns the claim's bytes.
func keptClaimSetup(t *testing.T, stateDir string) []byte {
	t.Helper()
	raw := requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-time.Second)})
	writeRequestFile(t, stateDir, raw)
	oldLink, oldRename := linkRequest, renameRequest
	t.Cleanup(func() { linkRequest, renameRequest = oldLink, oldRename })
	linkRequest = func(string, string) error { return &os.PathError{Op: "link", Err: errors.ErrUnsupported} }
	renameRequest = func(string, string) error { return &os.PathError{Op: "rename", Err: os.ErrPermission} }
	stderr, err := restartingServe(t, stateDir, true, nil)
	if !errors.Is(err, errClaimKept) {
		t.Fatalf("err %v, want errClaimKept\n%s", err, stderr)
	}
	linkRequest, renameRequest = oldLink, oldRename
	return raw
}

func clearOnNextDaemon(stateDir string) string {
	var stderr bytes.Buffer
	(&env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}).clearClaimedRequest()
	return stderr.String()
}

// The claim a daemon kept (link and rename both failed) is put back by the
// next daemon, which then runs it exactly once; it is not deleted.
func TestKeptClaimSurvivesTheNextDaemonAndRunsOnce(t *testing.T) {
	_, stateDir := setup(t)
	raw := keptClaimSetup(t, stateDir)
	if !requestExists(stateDir, runKeptFile) {
		t.Fatal("errClaimKept left no marker")
	}
	out := clearOnNextDaemon(stateDir)
	got, err := os.ReadFile(runsPath(stateDir, runRequestFile))
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("kept claim was not put back: %v\n%s\n%s", err, got, out)
	}
	if requestExists(stateDir, runClaimedFile) || requestExists(stateDir, runKeptFile) {
		t.Fatalf("claimed %v marker %v after restore", requestExists(stateDir, runClaimedFile), requestExists(stateDir, runKeptFile))
	}
	calls := 0
	var mu sync.Mutex
	for i := 0; i < 2; i++ {
		if err := (&env{stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}).serveRunRequest(
			context.Background(), func(context.Context, runOptions) error { calls++; return nil }, &mu); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("ran %d times, want once", calls)
	}
}

// Restoring must not clobber a newer pending request: the newer one wins and
// the kept claim is dropped.
func TestKeptClaimYieldsToANewerRequest(t *testing.T) {
	_, stateDir := setup(t)
	keptClaimSetup(t, stateDir)
	newer := requestJSON(t, runRequest{V: 1, RequestedAt: time.Now(), Options: runOptions{Advance: 3}})
	writeRequestFile(t, stateDir, newer)
	clearOnNextDaemon(stateDir)
	got, _ := os.ReadFile(runsPath(stateDir, runRequestFile))
	if !bytes.Equal(got, newer) || requestExists(stateDir, runClaimedFile) || requestExists(stateDir, runKeptFile) {
		t.Fatalf("pending %s claimed %v marker %v", got, requestExists(stateDir, runClaimedFile), requestExists(stateDir, runKeptFile))
	}
}

// If the next daemon cannot put the claim back either, the claim and marker
// stay for the daemon after it.
func TestKeptClaimStaysWhenRestoreFailsAgain(t *testing.T) {
	_, stateDir := setup(t)
	raw := keptClaimSetup(t, stateDir)
	oldLink, oldRename := linkRequest, renameRequest
	t.Cleanup(func() { linkRequest, renameRequest = oldLink, oldRename })
	linkRequest = func(string, string) error { return &os.PathError{Op: "link", Err: errors.ErrUnsupported} }
	renameRequest = func(string, string) error { return &os.PathError{Op: "rename", Err: os.ErrPermission} }
	clearOnNextDaemon(stateDir)
	got, err := os.ReadFile(runsPath(stateDir, runClaimedFile))
	if err != nil || !bytes.Equal(got, raw) || !requestExists(stateDir, runKeptFile) {
		t.Fatalf("claim %v marker %v", err, requestExists(stateDir, runKeptFile))
	}
}

// A claim with no errClaimKept (a crash or a killed run) is still removed and
// not run: no marker at all.
func TestCrashLeftoverClaimIsStillRemoved(t *testing.T) {
	_, stateDir := setup(t)
	writeRequestFile(t, stateDir, requestJSON(t, runRequest{V: 1, RequestedAt: time.Now()}))
	if err := os.Rename(runsPath(stateDir, runRequestFile), runsPath(stateDir, runClaimedFile)); err != nil {
		t.Fatal(err)
	}
	clearOnNextDaemon(stateDir)
	if requestExists(stateDir, runClaimedFile) || requestExists(stateDir, runRequestFile) {
		t.Fatal("a crash leftover was kept or requeued")
	}
}

// Markers that do not vouch for this claim leave today's behavior: the claim
// is removed, never requeued, and an unusable marker is cleaned up.
func TestUnusableKeptMarkersDoNotSaveTheClaim(t *testing.T) {
	cases := []struct {
		name  string
		write func(t *testing.T, stateDir string, good []byte)
	}{
		{"different file", func(t *testing.T, s string, g []byte) {
			put(t, runsPath(s, runKeptFile), []byte(strings.Replace(string(g), runClaimedFile, "request.other.json", 1)))
		}},
		{"different content", func(t *testing.T, s string, g []byte) {
			put(t, runsPath(s, runClaimedFile), []byte(`{"v":1,"requested_at":"2020-01-01T00:00:00Z"}`))
		}},
		{"corrupt", func(t *testing.T, s string, _ []byte) { put(t, runsPath(s, runKeptFile), []byte(`{"v":1,"file":`)) }},
		{"empty", func(t *testing.T, s string, _ []byte) { put(t, runsPath(s, runKeptFile), nil) }},
		{"no digest", func(t *testing.T, s string, _ []byte) {
			put(t, runsPath(s, runKeptFile), []byte(`{"v":1,"file":"`+runClaimedFile+`","sha256":""}`))
		}},
		{"oversized", func(t *testing.T, s string, g []byte) {
			put(t, runsPath(s, runKeptFile), append(append([]byte{}, g...), bytes.Repeat([]byte(" "), maxKeptMarker)...))
		}},
		{"symlink", func(t *testing.T, s string, g []byte) {
			outside := t.TempDir() + "/outside.json"
			put(t, outside, g)
			if err := os.Remove(runsPath(s, runKeptFile)); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, runsPath(s, runKeptFile)); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, stateDir := setup(t)
			keptClaimSetup(t, stateDir)
			g, err := os.ReadFile(runsPath(stateDir, runKeptFile))
			if err != nil {
				t.Fatal(err)
			}
			c.write(t, stateDir, g)
			clearOnNextDaemon(stateDir)
			if requestExists(stateDir, runClaimedFile) || requestExists(stateDir, runRequestFile) {
				t.Fatalf("claimed %v pending %v", requestExists(stateDir, runClaimedFile), requestExists(stateDir, runRequestFile))
			}
			if requestExists(stateDir, runKeptFile) {
				t.Fatal("the unusable marker was left behind")
			}
		})
	}
}

func put(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A symlinked marker's target is neither read through nor removed.
func TestSymlinkedKeptMarkerTargetIsLeftAlone(t *testing.T) {
	_, stateDir := setup(t)
	keptClaimSetup(t, stateDir)
	g, err := os.ReadFile(runsPath(stateDir, runKeptFile))
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir() + "/outside.json"
	put(t, outside, g)
	if err := os.Remove(runsPath(stateDir, runKeptFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, runsPath(stateDir, runKeptFile)); err != nil {
		t.Fatal(err)
	}
	clearOnNextDaemon(stateDir)
	if got, err := os.ReadFile(outside); err != nil || !bytes.Equal(got, g) {
		t.Fatalf("symlink target touched: %v", err)
	}
	if requestExists(stateDir, runRequestFile) {
		t.Fatal("a symlinked marker saved the claim")
	}
}

// A marker whose claim is gone is stale and is removed; nothing is requeued.
func TestStaleKeptMarkerWithoutClaimIsRemoved(t *testing.T) {
	_, stateDir := setup(t)
	keptClaimSetup(t, stateDir)
	if err := os.Remove(runsPath(stateDir, runClaimedFile)); err != nil {
		t.Fatal(err)
	}
	clearOnNextDaemon(stateDir)
	if requestExists(stateDir, runKeptFile) || requestExists(stateDir, runRequestFile) {
		t.Fatal("stale marker left or request invented")
	}
}
