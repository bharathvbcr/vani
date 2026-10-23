package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// tunedRun is what one fake run saw of the app's run hooks.
type tunedRun struct {
	tunes   [][2]any // (advance, listOnly) per Tune call
	reloads [][]sources.Board
	runs    int
}

// wireTuned installs an App on l that records Tune, Reload and RunOnce.
func wireTuned(t *testing.T, l *memLedger) *tunedRun {
	t.Helper()
	old := buildApp
	t.Cleanup(func() { buildApp = old })
	got := &tunedRun{}
	buildApp = func(config.Config, string, io.Writer) (*App, error) {
		return &App{
			Ledger: l, Notifier: &fakeNotifier{},
			RunOnce: func(context.Context) error { got.runs++; return nil },
			Tune:    func(advance int, listOnly bool) { got.tunes = append(got.tunes, [2]any{advance, listOnly}) },
			Reload:  func(c config.Config) { got.reloads = append(got.reloads, c.Watchlist) },
		}, nil
	}
	return got
}

func addBoards(t *testing.T) {
	t.Helper()
	mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "acme", "--company", "Acme", "--allow-host")
	mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "globex", "--company", "Globex", "--allow-host")
	mustCode(t, 0, "watchlist", "add", "--ats", "ab", "--token", "initech", "--company", "Initech", "--allow-host")
}

// Options no run can honour are usage errors, refused before the app is built:
// the ledger is never opened and no run is recorded.
func TestRunOnceRefusesBadOptions(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	addBoards(t)
	got := wireTuned(t, fixture())
	for _, args := range [][]string{
		{"run-once", "--advance", "-1"},
		{"run-once", "--advance", "5001"},
		{"run-once", "--advance", "abc"},
		{"run-once", "--advance", "99999999999999999999"},
		{"run-once", "--list-only", "--advance", "5"},
		{"run-once", "--board", "acme"},
		{"run-once", "--board", ":acme"},
		{"run-once", "--board", "gh:"},
		{"run-once", "--board", "gh:nobody"},
		{"run-once", "--board", "lv:acme"},
		{"run-once", "--board", "gh:acme", "--board", "gh:nobody"},
		{"run-once", "extra"},
	} {
		if code, _, stderr := do(t, args...); code != exitUsage {
			t.Errorf("vani %v: exit %d, want 2 (stderr %q)", args, code, stderr)
		}
	}
	if got.runs != 0 || len(got.tunes) != 0 {
		t.Errorf("a refused run reached the app: %+v", got)
	}
	if _, err := os.Stat(runRecordPath(stateDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused run wrote a record: %v", err)
	}
}

func TestRunOnceRefusesTooManyBoards(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	args := []string{"run-once"}
	for range maxRunBoards + 1 {
		args = append(args, "--board", "gh:acme")
	}
	mustCode(t, exitUsage, args...)
}

// A plain run-once is a manual run with no overrides: the record says manual,
// carries no options, and the app's advance phase is reset to the config's.
func TestRunOnceRecordsAManualRun(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	got := wireTuned(t, fixture())
	mustCode(t, 0, "run-once")
	r := lastRecord(t, stateDir)
	if r.Trigger != triggerManual || r.Options != nil || r.Outcome != outcomeOK {
		t.Errorf("record trigger=%q options=%+v outcome=%s", r.Trigger, r.Options, r.Outcome)
	}
	if !slices.Equal(got.tunes, [][2]any{{0, false}}) || got.runs != 1 {
		t.Errorf("tunes %v runs %d, want one reset and one run", got.tunes, got.runs)
	}
	if r.DigestRoles == nil {
		t.Error("a run without --no-digest wrote no digest")
	}
}

// The overrides reach the app's advance phase and the record, and status
// --json reports both, so the Pipeline tab can say what the run was.
func TestRunOnceOverridesReachTheAppAndTheRecord(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	addBoards(t)
	got := wireTuned(t, fixture())
	mustCode(t, 0, "run-once", "--advance", "7", "--board", "GH:Globex", "--board", "gh:acme", "--board", "gh:acme")
	if !slices.Equal(got.tunes, [][2]any{{7, false}}) {
		t.Errorf("tunes %v, want [7 false]", got.tunes)
	}
	// Watchlist order, each board once, whatever the flags' order and case.
	if len(got.reloads) != 1 || len(got.reloads[0]) != 2 || got.reloads[0][0].Token != "acme" || got.reloads[0][1].Token != "globex" {
		t.Fatalf("reloads %+v, want acme then globex", got.reloads)
	}
	r := lastRecord(t, stateDir)
	if r.Trigger != triggerManual || r.Options == nil || r.Options.Advance != 7 || len(r.Options.Boards) != 3 {
		t.Errorf("record %q %+v", r.Trigger, r.Options)
	}
	o, _ := mustCode(t, 0, "status", "--json")
	var s statusJSON
	if err := json.Unmarshal([]byte(o), &s); err != nil {
		t.Fatal(err)
	}
	if s.LastRun.Trigger != triggerManual || s.LastRun.Options == nil || s.LastRun.Options.Advance != 7 {
		t.Errorf("status last_run trigger=%q options=%+v", s.LastRun.Trigger, s.LastRun.Options)
	}
	if !strings.Contains(o, `"options": {`) && !strings.Contains(o, `"options":{`) {
		t.Errorf("status --json has no options object: %s", o)
	}
}

func TestRunOnceListOnly(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	got := wireTuned(t, fixture())
	mustCode(t, 0, "run-once", "--list-only")
	if !slices.Equal(got.tunes, [][2]any{{0, true}}) {
		t.Errorf("tunes %v, want [0 true]", got.tunes)
	}
	if r := lastRecord(t, stateDir); r.Options == nil || !r.Options.ListOnly {
		t.Errorf("options %+v", r.Options)
	}
}

// --no-digest writes no digest and marks nothing Digested: the roles stay for
// the next digest, and the record says no digest was written (nil, not 0).
func TestRunOnceNoDigest(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l := fixture()
	wireTuned(t, l)
	before := statusCounts(l.All())
	o, _ := mustCode(t, 0, "run-once", "--no-digest")
	if !strings.Contains(o, "skipped") {
		t.Errorf("stdout %q does not say the digest was skipped", o)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "digests")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("--no-digest wrote a digest: %v", err)
	}
	if after := statusCounts(l.All()); after[string(posting.Digested)] != before[string(posting.Digested)] {
		t.Errorf("--no-digest marked roles digested: %v -> %v", before, after)
	}
	r := lastRecord(t, stateDir)
	if r.Outcome != outcomeOK || r.DigestRoles != nil || r.Options == nil || !r.Options.NoDigest {
		t.Errorf("record outcome=%s digest_roles=%s options=%+v", r.Outcome, intPtrString(r.DigestRoles), r.Options)
	}
}

