package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func wdBoard(token string) sources.Board {
	return sources.Board{ATS: posting.Workday, Token: token, Company: token}
}

func wdRead(b sources.Board) boardReport {
	n := 300
	return boardReport{ATS: string(b.ATS), Token: b.Token, Company: b.Company, Outcome: boardRead, Listed: &n}
}

// foldRun is one run's board health fold for b: its outcome e, and the fetch
// counts the adapter reported, at at.
func foldRun(h *boardHealth, b sources.Board, e boardReport, f boardFetch, at time.Time) {
	h.fetches = map[boardKey]boardFetch{fetchKey(b.ATS, b.Token): f}
	h.record([]sources.Board{b}, []boardReport{e}, true, at, func(string, ...any) {})
}

// A board's deferred backlog is dated from the first good read that deferred
// rows and kept while reads go on deferring, through failed attempts, and ended
// by a read that defers none. The same for a short listing.
func TestBacklogStreakFollowsGoodReads(t *testing.T) {
	h := bhHealth(t)
	b := wdBoard("acme/External")
	key := boardHealthKey(string(b.ATS), b.Token)
	day := func(n int) time.Time { return bhNow.Add(time.Duration(n) * 24 * time.Hour) }
	since := func() (time.Time, time.Time) { r := h.doc.Boards[key]; return r.DeferredSince, r.ShortSince }

	foldRun(h, b, wdRead(b), boardFetch{}, day(0))
	if d, s := since(); !d.IsZero() || !s.IsZero() {
		t.Fatalf("a clean read opened a streak: %v %v", d, s)
	}
	foldRun(h, b, wdRead(b), boardFetch{Deferred: 120}, day(1))
	foldRun(h, b, wdRead(b), boardFetch{Deferred: 80, Short: 2}, day(2))
	if d, s := since(); !d.Equal(day(1)) || !s.Equal(day(2)) {
		t.Fatalf("streaks %v %v, want deferred from day 1 and short from day 2", d, s)
	}
	foldRun(h, b, bhFail(b, "boom"), boardFetch{}, day(3)) // a failed attempt says nothing
	if d, s := since(); !d.Equal(day(1)) || !s.Equal(day(2)) {
		t.Fatalf("a failed attempt changed the streaks: %v %v", d, s)
	}
	foldRun(h, b, wdRead(b), boardFetch{Deferred: 10}, day(4)) // short ends, deferred goes on
	if d, s := since(); !d.Equal(day(1)) || !s.IsZero() {
		t.Fatalf("after a read that deferred and was not short: %v %v", d, s)
	}
	foldRun(h, b, wdRead(b), boardFetch{}, day(5))
	if d, _ := since(); !d.IsZero() {
		t.Fatalf("an emptied backlog left its streak open: %v", d)
	}
	foldRun(h, b, wdRead(b), boardFetch{Deferred: 1}, day(6))
	if d, _ := since(); !d.Equal(day(6)) {
		t.Fatalf("a new backlog is dated from its own start: %v", d)
	}
}

// The streaks survive the file, and a start stamped ahead of the clock (written
// while it ran ahead) restarts at now rather than reading as a long backlog.
func TestBacklogStreakSurvivesFileAndClockAhead(t *testing.T) {
	dir := t.TempDir()
	b := wdBoard("acme/External")
	key := boardHealthKey(string(b.ATS), b.Token)
	h := openBoardHealth(dir, func(string, ...any) {})
	foldRun(h, b, wdRead(b), boardFetch{Deferred: 4, Short: 1}, bhNow.Add(-72*time.Hour))
	h2 := openBoardHealth(dir, func(string, ...any) {})
	if r := h2.doc.Boards[key]; !r.DeferredSince.Equal(bhNow.Add(-72*time.Hour)) || !r.ShortSince.Equal(bhNow.Add(-72*time.Hour)) {
		t.Fatalf("streaks not reloaded: %+v", r)
	}
	r := boardHealthRec{DeferredSince: bhNow.Add(30 * 24 * time.Hour), ShortSince: bhNow.Add(2 * time.Hour)}
	if got := r.sane(bhNow); !got.DeferredSince.Equal(bhNow) || !got.ShortSince.Equal(bhNow) {
		t.Fatalf("a start ahead of the clock was not restarted: %+v", got)
	}
}

