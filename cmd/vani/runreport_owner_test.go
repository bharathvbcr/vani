package main

import (
	"context"
	"errors"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/daemon"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// previousRunDetail is what an earlier, successful run left in the app's
// report: the numbers a refused run must not copy into its own record.
func previousRunDetail() runDetail {
	return runDetail{
		Boards:  &boardsReport{Configured: 7, Read: 7},
		Advance: &pipeline.AdvanceReport{Attempted: 123},
	}
}

func assertEmptyReport(t *testing.T, r runRecord) {
	t.Helper()
	if r.Boards != nil || r.Advance != nil || r.BackendErrors != nil || r.SemanticPre != nil {
		t.Fatalf("run %s recorded a report it did not produce: boards %+v advance %+v", r.Outcome, r.Boards, r.Advance)
	}
}

// A run refused before its pipeline starts records an empty report, not the
// last successful run's counts under its own outcome.
func TestRefusedDailyRunRecordsNoPreviousReport(t *testing.T) {
	e, stateDir, _ := panicAfterEnv(t)
	led := &brokenLedger{memLedger: fixture()}
	app := &App{
		Ledger:    led,
		RunOnce:   func(context.Context) error { return nil },
		RunReport: func() runDetail { return previousRunDetail() },
		Reload:    func(config.Config) {},
	}
	run := func(opts runOptions) error {
		opts.NoDigest = true
		return e.dailyJob(app, config.Default(), triggerManual, opts)(context.Background())
	}

	if err := run(runOptions{}); err != nil {
		t.Fatal(err)
	}
	ok := lastRecord(t, stateDir)
	if ok.Outcome != outcomeOK || ok.Advance == nil || ok.Advance.Attempted != 123 || ok.Boards == nil {
		t.Fatalf("the seeding run did not record its own report: %+v", ok)
	}

	t.Run("board selection", func(t *testing.T) {
		err := run(runOptions{Boards: []string{"gh:no-such-board"}})
		if !errors.Is(err, errBoardSelection) {
			t.Fatalf("err = %v, want errBoardSelection", err)
		}
		r := lastRecord(t, stateDir)
		if r.Outcome != outcomeFailed {
			t.Fatalf("outcome %s", r.Outcome)
		}
		assertEmptyReport(t, r)
	})

	t.Run("ledger broken", func(t *testing.T) {
		led.poison(errors.New("disk gone"))
		defer led.poison(nil)
		err := run(runOptions{})
		if !errors.Is(err, daemon.ErrFatal) {
			t.Fatalf("err = %v, want ErrFatal", err)
		}
		r := lastRecord(t, stateDir)
		if r.Outcome != outcomeFailed {
			t.Fatalf("outcome %s", r.Outcome)
		}
		assertEmptyReport(t, r)
	})

	t.Run("a later run that reaches the pipeline reports again", func(t *testing.T) {
		if err := run(runOptions{}); err != nil {
			t.Fatal(err)
		}
		if r := lastRecord(t, stateDir); r.Advance == nil || r.Advance.Attempted != 123 {
			t.Fatalf("report lost: %+v", r.Advance)
		}
	})
}

// A run whose egress client cannot be built fails inside startRun. Its report
// is the empty one, not the last run's.
func TestStartRunFailureClearsTheLastRunsReport(t *testing.T) {
	cfg := config.Default()
	cfg.AllowedHosts = nil
	w, _ := openWired(t, cfg)
	seedReport(w)
	if _, _, _, err := w.startRun(context.Background(), false); err == nil {
		t.Fatal("startRun succeeded with no allowed hosts")
	}
	if rd := w.runReport(); rd.Boards != nil || rd.Advance != nil || rd.SemanticPre != nil {
		t.Fatalf("a run refused at egress reports the last run's numbers: %+v", rd)
	}
}

// The daemon polls the inbox between daily runs. The poll is not a daily run:
// what the last daily run reported is still what RunReport says after it.
func TestPollInboxKeepsTheLastDailyRunsReport(t *testing.T) {
	clearKeys(t)
	cfg := config.Default()
	cfg.Decide.OpenRouterModel = ""
	cfg.LLM.Provider = ""
	w, _ := openWired(t, cfg)
	seedReport(w)
	spoolBatch(t, w.stateDir, "careers-acme-com", "Data Engineer")
	if err := w.pollInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if recs := w.led.All(); len(recs) != 1 || recs[0].Status != posting.Live {
		t.Fatalf("the poll did not fold the batch: %+v", recs)
	}
	rd := w.runReport()
	if rd.Boards == nil || rd.Boards.Configured != 7 || rd.Advance == nil || rd.Advance.Attempted != 123 {
		t.Fatalf("an inbox poll wiped the daily run's report: %+v", rd)
	}

	// An empty poll and a second one change nothing either.
	for range 2 {
		if err := w.pollInbox(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if rd := w.runReport(); rd.Advance == nil || rd.Advance.Attempted != 123 {
		t.Fatalf("report after more polls: %+v", rd)
	}
}

func seedReport(w *wired) {
	d := previousRunDetail()
	w.mu.Lock()
	w.backendErrs, w.boards, w.advance = newBackendErrors(), d.Boards, d.Advance
	w.mu.Unlock()
}
