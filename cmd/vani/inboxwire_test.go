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
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/inbox"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// spoolBatch queues n jobs of Acme under token, as `jobs ingest` would.
func spoolBatch(t *testing.T, stateDir, token string, titles ...string) {
	t.Helper()
	var jobs []inbox.Job
	for i, title := range titles {
		jobs = append(jobs, inbox.Job{Title: title, Company: "Acme", Location: "Remote", URL: "https://careers.acme.com/jobs/" + string(rune('a'+i)) + title[:1]})
	}
	if _, err := inbox.Write(filepath.Join(stateDir, inbox.Dir), inbox.Batch{Token: token, Company: "Acme", Jobs: jobs}); err != nil {
		t.Fatal(err)
	}
}

func openWired(t *testing.T, cfg config.Config) (*wired, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	led, err := ledger.Open(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	warn := &bytes.Buffer{}
	return &wired{cfg: cfg, stateDir: dir, led: led, warn: warn}, warn
}

func TestPollInboxFoldsAndAdvancesOnlyTheNewLiveKeys(t *testing.T) {
	cfg := config.Default()
	w, warn := openWired(t, cfg)
	// An ATS record already holds the Platform Engineer role.
	gh, _ := posting.GreenhouseKey("acme", "9")
	if err := w.led.Append(context.Background(), ledger.Event{V: 1, At: time.Now(), Key: gh, Status: posting.Live, Reason: "new",
		Posting: &posting.Posting{Key: gh, ATS: posting.Greenhouse, Company: "Acme", Title: "Platform Engineer", Location: "Remote", URL: "https://boards.greenhouse.io/acme/jobs/9"}}); err != nil {
		t.Fatal(err)
	}
	var got [][]ledger.Record
	testHookAdvanceNew = func(_ context.Context, recs []ledger.Record) error { got = append(got, recs); return nil }
	t.Cleanup(func() { testHookAdvanceNew = nil })

	if err := w.pollInbox(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("an empty inbox: err %v, advanced %d batches", err, len(got))
	}
	spoolBatch(t, w.stateDir, "careers-acme-com", "Platform Engineer", "Data Engineer")
	if err := w.pollInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0]) != 1 || got[0][0].Posting.Title != "Data Engineer" || got[0][0].Status != posting.Live {
		t.Fatalf("advanced %+v; want only the Data Engineer (the other repeats the ATS record)", got)
	}
	if !strings.Contains(warn.String(), "folded 1 batch(es), 2 job(s): 1 new, 1 duplicate") {
		t.Errorf("warnings: %s", warn.String())
	}
	if n, _ := inbox.Pending(filepath.Join(w.stateDir, inbox.Dir)); n != 0 {
		t.Errorf("%d files left", n)
	}
	// Folded twice is folded once: a repeat poll has nothing to advance.
	if err := w.pollInbox(context.Background()); err != nil || len(got) != 1 {
		t.Fatalf("second poll: %v, %d advances", err, len(got))
	}
}

func TestPollInboxAdvanceFailureIsReportedAndRecordsStayLive(t *testing.T) {
	w, _ := openWired(t, config.Default())
	spoolBatch(t, w.stateDir, "careers-acme-com", "Data Engineer")
	boom := errors.New("backends down")
	testHookAdvanceNew = func(context.Context, []ledger.Record) error { return boom }
	t.Cleanup(func() { testHookAdvanceNew = nil })
	if err := w.pollInbox(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	for _, r := range w.led.All() {
		if r.Status != posting.Live {
			t.Errorf("%s is %s", r.Key, r.Status)
		}
	}
}

// With no decision backend the poll cannot advance: it says so and leaves the
// folded jobs Live for the daily run, without failing.
func TestPollInboxWithoutBackendLeavesJobsLive(t *testing.T) {
	clearKeys(t)
	cfg := config.Default()
	cfg.Decide.OpenRouterModel = ""
	cfg.LLM.Provider = ""
	w, warn := openWired(t, cfg)
	spoolBatch(t, w.stateDir, "careers-acme-com", "Data Engineer")
	if err := w.pollInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	recs := w.led.All()
	if len(recs) != 1 || recs[0].Status != posting.Live || !strings.Contains(warn.String(), "wait for the next run") {
		t.Fatalf("records %+v\nwarnings: %s", recs, warn.String())
	}
}

func TestPollInboxCancelledKeepsTheBatch(t *testing.T) {
	w, _ := openWired(t, config.Default())
	spoolBatch(t, w.stateDir, "careers-acme-com", "Data Engineer")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	testHookAdvanceNew = func(context.Context, []ledger.Record) error { t.Error("advanced after cancel"); return nil }
	t.Cleanup(func() { testHookAdvanceNew = nil })
	_ = w.pollInbox(ctx)
	if n, _ := inbox.Pending(filepath.Join(w.stateDir, inbox.Dir)); n != 1 || len(w.led.All()) != 0 {
		t.Fatalf("%d files left, %d records", n, len(w.led.All()))
	}
}

func TestPollInboxMovesCorruptFilesAside(t *testing.T) {
	w, warn := openWired(t, config.Default())
	dir := filepath.Join(w.stateDir, inbox.Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "00000000000000000001-x.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	testHookAdvanceNew = func(context.Context, []ledger.Record) error { t.Error("advanced nothing"); return nil }
	t.Cleanup(func() { testHookAdvanceNew = nil })
	if err := w.pollInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, inbox.BadDir, "00000000000000000001-x.json")); err != nil {
		t.Errorf("not moved to bad/: %v", err)
	}
	if !strings.Contains(warn.String(), "unreadable file(s) moved to") {
		t.Errorf("warnings: %s", warn.String())
	}
	if n, _ := inbox.Pending(dir); n != 0 {
		t.Errorf("still pending: %d", n)
	}
}

