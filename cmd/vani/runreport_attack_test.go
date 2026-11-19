package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// panicWriter panics on a line that contains trigger, once.
type panicWriter struct {
	trigger string
	buf     bytes.Buffer
	fired   bool
}

func (p *panicWriter) Write(b []byte) (int, error) {
	if !p.fired && strings.Contains(string(b), p.trigger) {
		p.fired = true
		panic("warn writer exploded")
	}
	return p.buf.Write(b)
}

func sameReport(t *testing.T, w *wired, want reportState) {
	t.Helper()
	got := w.saveReport()
	if got.Boards != want.Boards || got.Advance != want.Advance || got.BackendErrs != want.BackendErrs {
		t.Fatalf("report was not restored: got %+v want %+v", got, want)
	}
}

// A panic after the poll's startRun reset the report (here a warn writer that
// panics on the "wait for the next run" line) must still run the deferred restore.
func TestAttackPollInboxPanicStillRestoresReport(t *testing.T) {
	clearKeys(t)
	cfg := config.Default()
	cfg.Decide.OpenRouterModel = ""
	cfg.LLM.Provider = ""
	w, _ := openWired(t, cfg)
	pw := &panicWriter{trigger: "wait for the next run"}
	w.warn = pw
	seedReport(w)
	want := w.saveReport()
	spoolBatch(t, w.stateDir, "careers-acme-com", "Data Engineer")
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the warn writer did not panic: the test did not reach the post-startRun path")
			}
		}()
		_ = w.pollInbox(context.Background())
	}()
	sameReport(t, w, want)
}

// A poll whose startRun succeeds (a key is set; the context is already
// cancelled so nothing reaches the network) must put back the daily report.
func TestAttackPollInboxCancelledAdvanceRestoresReport(t *testing.T) {
	clearKeys(t)
	t.Setenv("VANI_NO_KEYCHAIN", "1")
	t.Setenv("OPENROUTER_API_KEY", "test-not-a-real-key")
	cfg := config.Default()
	w, warn := openWired(t, cfg)
	seedReport(w)
	want := w.saveReport()
	spoolBatch(t, w.stateDir, "careers-acme-com", "Data Engineer")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = w.pollInbox(ctx)
	if strings.Contains(warn.String(), "wait for the next run") {
		t.Skipf("startRun refused here, so the success path was not exercised: %s", warn.String())
	}
	sameReport(t, w, want)
}

// Readers of the report race with polls that reset and restore it.
func TestAttackReportReadsRaceWithPolls(t *testing.T) {
	clearKeys(t)
	cfg := config.Default()
	cfg.Decide.OpenRouterModel = ""
	cfg.LLM.Provider = ""
	w, _ := openWired(t, cfg)
	seedReport(w)
	spoolBatch(t, w.stateDir, "careers-acme-com", "Data Engineer")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = w.runReport()
				_ = w.saveReport()
			}
		}()
	}
	for range 20 {
		if err := w.pollInbox(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	if rd := w.runReport(); rd.Advance == nil || rd.Advance.Attempted != 123 {
		t.Fatalf("report after polls: %+v", rd)
	}
}

func wiredApp(w *wired) *App {
	return &App{Ledger: fixture(), RunOnce: w.runOnce, RunReport: w.runReport, Reload: func(config.Config) {}}
}

// Through the real wired runOnce: a run that fails inside startRun (no
// allowed hosts) has pipelineRan set, yet must not record the last run's report.
func TestAttackRunOnceStartRunFailureRecordsNoStaleReport(t *testing.T) {
	e, stateDir, _ := panicAfterEnv(t)
	cfg := config.Default()
	cfg.AllowedHosts = nil
	w, _ := openWired(t, cfg)
	seedReport(w)
	err := e.dailyJob(wiredApp(w), config.Default(), triggerManual, runOptions{NoDigest: true})(context.Background())
	if err == nil {
		t.Fatal("run succeeded without allowed hosts")
	}
	r := lastRecord(t, stateDir)
	if r.Outcome != outcomeFailed {
		t.Fatalf("outcome %s", r.Outcome)
	}
	assertEmptyReport(t, r)

	// --list-only takes the same path.
	err = e.dailyJob(wiredApp(w), config.Default(), triggerManual, runOptions{NoDigest: true, ListOnly: true})(context.Background())
	if err == nil {
		t.Fatal("list-only run succeeded without allowed hosts")
	}
	assertEmptyReport(t, lastRecord(t, stateDir))
}

// A refused run after a poll: the poll restored the daily report, the refusal
// must still record nothing, and the wired report stays the daily one.
func TestAttackRefusedRunAfterPollRecordsNothing(t *testing.T) {
	clearKeys(t)
	e, stateDir, _ := panicAfterEnv(t)
	cfg := config.Default()
	cfg.Decide.OpenRouterModel = ""
	cfg.LLM.Provider = ""
	w, _ := openWired(t, cfg)
	seedReport(w)
	spoolBatch(t, w.stateDir, "careers-acme-com", "Data Engineer")
	if err := w.pollInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := e.dailyJob(wiredApp(w), config.Default(), triggerManual, runOptions{NoDigest: true, Boards: []string{"gh:no-such-board"}})(context.Background())
	if !errors.Is(err, errBoardSelection) {
		t.Fatalf("err = %v", err)
	}
	assertEmptyReport(t, lastRecord(t, stateDir))
	if rd := w.runReport(); rd.Advance == nil || rd.Advance.Attempted != 123 {
		t.Fatalf("wired report changed: %+v", rd)
	}
}

// A panic inside RunOnce is a run that reached the pipeline: the record is a
// failed one carrying the pipeline's own report, and the daemon survives.
func TestAttackRunOncePanicRecordsOwnReport(t *testing.T) {
	e, stateDir, _ := panicAfterEnv(t)
	own := &pipeline.AdvanceReport{Attempted: 7}
	app := &App{
		Ledger:    fixture(),
		RunOnce:   func(context.Context) error { panic("boom in pipeline") },
		RunReport: func() runDetail { return runDetail{Advance: own} },
		Reload:    func(config.Config) {},
	}
	err, panicked := callJob(e.dailyJob(app, config.Default(), triggerManual, runOptions{NoDigest: true}))
	if panicked == nil && err == nil {
		t.Fatal("a panicking pipeline reported success")
	}
	if panicked == nil {
		r := lastRecord(t, stateDir)
		if r.Outcome != outcomeFailed {
			t.Fatalf("outcome %s", r.Outcome)
		}
		if r.Advance == nil || r.Advance.Attempted != 7 {
			t.Fatalf("a run that reached the pipeline lost its own report: %+v", r.Advance)
		}
	}
}

// RunOnce failing straight away still records the report the pipeline holds.
func TestAttackRunOnceErrorRecordsOwnReport(t *testing.T) {
	e, stateDir, _ := panicAfterEnv(t)
	app := &App{
		Ledger:    fixture(),
		RunOnce:   func(context.Context) error { return errors.New("boards unreadable") },
		RunReport: func() runDetail { return runDetail{Advance: &pipeline.AdvanceReport{Attempted: 9}} },
		Reload:    func(config.Config) {},
	}
	if err := e.dailyJob(app, config.Default(), triggerManual, runOptions{NoDigest: true})(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	if r := lastRecord(t, stateDir); r.Advance == nil || r.Advance.Attempted != 9 {
		t.Fatalf("record %+v", r.Advance)
	}
}

var _ = posting.Live