// The age of a backlog reaches the run record's board item and `runs show`: an
// upper bound on the oldest deferred row, named per board and bounded.
func TestRunsShowNamesDeferredBacklogAge(t *testing.T) {
	h := bhHealth(t)
	b := wdBoard("acme/External")
	foldRun(h, b, wdRead(b), boardFetch{Deferred: 200}, bhNow.Add(-5*24*time.Hour))
	foldRun(h, b, wdRead(b), boardFetch{Deferred: 150}, bhNow)

	rep := &boardsReport{Configured: 1, Read: 1, Deferred: 150, Items: []boardReport{wdRead(b)}}
	rep.Items[0].Deferred = 150
	applyBacklogLocked(rep, h)
	if rep.Items[0].DeferredSince == nil || !rep.Items[0].DeferredSince.Equal(bhNow.Add(-5*24*time.Hour)) {
		t.Fatalf("item carries no backlog start: %+v", rep.Items[0])
	}

	rec := histRec(1, outcomeOK)
	rec.FinishedAt = bhNow
	rec.Boards = rep
	text := (&env{stateDir: t.TempDir(), stderr: &bytes.Buffer{}}).runReportOf(runRef{ID: runID(rec), Rec: rec}, histT0, true)
	for _, want := range []string{"Deferred: wd:acme/External (acme/External): 150 new row(s) past the detail cap", "backlog open since " + bhNow.Add(-5*24*time.Hour).Format("2006-01-02"), "at most "} {
		if !strings.Contains(text.Text, want) {
			t.Errorf("report missing %q\n%s", want, text.Text)
		}
	}
	if text.HasFailures {
		t.Fatal("a deferred backlog made the run a failure")
	}

	// First run that deferred: no streak start is known yet.
	rep.Items[0].DeferredSince = nil
	if got := deferredBacklogLines(rep, bhNow); len(got) != 1 || !strings.Contains(got[0], "backlog opened this run") {
		t.Fatalf("no start: %v", got)
	}
	// Nothing deferred, nothing said; a nil report is nothing.
	if got := deferredBacklogLines(&boardsReport{}, bhNow); got != nil {
		t.Fatalf("lines for no backlog: %v", got)
	}
	if got := deferredBacklogLines(nil, bhNow); got != nil {
		t.Fatalf("lines for a nil report: %v", got)
	}
}

func TestDeferredBacklogLinesAreBounded(t *testing.T) {
	rep := &boardsReport{}
	for i := range maxBacklogLines + 3 {
		it := wdRead(wdBoard(fmt.Sprintf("t%d/Site", i)))
		it.Deferred = 2
		rep.Items = append(rep.Items, it)
		rep.Deferred += 2
	}
	got := deferredBacklogLines(rep, bhNow)
	if len(got) != maxBacklogLines+1 || !strings.Contains(got[len(got)-1], "3 more board(s)") {
		t.Fatalf("%d lines, last %q", len(got), got[len(got)-1])
	}
}

