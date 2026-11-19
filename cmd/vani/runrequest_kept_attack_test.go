package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// handKept lays down a claim and a marker the way errClaimKept leaves them.
func handKept(t *testing.T, stateDir string, raw []byte) {
	t.Helper()
	put := func(p string, b []byte) {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put(runsPath(stateDir, runClaimedFile), raw)
	if err := writeKeptMarker(stateDir); err != nil {
		t.Fatal(err)
	}
}

func serveCount(t *testing.T, stateDir string) (int, error) {
	t.Helper()
	calls := 0
	var mu sync.Mutex
	var err error
	for i := 0; i < 2; i++ {
		if e := (&env{stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}).serveRunRequest(
			context.Background(), func(context.Context, runOptions) error { calls++; return nil }, &mu); e != nil {
			err = e
		}
	}
	return calls, err
}

// The marker cannot be written (its path is a directory): the claim is still
// kept, the error says the marker failed, and the next daemon deletes the claim
// as before (the old behaviour), without crashing on the directory.
func TestAttackMarkerWriteFailsClaimClearedNextStart(t *testing.T) {
	_, stateDir := setup(t)
	if err := os.MkdirAll(runsPath(stateDir, runKeptFile), 0o700); err != nil {
		t.Fatal(err)
	}
	oldLink, oldRename := linkRequest, renameRequest
	t.Cleanup(func() { linkRequest, renameRequest = oldLink, oldRename })
	writeRequestFile(t, stateDir, requestJSON(t, runRequest{V: 1, RequestedAt: time.Now()}))
	linkRequest = func(string, string) error { return os.ErrPermission }
	renameRequest = func(string, string) error { return os.ErrPermission }
	stderr, err := restartingServe(t, stateDir, true, nil)
	linkRequest, renameRequest = oldLink, oldRename
	if !errors.Is(err, errClaimKept) || !strings.Contains(err.Error(), "marker") {
		t.Fatalf("err %v; want errClaimKept mentioning the marker\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "could not mark the kept claim") || !requestExists(stateDir, runClaimedFile) {
		t.Fatalf("claim must stay and the failure be reported: %s", stderr)
	}
	clearOnNextDaemon(stateDir)
	if requestExists(stateDir, runClaimedFile) || requestExists(stateDir, runRequestFile) {
		t.Fatal("claim survived or was requeued with no valid marker")
	}
}

// A crash after the link but before the claim was removed leaves pending and
// claim as the same file plus a valid marker. The next start must leave one
// pending request, and it must run exactly once.
func TestAttackCrashBetweenPutBackAndClaimRemoval(t *testing.T) {
	_, stateDir := setup(t)
	raw := requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-time.Second)})
	handKept(t, stateDir, raw)
	if err := os.Link(runsPath(stateDir, runClaimedFile), runsPath(stateDir, runRequestFile)); err != nil {
		t.Fatal(err)
	}
	clearOnNextDaemon(stateDir)
	if requestExists(stateDir, runClaimedFile) || requestExists(stateDir, runKeptFile) {
		t.Fatal("claim or marker left")
	}
	if n, err := serveCount(t, stateDir); n != 1 || err != nil {
		t.Fatalf("ran %d times (err %v), want once", n, err)
	}
}

// A crash after the claim was removed but before the marker: marker alone is
// removed, the pending request is untouched and runs once.
func TestAttackCrashBetweenClaimRemovalAndMarkerRemoval(t *testing.T) {
	_, stateDir := setup(t)
	raw := requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-time.Second)})
	handKept(t, stateDir, raw)
	if err := os.Rename(runsPath(stateDir, runClaimedFile), runsPath(stateDir, runRequestFile)); err != nil {
		t.Fatal(err)
	}
	clearOnNextDaemon(stateDir)
	if requestExists(stateDir, runKeptFile) {
		t.Fatal("stale marker left")
	}
	if n, _ := serveCount(t, stateDir); n != 1 {
		t.Fatalf("ran %d times, want once", n)
	}
}

// A restored request older than runRequestMaxAge is not run: the serve drops it
// as stale (error, no run) and removes it.
func TestAttackRestoredExpiredRequestIsNotRun(t *testing.T) {
	_, stateDir := setup(t)
	handKept(t, stateDir, requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-2 * runRequestMaxAge)}))
	clearOnNextDaemon(stateDir)
	if !requestExists(stateDir, runRequestFile) {
		t.Fatal("expected the claim to be restored as pending")
	}
	n, err := serveCount(t, stateDir)
	if n != 0 || err == nil || !errors.Is(err, errRequestStale) {
		t.Fatalf("ran %d, err %v; want 0 runs and a stale error", n, err)
	}
	if requestExists(stateDir, runRequestFile) || requestExists(stateDir, runClaimedFile) {
		t.Fatal("expired request left behind")
	}
}

