package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The schedule is a promise only a running daemon keeps. These tests pin what
// `vani status` (text and --json) says when none is: that it is not kept, what
// that has cost (last run, overdue, the backlog in runs), and what the fix
// needs. The clock is 2026-10-22 11:00 America/Los_Angeles, after the 08:30
// slot.

var keptNow = time.Date(2026, 10, 22, 18, 0, 0, 0, time.UTC) // 11:00 PDT

type keptFix struct {
	stateDir string
}

// keptSetup initialises a config with advance_per_run advance, pins the clock
// and seeds live postings. live < 0 leaves the ledger uncreated.
func keptSetup(t *testing.T, advance, live int) keptFix {
	t.Helper()
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AdvancePerRun = advance
	writeConfig(t, cfgPath, cfg)

	oldNow, oldAlive := now, pidAlive
	t.Cleanup(func() { now, pidAlive = oldNow, oldAlive })
	now = func() time.Time { return keptNow }
	pidAlive = func(int) bool { return false }

	if live >= 0 {
		m := &memLedger{recs: map[posting.Key]*ledger.Record{}}
		for i := range live {
			k := posting.Key(fmt.Sprintf("gh:acme:%05d", i))
			m.recs[k] = &ledger.Record{Key: k, Status: posting.Live, LastEvent: keptNow.Add(-time.Hour),
				Posting: posting.Posting{Company: "Acme", Title: "Engineer " + string(k), URL: "https://x.example/" + string(k)}}
		}
		// A posting that already moved on is not awaiting triage.
		m.recs["gh:acme:done"] = &ledger.Record{Key: "gh:acme:done", Status: posting.Explained, LastEvent: keptNow.Add(-time.Hour),
			Posting: posting.Posting{Company: "Acme", Title: "Done", URL: "https://x.example/done"}}
		seedLedger(t, stateDir, m)
	}
	return keptFix{stateDir: stateDir}
}

func (f keptFix) lastRun(t *testing.T, started time.Time, outcome string) {
	t.Helper()
	rec := runRecord{PID: 1, StartedAt: started, Outcome: outcome, Trigger: triggerManual}
	if outcome != outcomeRunning {
		rec.FinishedAt = started.Add(5 * time.Minute)
	}
	if err := writeRunRecord(f.stateDir, rec); err != nil {
		t.Fatal(err)
	}
}

func TestStatusNoDaemonSaysTheScheduleIsNotKept(t *testing.T) {
	keptSetup(t, 750, 1600)
	s := statusOf(t)

	if !s.Schedule.OK || s.Schedule.NextRun.IsZero() {
		t.Fatalf("the schedule itself reads fine and keeps its next run: %+v", s.Schedule)
	}
	if s.Schedule.Kept == nil || *s.Schedule.Kept {
		t.Fatalf("kept = %v with no daemon.json, want false", s.Schedule.Kept)
	}
	u := s.Schedule.Unkept
	if u == nil {
		t.Fatal("no unkept details")
	}
	if !strings.Contains(u.Why, "no daemon has announced itself") {
		t.Errorf("why = %q", u.Why)
	}
	if u.AwaitingTriage == nil || *u.AwaitingTriage != 1600 || *u.AdvancePerRun != 750 || *u.RunsNeeded != 3 {
		t.Errorf("backlog: %v of %v, %v runs (want 1600 at 750 = 3 runs; the explained posting does not count)", u.AwaitingTriage, u.AdvancePerRun, u.RunsNeeded)
	}
	// No run is recorded: the last run is absent (not "now" and not zero-time
	// text) and a run is overdue.
	if !u.LastRunAt.IsZero() || u.Overdue == nil || !*u.Overdue {
		t.Errorf("no run recorded: last=%v overdue=%v", u.LastRunAt, u.Overdue)
	}
	wantDue := time.Date(2026, 10, 22, 15, 30, 0, 0, time.UTC)
	if !u.DueAt.Equal(wantDue) {
		t.Errorf("due at %v, want %v", u.DueAt, wantDue)
	}
	for _, want := range []string{"Run on schedule", "vani launchd plist", "installs nothing", "OPENROUTER_API_KEY", "Keychain", "--advance"} {
		if !strings.Contains(u.Hint, want) {
			t.Errorf("hint lacks %q: %s", want, u.Hint)
		}
	}
	if strings.Contains(u.Hint, "EnvironmentVariables") {
		t.Errorf("hint tells the owner to write the key into a plist: %s", u.Hint)
	}
}

