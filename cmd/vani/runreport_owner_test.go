package main

import (
	"context"
	"errors"
	"reflect"
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

// A run refused by dailySteps' own checkPromptSummary (the words file is gone
// since start, and the reload falls back to the previous config) never reaches
// the pipeline, so it records an empty report. It gets its own env: an earlier
// run's reload would otherwise have replaced the "previous" config.
func TestSummaryRefusedDailyRunRecordsNoPreviousReport(t *testing.T) {
	e, stateDir, _ := panicAfterEnv(t)
	start, err := config.Load(e.configPath)
	if err != nil {
		t.Fatal(err)
	}
	start.TargetSummary = cleanSummary
	writeConfig(t, e.configPath, start)
	runs := 0
	app := &App{
		Ledger:    &brokenLedger{memLedger: fixture()},
		RunOnce:   func(context.Context) error { runs++; return nil },
		RunReport: func() runDetail { return previousRunDetail() },
		Reload:    func(config.Config) {},
	}
	// No identifying-words file exists in the state dir.
	err = e.dailyJob(app, start, triggerManual, runOptions{NoDigest: true})(context.Background())
	if !errors.Is(err, errTargetSummary) || runs != 0 {
		t.Fatalf("err = %v, runs = %d; want errTargetSummary and no pipeline run", err, runs)
	}
	r := lastRecord(t, stateDir)
	if r.Outcome != outcomeFailed {
		t.Fatalf("outcome %s", r.Outcome)
	}
	assertEmptyReport(t, r)
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

// filledReport sets every field of a reportState to a non-zero value by
// reflection, so a field added later is covered without editing the test.
func filledReport(t *testing.T) reportState {
	t.Helper()
	var s reportState
	v := reflect.ValueOf(&s).Elem()
	for i := range v.NumField() {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.Pointer:
			f.Set(reflect.New(f.Type().Elem()))
		case reflect.Map:
			m := reflect.MakeMap(f.Type())
			m.SetMapIndex(reflect.New(f.Type().Key()).Elem(), reflect.New(f.Type().Elem()).Elem())
			f.Set(m)
		case reflect.String:
			f.SetString("set")
		default:
			t.Fatalf("reportState.%s has kind %s: teach filledReport to fill it", v.Type().Field(i).Name, f.Kind())
		}
	}
	return s
}

// The run-scoped report has one field list (reportState). Reset, save and
// restore all go through it, so a field cannot be reset but not restored (or
// the reverse).
func TestReportStateIsTheOneListForResetSaveAndRestore(t *testing.T) {
	cfg := config.Default()
	cfg.AllowedHosts = nil
	w, _ := openWired(t, cfg)

	filled := filledReport(t)
	w.restoreReport(filled)
	if got := w.saveReport(); !reflect.DeepEqual(got, filled) {
		t.Fatalf("save after restore does not round-trip every field:\n got %+v\nwant %+v", got, filled)
	}

	// startRun refuses (no allowed hosts) after resetting the report.
	if _, _, _, err := w.startRun(context.Background(), false); err == nil {
		t.Fatal("startRun succeeded with no allowed hosts")
	}
	got := w.saveReport()
	if got.BackendErrs == nil || got.BackendErrs == filled.BackendErrs {
		t.Fatalf("reset did not install a fresh backendErrors: %p", got.BackendErrs)
	}
	v := reflect.ValueOf(got)
	for i := range v.NumField() {
		if name := v.Type().Field(i).Name; name != "BackendErrs" && !v.Field(i).IsZero() {
			t.Errorf("reportState.%s survived the reset: %v", name, v.Field(i).Interface())
		}
	}
}

func seedReport(w *wired) {
	d := previousRunDetail()
	w.mu.Lock()
	w.setReportLocked(reportState{BackendErrs: newBackendErrors(), Boards: d.Boards, Advance: d.Advance})
	w.mu.Unlock()
}