// An app that cannot be tuned must not quietly run with the config's cap: the
// run fails before the pipeline, and says why.
func TestRunOnceAdvanceWithoutTuneFails(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	runs := wire(t, fixture(), &fakeNotifier{}, nil) // an App with no Tune
	_, e := mustCode(t, 1, "run-once", "--advance", "3")
	if *runs != 0 || !strings.Contains(e, "--advance") {
		t.Errorf("runs %d, stderr %q", *runs, e)
	}
	if r := lastRecord(t, stateDir); r.Outcome != outcomeFailed || r.Stage != stagePipeline {
		t.Errorf("record outcome=%s stage=%q", r.Outcome, r.Stage)
	}
	// A run without overrides still works on such an app.
	mustCode(t, 0, "run-once")
}

// The same for a board selection on an app that cannot reload its boards.
func TestRunOnceBoardsWithoutReloadFails(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	addBoards(t)
	runs := wire(t, fixture(), &fakeNotifier{}, nil)
	mustCode(t, 1, "run-once", "--board", "gh:acme")
	if *runs != 0 {
		t.Errorf("the pipeline ran %d times without its board selection", *runs)
	}
	if r := lastRecord(t, stateDir); r.Outcome != outcomeFailed {
		t.Errorf("record outcome=%s", r.Outcome)
	}
}