// Hostile markers: none may save the claim.
func TestAttackHostileMarkersDoNotRestore(t *testing.T) {
	raw := requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-time.Second)})
	sum := claimDigest(raw)
	pad := func(n int) string { return strings.Repeat(" ", n) }
	good := `{"v":1,"file":"request.claimed.json","sha256":"` + sum + `"}`
	cases := map[string]string{
		"dotdot file":   `{"v":1,"file":"../request.claimed.json","sha256":"` + sum + `"}`,
		"absolute file": `{"v":1,"file":"/etc/passwd","sha256":"` + sum + `"}`,
		"dot file":      `{"v":1,"file":"./request.claimed.json","sha256":"` + sum + `"}`,
		"extra field":   `{"v":1,"file":"request.claimed.json","sha256":"` + sum + `","x":1}`,
		"bom":           "\ufeff" + good,
		"version 2":     `{"v":2,"file":"request.claimed.json","sha256":"` + sum + `"}`,
		"upper digest":  `{"v":1,"file":"request.claimed.json","sha256":"` + strings.ToUpper(sum) + `"}`,
		"trailing":      good + "{}",
		"array":         "[" + good + "]",
		"4KiB+1":        good + pad(maxKeptMarker+1-len(good)),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, stateDir := setup(t)
			handKept(t, stateDir, raw)
			if err := os.WriteFile(runsPath(stateDir, runKeptFile), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			clearOnNextDaemon(stateDir)
			if requestExists(stateDir, runRequestFile) || requestExists(stateDir, runClaimedFile) || requestExists(stateDir, runKeptFile) {
				t.Fatalf("pending %v claimed %v marker %v", requestExists(stateDir, runRequestFile), requestExists(stateDir, runClaimedFile), requestExists(stateDir, runKeptFile))
			}
		})
	}
}

// A claim edited by one byte after the marker was written is deleted.
func TestAttackClaimModifiedAfterMarker(t *testing.T) {
	_, stateDir := setup(t)
	raw := requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-time.Second)})
	handKept(t, stateDir, raw)
	if err := os.WriteFile(runsPath(stateDir, runClaimedFile), append(append([]byte{}, raw...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	clearOnNextDaemon(stateDir)
	if requestExists(stateDir, runRequestFile) || requestExists(stateDir, runClaimedFile) {
		t.Fatal("modified claim restored or left")
	}
}

// A claim replaced by a symlink to an outside file with the matching digest is
// neither followed nor restored; the link is removed, the target untouched.
func TestAttackClaimSymlinkToOutsideFile(t *testing.T) {
	_, stateDir := setup(t)
	raw := requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-time.Second)})
	handKept(t, stateDir, raw)
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(runsPath(stateDir, runClaimedFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, runsPath(stateDir, runClaimedFile)); err != nil {
		t.Fatal(err)
	}
	clearOnNextDaemon(stateDir)
	if requestExists(stateDir, runRequestFile) || requestExists(stateDir, runClaimedFile) {
		t.Fatal("symlinked claim restored or left")
	}
	if got, err := os.ReadFile(outside); err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("outside target touched: %v", err)
	}
}

// Concurrent clears (raced under -race) leave exactly one pending request.
func TestAttackConcurrentClearsRestoreOnce(t *testing.T) {
	_, stateDir := setup(t)
	raw := requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-time.Second)})
	handKept(t, stateDir, raw)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			(&env{stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}).clearClaimedRequest()
		}()
	}
	wg.Wait()
	got, err := os.ReadFile(runsPath(stateDir, runRequestFile))
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("pending %v %s", err, got)
	}
	if requestExists(stateDir, runClaimedFile) || requestExists(stateDir, runKeptFile) {
		t.Fatal("claim or marker left")
	}
}

// Status with a kept claim present still reports it as claimed, not pending,
// and the facet is OK.
func TestAttackStatusFacetWithKeptClaim(t *testing.T) {
	_, stateDir := setup(t)
	handKept(t, stateDir, requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(-time.Second)}))
	f := (&env{stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}).runRequestFacet(time.Now())
	if !f.OK || !f.Claimed || f.Pending {
		t.Fatalf("facet %+v", f)
	}
}

// A second errClaimKept replaces the marker with the new claim's digest, and
// the next daemon restores that claim.
func TestAttackSecondKeptReplacesMarker(t *testing.T) {
	_, stateDir := setup(t)
	keptClaimSetup(t, stateDir)
	raw2 := requestJSON(t, runRequest{V: 1, RequestedAt: time.Now(), Options: runOptions{Advance: 7}})
	if err := os.WriteFile(runsPath(stateDir, runClaimedFile), raw2, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeKeptMarker(stateDir); err != nil {
		t.Fatal(err)
	}
	clearOnNextDaemon(stateDir)
	got, _ := os.ReadFile(runsPath(stateDir, runRequestFile))
	if !bytes.Equal(got, raw2) {
		t.Fatalf("pending %s", got)
	}
}
