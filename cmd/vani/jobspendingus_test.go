package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// seedPendingUS seeds seven postings awaiting triage across two companies
// (three of them in the US, written the ways boards write them), a triaged
// posting in the US, and a dead one: neither of those waits.
func seedPendingUS(t *testing.T, state string) {
	t.Helper()
	seedJobs(t, state,
		seedJob{key: "gh:acme:1", title: "Engineer", status: posting.Live, company: "Acme", location: "San Francisco"},
		seedJob{key: "gh:acme:2", title: "Engineer", status: posting.Live, company: "Acme", location: "US, CA, Santa Clara"},
		seedJob{key: "gh:acme:3", title: "Engineer", status: posting.Live, company: "Acme", location: "Toronto, ON"},
		seedJob{key: "gh:acme:4", title: "Engineer", status: posting.Live, company: "Acme", location: ""},
		seedJob{key: "gh:beta:1", title: "Engineer", status: posting.Live, company: "Beta", location: "Remote - US"},
		seedJob{key: "gh:beta:2", title: "Engineer", status: posting.Live, company: "Beta", location: "Remote"},
		seedJob{key: "gh:beta:3", title: "Engineer", status: posting.Live, company: "Beta", location: "Berlin, Germany"},
		seedJob{key: "gh:beta:4", title: "Engineer", fit: "strong_fit", status: posting.Digested, company: "Beta", location: "Austin, TX"},
		seedJob{key: "gh:beta:5", title: "Engineer", status: posting.Dead, company: "Beta", location: "Austin, TX"},
	)
}

// `jobs list --json` sizes the US part of the backlog, in total and per
// company, from locations as boards write them; the query narrows `matching`
// and leaves the US counts alone. The text line says the same.
func TestJobsPendingReportsTheUSBacklog(t *testing.T) {
	_, state := setup(t)
	seedPendingUS(t, state)

	l := listJSON(t, "--q", " BETA ")
	want := jobview.Pending{Total: 7, Matching: 3, US: 3, Companies: []jobview.PendingCompany{
		{Company: "Acme", N: 4, US: 2}, {Company: "Beta", N: 3, US: 1}}}
	if l.Pending.Total != want.Total || l.Pending.Matching != want.Matching || l.Pending.US != want.US ||
		len(l.Pending.Companies) != 2 || l.Pending.Companies[0] != want.Companies[0] || l.Pending.Companies[1] != want.Companies[1] {
		t.Fatalf("pending %+v, want %+v", l.Pending, want)
	}

	o, _ := mustCode(t, 0, "jobs", "list", "--json")
	if !strings.Contains(o, `"pending":{"total":7,"matching":7,"companies":[{"company":"Acme","n":4,"us":2},{"company":"Beta","n":3,"us":1}],"us":3}`) {
		t.Fatalf("pending wire shape:\n%s", o)
	}

	o, _ = mustCode(t, 0, "jobs", "stats", "--json")
	var s jobview.Stats
	if err := json.Unmarshal([]byte(o), &s); err != nil {
		t.Fatal(err)
	}
	if s.Pending.US != 3 || s.Pending.Total != 7 {
		t.Fatalf("stats pending %+v", s.Pending)
	}

	text, _ := mustCode(t, 0, "jobs", "list")
	if !strings.Contains(text, "awaiting triage (not listed until a run triages them): 7 (3 in the US; Acme 4, Beta 3); list them") {
		t.Fatalf("text line:\n%s", text)
	}
	text, _ = mustCode(t, 0, "jobs", "list", "--q", "beta")
	if !strings.Contains(text, `3 matching "beta", 7 in all (3 in the US); list them`) {
		t.Fatalf("text line with a query:\n%s", text)
	}
}

// Nothing waits in the US: the count is 0 on the wire and in the text, never
// absent. (Nothing waiting at all is TestJobsPendingEmpty.)
func TestJobsPendingUSZero(t *testing.T) {
	_, state := setup(t)
	seedJobs(t, state,
		seedJob{key: "gh:a:1", title: "Engineer", status: posting.Live, location: "Berlin, Germany"},
		seedJob{key: "gh:a:2", title: "Engineer", status: posting.Live, location: "Toronto, ON"},
	)
	o, _ := mustCode(t, 0, "jobs", "list", "--json")
	if !strings.Contains(o, `"pending":{"total":2,"matching":2,"companies":[{"company":"Acme","n":2,"us":0}],"us":0}`) {
		t.Fatalf("reply %s", o)
	}
	if text, _ := mustCode(t, 0, "jobs", "list"); !strings.Contains(text, ": 2 (0 in the US; Acme 2)") {
		t.Fatalf("text line:\n%s", text)
	}
}

// status --json: awaiting_triage_us sits beside awaiting_triage in the unkept
// schedule, counted from the same ledger snapshot; 0 is written as 0, and with
// no ledger yet both are absent.
func TestStatusAwaitingTriageUS(t *testing.T) {
	f := keptSetup(t, 750, -1)
	if o, _ := mustCode(t, 0, "status", "--json"); strings.Contains(o, "awaiting_triage") {
		t.Fatalf("no ledger yet is not a backlog:\n%s", o)
	}
	m := &memLedger{recs: map[posting.Key]*ledger.Record{}}
	for k, c := range map[posting.Key]struct {
		st  posting.Status
		loc string
	}{
		"gh:a:1": {posting.Live, "San Francisco"}, "gh:a:2": {posting.Live, "Remote - US"}, "gh:a:3": {posting.Live, "Toronto, ON"},
		"gh:a:4": {posting.Live, ""}, "gh:a:5": {posting.Explained, "Austin, TX"}, "gh:a:6": {posting.Dead, "Austin, TX"},
	} {
		m.recs[k] = &ledger.Record{Key: k, Status: c.st, LastEvent: keptNow.Add(-time.Hour),
			Posting: posting.Posting{Company: "Acme", Title: "Engineer " + string(k), URL: "https://x.example/" + string(k), Location: c.loc}}
	}
	seedLedger(t, f.stateDir, m)
	o, _ := mustCode(t, 0, "status", "--json")
	if !strings.Contains(o, `"awaiting_triage":4,`) || !strings.Contains(o, `"awaiting_triage_us":2,`) {
		t.Fatalf("status lacks the US part of the backlog:\n%s", o)
	}
	u := statusOf(t).Schedule.Unkept
	if u == nil || u.AwaitingTriageUS == nil || *u.AwaitingTriageUS != 2 || *u.AwaitingTriageUS > *u.AwaitingTriage {
		t.Fatalf("unkept %+v", u)
	}
	if strings.Contains(o, `"liveUS"`) || strings.Contains(o, `"live_us"`) {
		t.Fatalf("the ledger facet leaked its counter:\n%s", o)
	}
}

