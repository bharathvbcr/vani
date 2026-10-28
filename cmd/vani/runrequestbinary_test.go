package main

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
)

// A daemon whose binary was replaced (the app reinstalled) must not read a
// request the newer vani wrote: options it does not know, such as a scope,
// fail its strict decode and the request would be dropped as corrupt. It
// restarts first, as a run's reload does, and the request waits, unclaimed,
// for the daemon that starts next.
func TestServeRunRequestRestartsOnAReplacedBinaryBeforeReading(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	announceDaemon(t, stateDir)
	mustCode(t, 0, "run-request", "--company", "acme")
	exe := &fakeExe{id: oldImage}
	exe.install(t, nil)
	var stderr strings.Builder
	var cause error
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr, binary: watchOwnBinary(io.Discard),
		restart: func(c error) { cause = c }}
	exe.set(newImage, nil)
	calls := 0
	var mu sync.Mutex
	err := e.serveRunRequest(context.Background(), func(context.Context, runOptions) error { calls++; return nil }, &mu)
	if err != nil || calls != 0 || !errors.Is(cause, errConfigRestart) {
		t.Fatalf("served: err %v, %d runs, restart cause %v", err, calls, cause)
	}
	if !requestExists(stateDir, runRequestFile) || requestExists(stateDir, runClaimedFile) {
		t.Error("the request did not stay pending for the next daemon")
	}
	if !strings.Contains(stderr.String(), "the request waits for the next daemon") {
		t.Errorf("stderr %q", stderr.String())
	}
	// The next daemon (its own binary) runs it.
	got, err := serveOnce(t, stateDir, nil)
	if err != nil || got.calls != 1 || !slices.Equal(got.opts.Companies, []string{"acme"}) {
		t.Errorf("the next daemon served %+v, %v", got, err)
	}
}
