package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/daemon"
	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// reloadErr is reloadConfig for callers that only want its error.
func (e *env) reloadErr(app *App, cfg config.Config, only []string) error {
	_, err := e.reloadConfig(app, cfg, only)
	return err
}

func digestOptionsForTest() digest.Options { return digest.Options{} }

func writeConfig(t *testing.T, path string, cfg config.Config) {
	t.Helper()
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendLive(t *testing.T, l ledger.Ledger, key, company string) {
	t.Helper()
	p := posting.Posting{Key: posting.Key(key), ATS: "gh", Company: company, Title: "Engineer " + key, Location: "Austin, TX"}
	if err := l.Append(context.Background(), ledger.Event{V: 1, At: time.Now(), Key: p.Key, Status: posting.Live, Reason: "new", Posting: &p}); err != nil {
		t.Fatal(err)
	}
}

func statusOfKey(t *testing.T, l ledger.Ledger, key string) posting.Status {
	t.Helper()
	r, ok := l.Get(posting.Key(key))
	if !ok {
		t.Fatalf("%s is not in the ledger", key)
	}
	return r.Status
}

// lifecycleApp is a fake app over a real wired app: its pipeline applies the
// exclusions the wired config holds when it runs, as runOnce does through
// runner.Policy, so what a run drops is what the reloaded config says.
func lifecycleApp(t *testing.T, cfg config.Config) (*wired, *App) {
	t.Helper()
	w, _ := openWired(t, cfg)
	return w, &App{
		Ledger:   w.led,
		Notifier: &fakeNotifier{},
		Reload:   w.reload,
		RunOnce: func(ctx context.Context) error {
			w.mu.Lock()
			policy := dropPolicy(w.cfg, "")
			w.mu.Unlock()
			return w.applyExclusions(ctx, policy, nil, time.Now(), &pipeline.FailOpen{})
		},
	}
}

// Exclusions saved while the daemon runs apply to its next run. A schedule
// change does not: this process keeps the schedule it started with and returns
// errConfigRestart instead of recording a failed run. A config that cannot be
// read keeps the exclusions the previous run applied, not the ones the daemon
// started with.
func TestDailyRunsApplyConfigEditedBetweenRuns(t *testing.T) {
	for _, bad := range []struct {
		name   string
		break_ func(t *testing.T, path string)
	}{
		{"malformed", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"deleted mid-run", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(bad.name, func(t *testing.T) {
			cfgPath, stateDir := setup(t)
			mustCode(t, 0, "init")
			start, err := config.Load(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			w, app := lifecycleApp(t, start)
			var stderr bytes.Buffer
			e := &env{configPath: cfgPath, stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &stderr}
			job := e.dailyJob(app, start, triggerSchedule, runOptions{})
			appendLive(t, w.led, "gh:a:1", "Acme")

			// Run 1: nothing is excluded.
			if err := job(context.Background()); err != nil {
				t.Fatalf("run 1: %v", err)
			}
			if got := statusOfKey(t, w.led, "gh:a:1"); got != posting.Live {
				t.Fatalf("run 1: gh:a:1 is %s, want it kept (nothing excludes it yet)", got)
			}

			// An exclusion saved while the daemon runs applies on the next run.
			appendLive(t, w.led, "gh:a:2", "Acme")
			edited := start
			edited.Exclude.Companies = []string{"Acme"}
			writeConfig(t, cfgPath, edited)
			if err := job(context.Background()); err != nil {
				t.Fatalf("run 2: %v", err)
			}
			if got := statusOfKey(t, w.led, "gh:a:2"); got != posting.Dropped {
				t.Errorf("run 2: the Acme role is %s, want it dropped by the exclusion saved since run 1", got)
			}

			// The schedule is fixed for this process. Changing it is not a run.
			edited.Schedule.DailyAt = "03:15"
			writeConfig(t, cfgPath, edited)
			err = job(context.Background())
			if !errors.Is(err, errConfigRestart) {
				t.Fatalf("schedule change: %v, want errConfigRestart", err)
			}
			if w.cfg.Schedule.DailyAt != start.Schedule.DailyAt {
				t.Errorf("the schedule is %q after a reload, want the process-lifetime %q", w.cfg.Schedule.DailyAt, start.Schedule.DailyAt)
			}
			if len(w.cfg.Exclude.Companies) != 1 {
				t.Error("the exclusion applied with the schedule change was dropped")
			}
			if !strings.Contains(stderr.String(), "schedule") || !strings.Contains(stderr.String(), "restart") {
				t.Errorf("a schedule change restarts the daemon and must say so on stderr:\n%s", stderr.String())
			}
			if r := lastRecord(t, stateDir); r.Outcome != outcomeOK {
				t.Errorf("a schedule change wrote outcome %s; it is not a run", r.Outcome)
			}
			// The file still differs from the value this process runs, so another
			// reload in this process reports it again. A process that loaded the
			// edited file as its start config does not.
			if _, err := e.reloadConfig(app, start, nil); !errors.Is(err, errConfigRestart) {
				t.Fatalf("same process, file still differs: %v", err)
			}
			loaded, lerr := config.Load(cfgPath)
			if lerr != nil {
				t.Fatal(lerr)
			}
			freshErr := &bytes.Buffer{}
			fresh := &env{configPath: cfgPath, stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: freshErr}
			got, lerr := fresh.reloadConfig(&App{Reload: func(config.Config) {}}, loaded, nil)
			if lerr != nil || got.Schedule.DailyAt != "03:15" {
				t.Fatalf("fresh process: %v, daily_at %q, stderr %s", lerr, got.Schedule.DailyAt, freshErr)
			}

			// Run 3: the config cannot be read. The previous run's exclusions stay.
			appendLive(t, w.led, "gh:a:3", "Acme")
			appendLive(t, w.led, "gh:a:4", "Initech")
			bad.break_(t, cfgPath)
			err = job(context.Background())
			if !errors.Is(err, errConfigStale) {
				t.Fatalf("run 3: %v, want errConfigStale", err)
			}
			if got := statusOfKey(t, w.led, "gh:a:3"); got != posting.Dropped {
				t.Errorf("run 3: the Acme role is %s, want the previous exclusions kept", got)
			}
			if got := statusOfKey(t, w.led, "gh:a:4"); got == posting.Dropped {
				t.Errorf("run 3: the Initech role was dropped by a policy nobody wrote")
			}
			if r := lastRecord(t, stateDir); r.Outcome != outcomeFailed || r.Cause != causeConfigStale {
				t.Errorf("record outcome=%s cause=%q, want failed config_stale", r.Outcome, r.Cause)
			}
		})
	}
}

// The config a stale run falls back to is the last one a run applied.
func TestStaleReloadFallsBackToTheLastAppliedConfig(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	start, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{configPath: cfgPath, stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	app := &App{Reload: func(config.Config) {}}
	edited := start
	edited.Exclude.Companies = []string{"Acme"}
	writeConfig(t, cfgPath, edited)
	got, err := e.reloadConfig(app, start, nil)
	if err != nil || len(got.Exclude.Companies) != 1 {
		t.Fatalf("reload: %v, %+v", err, got.Exclude)
	}
	if err := os.WriteFile(cfgPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = e.reloadConfig(app, start, nil)
	if !errors.Is(err, errConfigStale) || len(got.Exclude.Companies) != 1 {
		t.Fatalf("stale reload: %v, exclusions %+v; want errConfigStale and the exclusions of the last good reload", err, got.Exclude)
	}
}

// The inbox poll reads the config as a run does.
func TestInboxPollReloadsTheConfig(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	start, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded []config.Config
	polls := 0
	app := &App{
		Reload:    func(c config.Config) { reloaded = append(reloaded, c) },
		PollInbox: func(context.Context) error { polls++; return nil },
	}
	e := &env{configPath: cfgPath, stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	edited := start
	edited.Exclude.Companies = []string{"Acme"}
	writeConfig(t, cfgPath, edited)
	if err := e.inboxPoll(app, start)(context.Background()); err != nil || polls != 1 {
		t.Fatalf("poll: %v, %d polls", err, polls)
	}
	if len(reloaded) != 1 || len(reloaded[0].Exclude.Companies) != 1 {
		t.Fatalf("reloads %+v, want the edited config before the poll", reloaded)
	}
	// An unreadable config is said, and the poll still runs on the old one.
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	if err := e.inboxPoll(app, start)(context.Background()); err != nil || polls != 2 || len(reloaded) != 1 {
		t.Fatalf("stale poll: %v, %d polls, %d reloads", err, polls, len(reloaded))
	}
}

// A schedule change between polls is not a poll failure: the poll does not run
// and the daemon is asked to exit.
func TestInboxPollStopsWhenTheScheduleChanges(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	start, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	polls := 0
	var stderr bytes.Buffer
	app := &App{
		Reload:    func(config.Config) {},
		PollInbox: func(context.Context) error { polls++; return nil },
	}
	e := &env{configPath: cfgPath, stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &stderr}
	edited := start
	edited.Schedule.DailyAt = "04:45"
	writeConfig(t, cfgPath, edited)
	err = e.inboxPoll(app, start)(context.Background())
	if !errors.Is(err, errConfigRestart) || polls != 0 {
		t.Fatalf("poll: %v, polls %d; a schedule change must not run the poll", err, polls)
	}
	if strings.Contains(stderr.String(), "inbox poll:") || !strings.Contains(stderr.String(), "restart") {
		t.Fatalf("stderr:\n%s", stderr.String())
	}
}

// The inbox loop returns a process-lifetime change and a fatal poll, and a
// plain poll error is logged and the loop continues.
func TestPollInboxLoopReturnsRestartAndFatal(t *testing.T) {
	old := inboxPollInterval
	inboxPollInterval = 15 * time.Millisecond
	t.Cleanup(func() { inboxPollInterval = old })

	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	start, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	polls := 0
	app := &App{
		Reload:    func(config.Config) {},
		PollInbox: func(context.Context) error { polls++; return nil },
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	var stderr bytes.Buffer
	e := &env{configPath: cfgPath, stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &stderr}
	e.bindRestart(cancel)
	edited := start
	edited.Schedule.DailyAt = "04:45"
	writeConfig(t, cfgPath, edited)
	err = e.pollInboxLoop(ctx, e.inboxPoll(app, start), new(sync.Mutex), nil)
	if !errors.Is(err, errConfigRestart) || polls != 0 {
		t.Fatalf("loop: %v, polls %d", err, polls)
	}
	if !errors.Is(context.Cause(ctx), errConfigRestart) {
		t.Fatalf("cause %v", context.Cause(ctx))
	}
	cancel(nil)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	err = e.pollInboxLoop(ctx, func(context.Context) error { return daemon.ErrFatal }, new(sync.Mutex), nil)
	if !errors.Is(err, daemon.ErrFatal) {
		t.Fatalf("fatal poll: %v", err)
	}
}

// Cancelling the daemon for a schedule change writes no failed run. The
// exclusion in the same file still reaches the app.
func TestScheduleChangeCancelsWithoutARunRecord(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	start, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	w, app := lifecycleApp(t, start)
	var stderr bytes.Buffer
	e := &env{configPath: cfgPath, stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &stderr}
	ctx, cancel := context.WithCancelCause(context.Background())
	e.bindRestart(cancel)
	job := e.dailyJob(app, start, triggerSchedule, runOptions{})
	if err := job(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	edited := start
	edited.Schedule.DailyAt = "03:15"
	edited.Exclude.Companies = []string{"Acme"}
	writeConfig(t, cfgPath, edited)
	if err := job(ctx); err != nil {
		t.Fatalf("restart returns nil once the daemon is cancelled, got %v", err)
	}
	if !errors.Is(context.Cause(ctx), errConfigRestart) {
		t.Fatalf("cause %v", context.Cause(ctx))
	}
	if w.cfg.Schedule.DailyAt != start.Schedule.DailyAt {
		t.Fatalf("schedule %q", w.cfg.Schedule.DailyAt)
	}
	if len(w.cfg.Exclude.Companies) != 1 {
		t.Fatal("exclusion was not applied before the restart")
	}
	if r := lastRecord(t, stateDir); r.Outcome != outcomeOK {
		t.Fatalf("record outcome %s, want the earlier run left as ok", r.Outcome)
	}
	if strings.Contains(stderr.String(), "job failed") {
		t.Fatalf("stderr:\n%s", stderr.String())
	}
}

// A change that lands after the pre-check, during the run's own reload, is
// still not a run: the pipeline does not start and the previous record stays.
func TestScheduleChangeFoundByTheRunReloadLeavesTheLastRecord(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	start, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	w, app := lifecycleApp(t, start)
	flip := false
	orig := app.Reload
	app.Reload = func(cfg config.Config) {
		orig(cfg)
		if !flip {
			return
		}
		flip = false
		edited := start
		edited.Schedule.DailyAt = "03:15"
		writeConfig(t, cfgPath, edited)
	}
	runs := 0
	once := app.RunOnce
	app.RunOnce = func(ctx context.Context) error {
		runs++
		return once(ctx)
	}
	var stderr bytes.Buffer
	e := &env{configPath: cfgPath, stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &stderr}
	ctx, cancel := context.WithCancelCause(context.Background())
	e.bindRestart(cancel)
	job := e.dailyJob(app, start, triggerSchedule, runOptions{})
	if err := job(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if runs != 1 {
		t.Fatalf("first run executed %d times", runs)
	}
	first := lastRecord(t, stateDir)
	flip = true
	if err := job(ctx); err != nil {
		t.Fatalf("restart returns nil once the daemon is cancelled, got %v", err)
	}
	if runs != 1 {
		t.Fatalf("the pipeline ran on a config restart: %d", runs)
	}
	if !errors.Is(context.Cause(ctx), errConfigRestart) {
		t.Fatalf("cause %v", context.Cause(ctx))
	}
	if w.cfg.Schedule.DailyAt != start.Schedule.DailyAt {
		t.Fatalf("schedule %q", w.cfg.Schedule.DailyAt)
	}
	got := lastRecord(t, stateDir)
	if got.Outcome != outcomeOK || got.StartedAt != first.StartedAt {
		t.Fatalf("record outcome %s started %s, want the earlier run left as ok at %s", got.Outcome, got.StartedAt, first.StartedAt)
	}
	if strings.Contains(stderr.String(), "job failed") {
		t.Fatalf("stderr:\n%s", stderr.String())
	}
}

// A1-3: the digest's ranking warning reaches stderr and the run record.
func TestDigestWarningIsPrintedAndRecorded(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RankWeightsFile = filepath.Join(filepath.Dir(cfgPath), "no-such-weights.json")
	writeConfig(t, cfgPath, cfg)
	wire(t, fixture(), &fakeNotifier{}, nil)
	stdout, stderr := mustCode(t, 0, "run-once")
	if !strings.Contains(stderr, "rank_weights_file") {
		t.Errorf("stderr does not carry the digest warning:\n%s", stderr)
	}
	if r := lastRecord(t, stateDir); !strings.Contains(r.DigestWarning, "rank_weights_file") {
		t.Errorf("record digest_warning = %q", r.DigestWarning)
	}
	raw, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil || !strings.Contains(string(raw), `"digest_warning"`) {
		t.Errorf("runs/last.json has no digest_warning: %v\n%s", err, raw)
	}
	if !strings.Contains(stdout, "digest:") {
		t.Errorf("the digest was not written:\n%s", stdout)
	}
}

// A run without a warning carries no digest_warning (additive field only).
func TestNoDigestWarningWhenRankedAsConfigured(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	wire(t, fixture(), &fakeNotifier{}, nil)
	mustCode(t, 0, "run-once")
	raw, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil || strings.Contains(string(raw), "digest_warning") {
		t.Errorf("a clean run recorded a warning: %v\n%s", err, raw)
	}
}

// runPanicking runs job and reports a panic that escaped it instead of
// crashing the test binary.
func runPanicking(t *testing.T, job daemon.Job, ctx context.Context) (err error) {
	t.Helper()
	defer func() {
		if p := recover(); p != nil {
			t.Errorf("a panic escaped the job: %v", p)
		}
	}()
	return job(ctx)
}

// A1-4: a pipeline that panics is a failed run with a notice, not a record
// stuck at "running". A panic in a goroutine the pipeline started is not
// covered: recover reaches only the goroutine that deferred it.
func TestPanickingRunIsRecordedAndAnnounced(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	n := &fakeNotifier{}
	app := &App{Ledger: fixture(), Notifier: n, RunOnce: func(context.Context) error { panic("boom: https://leak.example") }}
	e := &env{configPath: os.Getenv("VANI_CONFIG"), stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	err := runPanicking(t, e.dailyJob(app, config.Default(), triggerSchedule, runOptions{}), context.Background())
	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("returned %v, want an error naming the panic", err)
	}
	r := lastRecord(t, stateDir)
	if r.Outcome != outcomeFailed || !strings.Contains(r.Error, "panic") || r.FinishedAt.IsZero() {
		t.Errorf("record outcome=%s error=%q finished=%v, want a finished failed run naming the panic", r.Outcome, r.Error, r.FinishedAt)
	}
	if len(n.calls) != 1 || !strings.HasPrefix(n.calls[0], "Vani run failed|") || strings.Contains(n.calls[0], "leak.example") {
		t.Errorf("notifications %v, want one failure notice without the panic text", n.calls)
	}
}

// Cancelling while the panic unwinds does not turn it into a quiet "cancelled".
func TestPanicDuringCancelIsStillAFailure(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	n := &fakeNotifier{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app := &App{Ledger: fixture(), Notifier: n, RunOnce: func(context.Context) error { cancel(); panic("late") }}
	e := &env{configPath: os.Getenv("VANI_CONFIG"), stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	if err := runPanicking(t, e.dailyJob(app, config.Default(), triggerSchedule, runOptions{}), ctx); err == nil {
		t.Fatal("no error")
	}
	if r := lastRecord(t, stateDir); r.Outcome != outcomeFailed || !strings.Contains(r.Error, "panic") {
		t.Errorf("record outcome=%s error=%q, want failed with the panic", r.Outcome, r.Error)
	}
	if len(n.calls) != 1 {
		t.Errorf("notifications %v, want the failure notice", n.calls)
	}
}

// The daemon keeps its schedule after a run panics.
func TestDaemonSurvivesAPanickingRun(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	l := &brokenLedger{memLedger: fixture()}
	runs := 0
	wireDaemon(t, l, func(context.Context) error {
		runs++
		if runs == 1 {
			panic("first run")
		}
		l.poison(errors.New("stop the daemon"))
		return errors.New("stop")
	})
	mustCode(t, exitRuntime, "daemon")
	if runs != 2 {
		t.Errorf("%d runs, want the second one after the panic", runs)
	}
}

// A1-10: a fatal ledger error that arrives joined with a cancellation is the
// run's cause, and its notice is posted.
func TestFatalUnderCancelIsFailedWithItsCause(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	n := &fakeNotifier{}
	noticeApp(t, n, errors.Join(context.Canceled, fmt.Errorf("%w: fsync", daemon.ErrFatal)), nil, nil)
	mustCode(t, 1, "run-once")
	if r := lastRecord(t, stateDir); r.Outcome != outcomeFailed || r.Cause != causeLedgerFatal {
		t.Errorf("record outcome=%s cause=%q, want failed ledger_fatal", r.Outcome, r.Cause)
	}
	if len(n.calls) != 1 {
		t.Errorf("notifications %v, want the failure notice", n.calls)
	}
}

// countingLedger counts the durable writes a caller makes: each Append and each
// AppendBatch is one fsync in ledger.File.
type countingLedger struct {
	*memLedger
	appends, batches, events, largest int
}

func (c *countingLedger) Append(ctx context.Context, e ledger.Event) error {
	c.appends++
	c.events++
	return c.memLedger.Append(ctx, e)
}

func (c *countingLedger) AppendBatch(ctx context.Context, evs []ledger.Event) error {
	c.batches++
	c.events += len(evs)
	c.largest = max(c.largest, len(evs))
	return c.memLedger.AppendBatch(ctx, evs)
}

func (c *countingLedger) fsyncs() int { return c.appends + c.batches }

func bigLedger(n int) *memLedger {
	at := time.Date(2026, 10, 18, 15, 0, 0, 0, time.UTC)
	m := &memLedger{recs: map[posting.Key]*ledger.Record{}}
	for i := range n {
		k := posting.Key(fmt.Sprintf("gh:a:%d", i))
		m.recs[k] = &ledger.Record{Key: k, Status: posting.Explained, FirstSeen: at, LastEvent: at,
			Posting:     posting.Posting{Company: "Acme", Title: fmt.Sprintf("Eng %d", i), URL: fmt.Sprintf("https://x.example/%d", i)},
			Annotations: map[string]string{"score.total": "70"}}
	}
	return m
}

// A2-2: marking a digest's roles Digested costs one fsync per 4,096 events.
func TestPublishDigestMarksInBatches(t *testing.T) {
	_, stateDir := setup(t)
	l := &countingLedger{memLedger: bigLedger(2000)}
	e := &env{stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	loc := time.UTC
	pub, err := e.publishDigest(context.Background(), &App{Ledger: l}, time.Date(2026, 10, 18, 16, 0, 0, 0, loc), loc, digestOptionsForTest())
	if err != nil || pub.roles == nil {
		t.Fatalf("publish: %v, roles %v", err, pub.roles)
	}
	if *pub.roles < 1000 {
		t.Fatalf("the digest listed %d roles; the test needs a large one", *pub.roles)
	}
	if l.events != *pub.roles {
		t.Errorf("%d events for %d roles", l.events, *pub.roles)
	}
	if want := (l.events + ledger.MaxBatchEvents - 1) / ledger.MaxBatchEvents; l.fsyncs() > want || l.largest > ledger.MaxBatchEvents {
		t.Errorf("%d fsyncs (%d appends, %d batches, largest %d) for %d events, want at most %d", l.fsyncs(), l.appends, l.batches, l.largest, l.events, want)
	}
	for k, r := range l.recs {
		if r.Status != posting.Digested {
			t.Fatalf("%s is %s, want digested", k, r.Status)
		}
	}
}

// A poisoned ledger fails the marking once and writes nothing.
func TestPublishDigestOnAPoisonedLedgerReturnsOneError(t *testing.T) {
	_, stateDir := setup(t)
	m := bigLedger(300)
	m.fail = true
	l := &countingLedger{memLedger: m}
	e := &env{stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	loc := time.UTC
	pub, err := e.publishDigest(context.Background(), &App{Ledger: l}, time.Date(2026, 10, 18, 16, 0, 0, 0, loc), loc, digestOptionsForTest())
	if err == nil {
		t.Fatal("no error from a ledger that refuses every write")
	}
	if u, ok := err.(interface{ Unwrap() []error }); ok && len(u.Unwrap()) != 1 {
		t.Errorf("%d joined errors, want exactly one: %v", len(u.Unwrap()), err)
	}
	for k, r := range m.recs {
		if r.Status == posting.Digested {
			t.Fatalf("%s was marked on a poisoned ledger", k)
		}
	}
	if pub.roles == nil {
		t.Error("the digest was written, so its size is still recorded")
	}
	if l.fsyncs() != 1 {
		t.Errorf("%d write attempts on a poisoned ledger, want 1", l.fsyncs())
	}
}

// ft-3d83: the daily run's semantic warm still scores a marked closed job
// whose text the ledger evicted from memory.
func TestDailyWarmGetsClosedMarkedRecordsWithTheirText(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Semantic.Snapshot, cfg.KB.Bank = "snapshot", "bank"
	dir := filepath.Join(t.TempDir(), "ledger")
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	key := posting.Key("gh:acme:9")
	p := posting.Posting{Key: key, ATS: key.ATS(), Company: "Acme", Title: "Closed role", Description: "the full job text"}
	for _, ev := range []ledger.Event{
		{V: 1, At: jobsT0, Key: key, Status: posting.Live, Posting: &p},
		{V: 1, At: jobsT0, Key: key, Status: posting.Dead},
	} {
		if err := l.Append(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if l, err = ledger.Open(dir); err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if r := l.All()[0]; !r.BodyEvicted {
		t.Fatal("setup: the closed record's text is resident")
	}
	if _, err := marks.SetMark(marks.Dir(stateDir), key, marks.Saved, jobsT0); err != nil {
		t.Fatal(err)
	}
	var seen []ledger.Record
	old := warmSemanticRun
	warmSemanticRun = func(_ context.Context, _ config.Config, _ string, recs []ledger.Record, _ map[posting.Key]marks.Mark, _ int) semanticReport {
		seen = recs
		return semanticReport{Ran: true}
	}
	t.Cleanup(func() { warmSemanticRun = old })
	e := &env{configPath: cfgPath, stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	app := &App{Ledger: l, RunOnce: func(context.Context) error { return nil }}
	if _, err := e.runAndPublish(context.Background(), app, cfg, runOptions{}, &runRecord{}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].Posting.Description != "the full job text" || seen[0].BodyEvicted {
		t.Fatalf("warm got %+v, want the marked closed job with its description", seen)
	}
	docs := warmDocs(seen, map[posting.Key]marks.Mark{key: marks.Saved})
	if len(docs) != 1 {
		t.Fatalf("warmDocs kept %d of the records, want the marked closed job", len(docs))
	}
}