func TestStatusAwaitingTriageUSZeroIsWrittenAsZero(t *testing.T) {
	f := keptSetup(t, 750, -1)
	m := &memLedger{recs: map[posting.Key]*ledger.Record{
		"gh:a:1": {Key: "gh:a:1", Status: posting.Live, LastEvent: keptNow.Add(-time.Hour),
			Posting: posting.Posting{Company: "Acme", Title: "Engineer", URL: "https://x.example/1", Location: "Berlin, Germany"}},
	}}
	seedLedger(t, f.stateDir, m)
	if o, _ := mustCode(t, 0, "status", "--json"); !strings.Contains(o, `"awaiting_triage":1,`) || !strings.Contains(o, `"awaiting_triage_us":0,`) {
		t.Fatalf("zero US:\n%s", o)
	}
}

// A kept schedule reports no backlog, so no US part either.
func TestStatusKeptScheduleHasNoAwaitingTriageUS(t *testing.T) {
	s := withAwaitingUS(scheduleFacet{Kept: new(true)}, new(2))
	if s.Unkept != nil {
		t.Fatalf("kept schedule grew an unkept block: %+v", s.Unkept)
	}
}

func TestWithAwaitingUSOnlyFillsAKnownBacklog(t *testing.T) {
	cfg := config.Config{AdvancePerRun: 750}
	unkept := func(live *int) scheduleFacet {
		return scheduleKeeping(cfg, scheduleFacet{facet: facet{OK: true}}, daemonFacet{facet: facet{OK: true}}, lastRunFacet{State: "none"}, live, keptNow)
	}
	if u := withAwaitingUS(unkept(new(5)), new(2)).Unkept; u == nil || u.AwaitingTriageUS == nil || *u.AwaitingTriageUS != 2 {
		t.Errorf("a counted backlog: %+v", u)
	}
	if u := withAwaitingUS(unkept(nil), new(2)).Unkept; u == nil || u.AwaitingTriageUS != nil {
		t.Errorf("an uncounted backlog got a US part: %+v", u)
	}
	if u := withAwaitingUS(unkept(new(5)), nil).Unkept; u == nil || u.AwaitingTriageUS != nil {
		t.Errorf("a nil US count was written: %+v", u)
	}
}

// liveInUS counts only live postings, and reads the Posting's location text as
// boards write it, whatever its size.
func TestLiveInUS(t *testing.T) {
	mk := func(st posting.Status, loc string) ledger.Record {
		return ledger.Record{Status: st, Posting: posting.Posting{Location: loc}}
	}
	recs := []ledger.Record{
		mk(posting.Live, "San Francisco"), mk(posting.Live, "US, CA, Santa Clara"), mk(posting.Live, "Toronto, ON"),
		mk(posting.Live, "Remote - US"), mk(posting.Live, ""), mk(posting.Live, "Remote"),
		mk(posting.Digested, "Austin, TX"), mk(posting.Dead, "Austin, TX"), mk(posting.Dropped, "Austin, TX"),
		mk(posting.Live, strings.Repeat("San Francisco, ", 20000)), mk(posting.Live, "\x00"),
	}
	// San Francisco, US/CA/Santa Clara, Remote - US, and the oversized one that
	// still names San Francisco.
	if got := liveInUS(recs); got != 4 {
		t.Errorf("liveInUS = %d, want 4", got)
	}
	if liveInUS(nil) != 0 || liveInUS([]ledger.Record{}) != 0 {
		t.Error("an empty ledger has no US backlog")
	}
}

// The text status line names the US part when it is known, and reads as it did
// before when it is not.
func TestUnkeptLinesNameTheUSBacklog(t *testing.T) {
	u := &unkeptSchedule{Hint: "h", AwaitingTriage: new(1600), AdvancePerRun: new(750), RunsNeeded: new(3), AwaitingTriageUS: new(700)}
	s := scheduleFacet{Timezone: "UTC", Unkept: u}
	if o := strings.Join(unkeptLines(s, keptNow), "\n"); !strings.Contains(o, "awaiting triage: 1600 postings (700 in the US), 3 runs at advance_per_run 750") {
		t.Errorf("with the US part:\n%s", o)
	}
	u.AwaitingTriageUS = new(0)
	if o := strings.Join(unkeptLines(s, keptNow), "\n"); !strings.Contains(o, "awaiting triage: 1600 postings (0 in the US), 3 runs") {
		t.Errorf("with a zero US part:\n%s", o)
	}
	u.AwaitingTriageUS = nil
	if o := strings.Join(unkeptLines(s, keptNow), "\n"); !strings.Contains(o, "awaiting triage: 1600 postings, 3 runs at advance_per_run 750") || strings.Contains(o, "in the US") {
		t.Errorf("without it:\n%s", o)
	}
}