// A ledger another process holds (the daemon) refuses a manual run at once,
// and the error says who to wait for rather than only "locked".
func TestRunOnceWhileTheLedgerIsHeld(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	held, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	started := time.Now()
	_, e := mustCode(t, 1, "run-once", "--no-digest")
	if took := time.Since(started); took > 5*time.Second {
		t.Errorf("run-once waited %v on a held ledger", took)
	}
	if !strings.Contains(e, "another vani holds it") || !strings.Contains(e, "daemon") {
		t.Errorf("stderr %q", e)
	}
	if _, err := os.Stat(runRecordPath(stateDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a run that never started wrote a record: %v", err)
	}
}

// The daemon's runs are recorded as scheduled, with no options.
func TestScheduledRunIsRecordedAsSuch(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l := fixture()
	got := &tunedRun{}
	app := &App{Ledger: l, Notifier: &fakeNotifier{}, RunOnce: func(context.Context) error { got.runs++; return nil },
		Tune: func(a int, lo bool) { got.tunes = append(got.tunes, [2]any{a, lo}) }}
	cfg, err := config.Load(os.Getenv("VANI_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{configPath: os.Getenv("VANI_CONFIG"), stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}
	if err := e.dailyJob(app, cfg, triggerSchedule, runOptions{})(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := lastRecord(t, stateDir)
	if r.Trigger != triggerSchedule || r.Options != nil {
		t.Errorf("record trigger=%q options=%+v", r.Trigger, r.Options)
	}
	// A scheduled run resets whatever a manual run in the same process set.
	if !slices.Equal(got.tunes, [][2]any{{0, false}}) {
		t.Errorf("tunes %v", got.tunes)
	}
}

// The selection is cut from the watchlist the reload read, so a board removed
// from the config since the run was asked for stops the run rather than
// listing every board; an unreadable config cuts it from the starting one.
func TestReloadBoardsAppliesTheSelectionAfterTheReload(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	addBoards(t)
	start, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var got [][]sources.Board
	app := &App{Reload: func(c config.Config) { got = append(got, c.Watchlist) }}
	e := &env{configPath: cfgPath, stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}

	if err := e.reloadErr(app, start, []string{"ab:initech"}); err != nil || len(got) != 1 || len(got[0]) != 1 || got[0][0].Token != "initech" {
		t.Fatalf("selection: err %v, reloads %+v", err, got)
	}

	mustCode(t, 0, "watchlist", "remove", "--ats", "ab", "--token", "initech")
	got = nil
	if err := e.reloadErr(app, start, []string{"ab:initech"}); !errors.Is(err, errBoardSelection) || len(got) != 0 {
		t.Errorf("a board gone from the config: err %v, reloads %+v", err, got)
	}

	if err := os.WriteFile(cfgPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = nil
	err = e.reloadErr(app, start, []string{"gh:globex"})
	if !errors.Is(err, errConfigStale) || len(got) != 1 || len(got[0]) != 1 || got[0][0].Token != "globex" {
		t.Errorf("stale config: err %v, reloads %+v", err, got)
	}
}

func TestSelectBoards(t *testing.T) {
	wl := []sources.Board{
		{ATS: "wd", Token: "intel/External"},
		{ATS: "gh", Token: "acme"},
		{ATS: "pg", Token: "a:b"},
	}
	got, err := selectBoards(wl, []string{"gh:ACME", "wd:intel/external", "pg:a:b", "gh:acme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Token != "intel/External" || got[1].Token != "acme" || got[2].Token != "a:b" {
		t.Errorf("got %+v", got)
	}
	for _, refs := range [][]string{{"gh"}, {""}, {" : "}, {"gh:other"}, {"wd:intel"}} {
		if _, err := selectBoards(wl, refs); !errors.Is(err, errBoardSelection) {
			t.Errorf("%q: err %v", refs, err)
		}
	}
	if got, err := selectBoards(nil, nil); err != nil || len(got) != 0 {
		t.Errorf("empty: %+v %v", got, err)
	}
}

// The wired app's advance plan follows the last Tune, and a reset returns it
// to advance_per_run.
func TestWiredAdvancePlan(t *testing.T) {
	w := &wired{cfg: config.Config{AdvancePerRun: 40}}
	if l, s := runPlan(w); l != 40 || s {
		t.Errorf("untuned: %d %v", l, s)
	}
	w.tune(5, false)
	if l, s := runPlan(w); l != 5 || s {
		t.Errorf("--advance 5: %d %v", l, s)
	}
	w.tune(0, true)
	if l, s := runPlan(w); l != 40 || !s {
		t.Errorf("--list-only: %d %v", l, s)
	}
	w.tune(0, false)
	if l, s := runPlan(w); l != 40 || s {
		t.Errorf("reset: %d %v", l, s)
	}
}

// An older vani's record (no trigger, no options) still reads, and status
// prints neither key.
func TestOlderRecordHasNoTrigger(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	at := time.Now().Add(-time.Hour)
	if err := writeRunRecord(stateDir, runRecord{PID: 1 << 30, StartedAt: at, FinishedAt: at.Add(time.Minute), Outcome: outcomeOK}); err != nil {
		t.Fatal(err)
	}
	o, _ := mustCode(t, 0, "status", "--json")
	var raw struct {
		LastRun map[string]json.RawMessage `json:"last_run"`
	}
	if err := json.Unmarshal([]byte(o), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"trigger", "options"} {
		if v, ok := raw.LastRun[k]; ok {
			t.Errorf("last_run.%s = %s on a record without it", k, v)
		}
	}
}