// A daily run drains the inbox before it reads boards. The advance limit is
// invalid on purpose (a config that skipped Validate), so the run ends in
// advanceAll's own refusal after the drain, before any model is asked.
func TestRunOnceDrainsTheInboxBeforeTheBoards(t *testing.T) {
	clearKeys(t)
	t.Setenv("GEMINI_API_KEY", "test-not-a-real-key")
	cfg := defaultWithLLM()
	cfg.Decide.OpenRouterModel = ""
	cfg.AdvancePerRun = -1
	w, warn := openWired(t, cfg)
	spoolBatch(t, w.stateDir, "careers-acme-com", "Data Engineer", "Platform Engineer")
	err := w.runOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "advance limit") {
		t.Fatalf("err = %v\n%s", err, warn.String())
	}
	recs := w.led.All()
	if len(recs) != 2 {
		t.Fatalf("%d records, want the 2 spooled jobs\n%s", len(recs), warn.String())
	}
	for _, r := range recs {
		if r.Status != posting.Live || !strings.HasPrefix(string(r.Key), "pg:careers-acme-com:") {
			t.Errorf("%s %s", r.Key, r.Status)
		}
	}
	if !strings.Contains(warn.String(), "inbox: folded 1 batch(es)") {
		t.Errorf("warnings: %s", warn.String())
	}
	// No page board is on the watchlist, so the scraped jobs were never aged.
	for _, r := range recs {
		if r.MissedRuns != 0 {
			t.Errorf("%s missed %d", r.Key, r.MissedRuns)
		}
	}
}

func TestSetBoardsReplacesWatchlistAndHosts(t *testing.T) {
	w, _ := openWired(t, config.Default())
	w.setBoards([]sources.Board{{ATS: posting.Ashby, Token: "x", Company: "X"}}, []string{"api.ashbyhq.com"})
	if len(w.cfg.Watchlist) != 1 || len(w.cfg.AllowedHosts) != 1 || w.cfg.Schedule.DailyAt == "" {
		t.Fatalf("%+v", w.cfg)
	}
}

// wireReload swaps buildApp for one whose app records each Reload and runs run
// as the pipeline.
func wireReload(t *testing.T, l *brokenLedger, reloads *[][]sources.Board, run func(context.Context) error) {
	t.Helper()
	old, oldNow, oldClock := buildApp, now, daemonClock
	t.Cleanup(func() { buildApp, now, daemonClock = old, oldNow, oldClock })
	clock := &jumpClock{t: time.Date(2026, 10, 18, 6, 0, 0, 0, time.UTC)}
	daemonClock, now = clock, clock.Now
	buildApp = func(config.Config, string, io.Writer) (*App, error) {
		return &App{Ledger: l, Notifier: &fakeNotifier{}, RunOnce: run,
			Reload: func(wl []sources.Board, _ []string) { *reloads = append(*reloads, wl) }}, nil
	}
}

// A board saved while the daemon runs is read by the next daily run.
func TestDailyRunRereadsTheWatchlist(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	l := &brokenLedger{memLedger: fixture()}
	var reloads [][]sources.Board
	runs := 0
	wireReload(t, l, &reloads, func(context.Context) error {
		runs++
		if runs == 1 { // the user saves a search while the first run is going
			mustCode(t, 0, "watchlist", "add", "--ats", "ab", "--token", "initech", "--company", "Initech", "--allow-host")
			return nil
		}
		l.poison(errors.New("stop the daemon"))
		return errors.New("stop")
	})
	mustCode(t, exitRuntime, "daemon")
	if len(reloads) != 2 || len(reloads[0]) != 0 || len(reloads[1]) != 1 || reloads[1][0].Token != "initech" {
		t.Fatalf("reloads %+v; want none on the first run and initech on the second", reloads)
	}
}

