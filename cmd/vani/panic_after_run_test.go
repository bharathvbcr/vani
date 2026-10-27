package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/daemon"
)

// A panic in the bookkeeping after guardedRun (RunReport, the ledger read,
// the failure notice) used to escape dailyJob and leave last.json "running".
func TestPanicAfterGuardedRunFinalisesTheRecord(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	n := &fakeNotifier{}
	app := &App{
		Ledger:   fixture(),
		Notifier: n,
		RunOnce:  func(context.Context) error { return nil },
		RunReport: func() runDetail {
			panic("boom\nhttps://leak.example")
		},
	}
	e := &env{configPath: os.Getenv("VANI_CONFIG"), stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	err := runPanicking(t, e.dailyJob(app, config.Default(), triggerSchedule, runOptions{NoDigest: true}), context.Background())
	if err == nil || errors.Is(err, daemon.ErrFatal) || !errors.Is(err, errRunPanic) {
		t.Fatalf("returned %v, want a non-fatal error wrapping errRunPanic", err)
	}
	r := lastRecord(t, stateDir)
	if r.Outcome == outcomeRunning || r.Outcome != outcomeFailed || r.FinishedAt.IsZero() || !strings.Contains(r.Error, "panic") {
		t.Fatalf("record outcome=%s error=%q finished=%v, want a finished failed run naming the panic", r.Outcome, r.Error, r.FinishedAt)
	}
	if strings.Contains(r.Error, "\n") {
		t.Fatalf("record error kept a raw panic line: %q", r.Error)
	}
	if len(n.calls) != 1 || !strings.HasPrefix(n.calls[0], "Vani run failed|") || strings.Contains(n.calls[0], "leak.example") {
		t.Fatalf("notifications %v, want exactly one failure notice without the panic text", n.calls)
	}
}

// The same panic with a ledger that broke after the run is fatal, and the
// record is still not left running.
func TestPanicAfterGuardedRunWithABrokenLedgerIsFatal(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	n := &fakeNotifier{}
	l := &brokenLedger{memLedger: fixture()}
	app := &App{
		Ledger:   l,
		Notifier: n,
		RunOnce:  func(context.Context) error { return nil },
		RunReport: func() runDetail {
			l.poison(errors.New("ledger: fsync failed: input/output error"))
			panic("boom after the run")
		},
	}
	e := &env{configPath: os.Getenv("VANI_CONFIG"), stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	err := runPanicking(t, e.dailyJob(app, config.Default(), triggerSchedule, runOptions{NoDigest: true}), context.Background())
	if err == nil || !errors.Is(err, daemon.ErrFatal) || !errors.Is(err, errRunPanic) {
		t.Fatalf("returned %v, want ErrFatal joined with errRunPanic", err)
	}
	r := lastRecord(t, stateDir)
	if r.Outcome == outcomeRunning || r.Outcome != outcomeFailed || r.FinishedAt.IsZero() {
		t.Fatalf("record outcome=%s finished=%v, want a finished failed run", r.Outcome, r.FinishedAt)
	}
}