// A stale board, a skipped board and a listing that has stayed short raise one
// attention line in the report, the failures copy too, and none of them makes
// the run a failure.
func TestBoardAttentionLineNamesOngoingProblems(t *testing.T) {
	since := bhNow.Add(-4 * 24 * time.Hour)
	recent := bhNow.Add(-6 * time.Hour)
	short := wdRead(wdBoard("slow/Site"))
	short.Short, short.ShortSince = 9, &since
	fresh := wdRead(wdBoard("fresh/Site"))
	fresh.Short, fresh.ShortSince = 3, &recent // short, but not for long
	rec := histRec(1, outcomeOK)
	rec.FinishedAt = bhNow
	rec.Boards = &boardsReport{Configured: 4, Read: 2, Skipped: 1, Short: 12,
		Stale: []staleBoard{{ATS: "gh", Token: "gone", Since: bhNow.Add(-3 * 24 * time.Hour)}}, StaleCount: 1,
		Items: []boardReport{short, fresh, {ATS: "page", Token: "careers.example.com", Outcome: boardSkipped, Error: "careers-page source is disabled"}}}
	if runHasFailures(rec, histT0) {
		t.Fatal("an ongoing board problem made the run a failure")
	}
	for _, detail := range []bool{true, false} {
		text := (&env{stateDir: t.TempDir(), stderr: &bytes.Buffer{}}).runReportOf(runRef{ID: runID(rec), Rec: rec}, bhNow, detail)
		for _, want := range []string{
			"Boards not being read (attention, not a failure): ",
			"1 stale (no good read for over 36h0m0s): gh:gone 3d",
			"1 skipped this run: page:careers.example.com",
			"1 listing short for over 36h0m0s, so nothing on them ages: wd:slow/Site (slow/Site) 4d",
		} {
			if !strings.Contains(text.Text, want) {
				t.Errorf("detail=%v: report missing %q\n%s", detail, want, text.Text)
			}
		}
		if strings.Contains(text.Text, "fresh/Site") && strings.Contains(attentionOf(text.Text), "fresh/Site") {
			t.Errorf("a board short for hours was named on the attention line\n%s", text.Text)
		}
		if text.HasFailures {
			t.Fatalf("detail=%v: report marked failures", detail)
		}
	}
}

func attentionOf(report string) string {
	for _, l := range strings.Split(report, "\n") {
		if strings.HasPrefix(l, "Boards not being read") {
			return l
		}
	}
	return ""
}

// No ongoing problem, no line; the digest form leaves the stale boards to the
// digest's own section; names are bounded while the counts stay exact.
func TestBoardAttentionLineEdges(t *testing.T) {
	if got := boardAttentionLine(nil, bhNow, true); got != "" {
		t.Fatalf("nil report: %q", got)
	}
	if got := boardAttentionLine(&boardsReport{Configured: 2, Read: 2, Items: []boardReport{wdRead(wdBoard("a/B"))}}, bhNow, true); got != "" {
		t.Fatalf("healthy boards: %q", got)
	}
	rep := &boardsReport{StaleCount: 2, Stale: []staleBoard{{ATS: "gh", Token: "x", Since: bhNow}}}
	if got := boardAttentionLine(rep, bhNow, false); got != "" {
		t.Fatalf("the digest form named stale boards: %q", got)
	}

	// Past the bound: five names and "and N more"; a skipped board the record
	// does not keep still counts.
	rep = &boardsReport{Skipped: 9}
	for i := range 7 {
		rep.Items = append(rep.Items, boardReport{ATS: "page", Token: fmt.Sprintf("h%d.example.com", i), Outcome: boardSkipped})
	}
	got := boardAttentionLine(rep, bhNow, true)
	if !strings.Contains(got, "9 skipped this run: ") || !strings.Contains(got, "page:h4.example.com and 4 more") || strings.Contains(got, "h5.example.com") {
		t.Fatalf("unbounded or inexact: %q", got)
	}
}

// A good read with no fetch counts for the board (health seeded or a board the
// adapter never reported on) leaves no streak, and nothing panics on a nil
// health (no state dir).
func TestBacklogWithoutCounts(t *testing.T) {
	var h *boardHealth
	rep := &boardsReport{Items: []boardReport{wdRead(wdBoard("a/B"))}}
	rep.Items[0].Deferred = 3
	applyBacklogLocked(rep, h) // nil health
	applyBacklogLocked(nil, bhHealth(t))
	if rep.Items[0].DeferredSince != nil {
		t.Fatalf("a streak out of nowhere: %v", rep.Items[0].DeferredSince)
	}
	r := trackBacklog(boardHealthRec{DeferredSince: bhNow}, wdRead(wdBoard("a/B")), boardFetch{}, bhNow)
	if !r.DeferredSince.IsZero() {
		t.Fatalf("a read with no deferral kept the streak: %+v", r)
	}
}