// A config that cannot be read keeps the previous watchlist, still runs, and
// fails the run loudly instead of swallowing the problem.
func TestReloadFailureIsLoudAndKeepsThePreviousBoards(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	l := &brokenLedger{memLedger: fixture()}
	var reloads [][]sources.Board
	runs := 0
	wireReload(t, l, &reloads, func(context.Context) error { runs++; return nil })
	old := buildApp
	buildApp = func(c config.Config, s string, w io.Writer) (*App, error) {
		if err := os.WriteFile(cfgPath, []byte("{not json"), 0o600); err != nil { // after the CLI loaded it
			t.Fatal(err)
		}
		return old(c, s, w)
	}
	stdout, stderr := mustCode(t, exitRuntime, "run-once")
	if runs != 1 || len(reloads) != 0 {
		t.Fatalf("runs %d, reloads %d: the run must go on without applying a bad config", runs, len(reloads))
	}
	if !strings.Contains(stderr, "config reload failed") || !strings.Contains(stderr, "previous watchlist") {
		t.Errorf("stderr does not say the config was stale:\n%s", stderr)
	}
	if !strings.Contains(stdout, "digest:") {
		t.Errorf("the digest was skipped:\n%s", stdout)
	}
	rec, err := readRunRecord(stateDir)
	if err != nil || rec == nil || rec.Outcome != outcomeFailed || !strings.Contains(rec.Error, "config reload failed") {
		t.Fatalf("run record %+v, %v", rec, err)
	}
	// The digest was written, so the record names the stale config as the
	// cause, at stage digest, with the digest's size (failureNotice: "Vani
	// config not reloaded", not an unfinished digest).
	if rec.Cause != causeConfigStale || rec.Stage != stageDigest || rec.DigestRoles == nil {
		t.Errorf("cause %q, stage %q, digest_roles %v: want %q at %q with the digest's size", rec.Cause, rec.Stage, rec.DigestRoles, causeConfigStale, stageDigest)
	}
}

// The poll never overlaps the daily run, runs while the daemon is up, and is
// finished before the app (and its ledger) closes.
func TestDaemonPollsTheInboxSerialisedWithTheDailyRun(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	oldInterval := inboxPollInterval
	inboxPollInterval = 3 * time.Millisecond
	t.Cleanup(func() { inboxPollInterval = oldInterval })

	l := &brokenLedger{memLedger: fixture()}
	var busy, overlap, polls atomic.Int32
	var closedWhilePolling atomic.Bool
	runs := 0
	run := func(context.Context) error {
		runs++
		busy.Add(1)
		time.Sleep(25 * time.Millisecond)
		busy.Add(-1)
		if runs == 3 {
			l.poison(errors.New("stop the daemon"))
			return errors.New("stop")
		}
		return nil
	}
	var inPoll atomic.Bool
	old, oldNow, oldClock := buildApp, now, daemonClock
	t.Cleanup(func() { buildApp, now, daemonClock = old, oldNow, oldClock })
	clock := &jumpClock{t: time.Date(2026, 10, 18, 6, 0, 0, 0, time.UTC)}
	daemonClock, now = clock, clock.Now
	buildApp = func(config.Config, string, io.Writer) (*App, error) {
		return &App{Ledger: l, Notifier: &fakeNotifier{}, RunOnce: run,
			PollInbox: func(context.Context) error {
				inPoll.Store(true)
				defer inPoll.Store(false)
				if busy.Load() != 0 {
					overlap.Add(1)
				}
				polls.Add(1)
				time.Sleep(time.Millisecond)
				return nil
			},
			Closer: func() error {
				if inPoll.Load() {
					closedWhilePolling.Store(true)
				}
				return nil
			}}, nil
	}
	mustCode(t, exitRuntime, "daemon")
	if polls.Load() == 0 {
		t.Error("the inbox was never polled")
	}
	if overlap.Load() != 0 {
		t.Errorf("%d polls overlapped a daily run", overlap.Load())
	}
	if closedWhilePolling.Load() {
		t.Error("the app closed while a poll was running")
	}
}

func TestPollInboxOnceWaitsForTheMutexAndContainsPanics(t *testing.T) {
	e := &env{stderr: io.Discard}
	var mu sync.Mutex
	mu.Lock()
	var called atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- e.pollInboxOnce(context.Background(), func(context.Context) error { called.Store(true); return nil }, &mu)
	}()
	time.Sleep(20 * time.Millisecond)
	if called.Load() {
		t.Fatal("the poll ran while the daily run held the lock")
	}
	mu.Unlock()
	if err := <-done; err != nil || !called.Load() {
		t.Fatalf("err %v called %v", err, called.Load())
	}
	err := e.pollInboxOnce(context.Background(), func(context.Context) error { panic("boom") }, &mu)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("a panic was not contained: %v", err)
	}
	if !mu.TryLock() {
		t.Fatal("the lock was kept after a panic")
	}
}