func TestStatusTextNoDaemonSaysTheScheduleIsNotKept(t *testing.T) {
	f := keptSetup(t, 750, 1600)
	f.lastRun(t, keptNow.Add(-30*time.Hour), outcomeOK)
	o, _ := mustCode(t, 0, "status")
	for _, want := range []string{
		"schedule: 08:30 America/Los_Angeles\n",
		"schedule not kept: nothing fires it (no daemon has announced itself)",
		"last run: 2026-10-21 05:05 PDT (30 h ago); overdue: the 08:30 run was due 3 h ago",
		"awaiting triage: 1600 postings, 3 runs at advance_per_run 750",
		"fix: turn on Run on schedule in Vani Apply's Pipeline tab",
		"postings: 1601",
	} {
		if !strings.Contains(o, want) {
			t.Errorf("status lacks %q:\n%s", want, o)
		}
	}
}

func TestStatusStaleDaemonRecordIsNotAKeeper(t *testing.T) {
	f := keptSetup(t, 750, 10)
	if err := writeDaemonRecord(f.stateDir, 4242, keptNow.Add(-72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	s := statusOf(t)
	if s.Schedule.Kept == nil || *s.Schedule.Kept || s.Schedule.Unkept == nil || !strings.Contains(s.Schedule.Unkept.Why, "pid 4242") {
		t.Fatalf("a daemon.json whose pid is gone keeps nothing: %+v", s.Schedule)
	}
	o, _ := mustCode(t, 0, "status")
	if !strings.Contains(o, "schedule not kept: nothing fires it (the daemon recorded as pid 4242 is gone") {
		t.Errorf("text status:\n%s", o)
	}
}

func TestStatusRunningDaemonKeepsTheSchedule(t *testing.T) {
	f := keptSetup(t, 750, 1600)
	if err := writeDaemonRecord(f.stateDir, 4242, keptNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	pidAlive = func(pid int) bool { return pid == 4242 }
	s := statusOf(t)
	if s.Schedule.Kept == nil || !*s.Schedule.Kept || s.Schedule.Unkept != nil {
		t.Errorf("a live daemon keeps it, with nothing to report: %+v", s.Schedule)
	}
	o, _ := mustCode(t, 0, "status")
	if strings.Contains(o, "not kept") || strings.Contains(o, "awaiting triage") {
		t.Errorf("a kept schedule adds no noise:\n%s", o)
	}
}

func TestStatusUnreadableDaemonRecordIsUnknownNotKeptNorUnkept(t *testing.T) {
	f := keptSetup(t, 750, 10)
	writeStateFile(t, f.stateDir, "runs/daemon.json", "{not json")
	s := statusOf(t)
	if s.Daemon.OK {
		t.Fatalf("daemon facet should not be ok: %+v", s.Daemon)
	}
	if s.Schedule.Kept != nil || s.Schedule.Unkept != nil {
		t.Errorf("whether a daemon runs could not be told, so the schedule is neither kept nor unkept: kept=%v unkept=%+v", s.Schedule.Kept, s.Schedule.Unkept)
	}
	o, _ := mustCode(t, 0, "status")
	if strings.Contains(o, "not kept") {
		t.Errorf("text status claims what it could not check:\n%s", o)
	}
}

func TestStatusOverdueIsRelativeToTheMostRecentSlot(t *testing.T) {
	slot := time.Date(2026, 10, 22, 15, 30, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		started time.Time
		overdue bool
	}{
		"before the slot":          {slot.Add(-time.Minute), true},
		"yesterday":                {slot.Add(-26 * time.Hour), true},
		"a manual run since":       {slot.Add(time.Minute), false},
		"started exactly at it":    {slot, false},
		"running now, since slot":  {keptNow.Add(-time.Minute), false},
		"a month ago":              {slot.Add(-30 * 24 * time.Hour), true},
		"started just before slot": {slot.Add(-time.Second), true},
	} {
		t.Run(name, func(t *testing.T) {
			f := keptSetup(t, 750, 3)
			f.lastRun(t, tc.started, outcomeOK)
			u := statusOf(t).Schedule.Unkept
			if u == nil || u.Overdue == nil || *u.Overdue != tc.overdue || u.LastRunAt.IsZero() {
				t.Fatalf("started %v: %+v", tc.started, u)
			}
			// The last run is when it finished.
			if want := tc.started.Add(5 * time.Minute); !u.LastRunAt.Equal(want) {
				t.Errorf("last run at %v, want %v", u.LastRunAt, want)
			}
		})
	}
}

func TestStatusUnfinishedRunUsesItsStart(t *testing.T) {
	f := keptSetup(t, 750, 3)
	started := keptNow.Add(-2 * time.Hour)
	f.lastRun(t, started, outcomeRunning)
	u := statusOf(t).Schedule.Unkept
	if u == nil || !u.LastRunAt.Equal(started) {
		t.Fatalf("an unfinished run is dated by its start: %+v", u)
	}
}

func TestStatusUnreadableRunRecordLeavesOverdueUnknown(t *testing.T) {
	f := keptSetup(t, 750, 3)
	writeStateFile(t, f.stateDir, "runs/last.json", "{not json")
	s := statusOf(t)
	u := s.Schedule.Unkept
	if u == nil || u.Overdue != nil || !u.LastRunAt.IsZero() {
		t.Fatalf("an unreadable run record is not 'no run': %+v", u)
	}
	o, _ := mustCode(t, 0, "status")
	if !strings.Contains(o, "last run: unknown (the run record could not be read)") {
		t.Errorf("text status:\n%s", o)
	}
}

func TestStatusZeroBacklogStillSaysNotKeptButNoBacklogLine(t *testing.T) {
	f := keptSetup(t, 750, 0)
	f.lastRun(t, keptNow.Add(-time.Hour), outcomeOK)
	u := statusOf(t).Schedule.Unkept
	if u == nil || u.AwaitingTriage == nil || *u.AwaitingTriage != 0 || *u.RunsNeeded != 0 {
		t.Fatalf("nothing waits: %+v", u)
	}
	o, _ := mustCode(t, 0, "status")
	if !strings.Contains(o, "schedule not kept") || strings.Contains(o, "awaiting triage") || strings.Contains(o, "overdue") {
		t.Errorf("no backlog and a run since the slot:\n%s", o)
	}
}

func TestStatusNoLedgerYetLeavesTheBacklogUnknown(t *testing.T) {
	keptSetup(t, 750, -1)
	u := statusOf(t).Schedule.Unkept
	if u == nil || u.AwaitingTriage != nil || u.RunsNeeded != nil || u.AdvancePerRun != nil {
		t.Fatalf("no ledger yet is not a backlog of zero: %+v", u)
	}
}

func TestStatusAdvanceLimitDefaultsAndRoundsUp(t *testing.T) {
	for _, tc := range []struct{ advance, live, want int }{
		{750, 750, 1}, {750, 751, 2}, {750, 1, 1}, {750, 8525, 12},
		{0, 301, 2}, {0, 300, 1}, // 0 reads as the default cap
	} {
		cfg := config.Config{AdvancePerRun: tc.advance}
		s := scheduleKeeping(cfg, scheduleFacet{facet: facet{OK: true}}, daemonFacet{facet: facet{OK: true}}, lastRunFacet{State: "none"}, &tc.live, keptNow)
		u := s.Unkept
		if u == nil || u.RunsNeeded == nil || *u.RunsNeeded != tc.want || *u.AdvancePerRun != cfg.AdvanceLimit() {
			t.Errorf("advance %d, live %d: %+v, want %d runs", tc.advance, tc.live, u, tc.want)
		}
	}
}

// Without an OpenRouter decision model there is no key for a daemon to lack,
// so the hint does not claim one.
func TestKeepHintOnlyNamesAKeyThatIsConfigured(t *testing.T) {
	cfg := config.Config{}
	if h := keepHint(cfg); strings.Contains(h, "key") || !strings.Contains(h, "vani launchd plist") {
		t.Errorf("no decision model configured: %s", h)
	}
	cfg.Decide.OpenRouterModel, cfg.Decide.OpenRouterKeyEnv = "liquid/d1-ultra", "MY_KEY_VAR"
	if h := keepHint(cfg); !strings.Contains(h, "MY_KEY_VAR") {
		t.Errorf("the configured variable is named: %s", h)
	}
}

// A schedule the config could not parse has nothing to keep: status makes no
// claim either way.
func TestScheduleKeepingLeavesAnUnreadableScheduleAlone(t *testing.T) {
	s := scheduleKeeping(config.Config{}, scheduleFacet{}, daemonFacet{facet: facet{OK: true}}, lastRunFacet{State: "none"}, new(5), keptNow)
	if s.Kept != nil || s.Unkept != nil {
		t.Errorf("%+v", s)
	}
}
