package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/ibm"
	"github.com/bharathvbcr/Vani/internal/sources/oraclehcm"
	"github.com/bharathvbcr/Vani/internal/sources/smartrecruiters"
	"github.com/bharathvbcr/Vani/internal/sources/workable"
	"github.com/bharathvbcr/Vani/internal/sources/workday"
)

// A board that listed fewer jobs than it counts (workday.Stats.Short) carries
// the gap into the run record: per board, and in the total even when the board
// is not among the kept items. Two reports for one board add.
func TestBoardFetchCarriesShortListing(t *testing.T) {
	w := &wired{warn: &bytes.Buffer{}}
	acme := sources.Board{ATS: posting.Workday, Token: "acme/External"}
	w.noteBoardFetch(acme, boardFetch{Short: 7})
	w.noteBoardFetch(acme, boardFetch{Short: 3, Deferred: 1})
	w.noteBoardFetch(sources.Board{ATS: posting.Workday, Token: "cut/Site"}, boardFetch{Short: 5})

	rep := &boardsReport{Items: []boardReport{{ATS: string(posting.Workday), Token: "acme/External", Outcome: boardRead}}}
	w.mu.Lock()
	w.applyFetchesLocked(rep)
	w.mu.Unlock()
	if rep.Items[0].Short != 10 || rep.Items[0].Deferred != 1 {
		t.Fatalf("kept item %+v", rep.Items[0])
	}
	if rep.Short != 15 {
		t.Fatalf("total short %d, want 15 (the cut board's 5 included)", rep.Short)
	}
}

// A board that failed after reporting its counts (an adapter reports before a
// detail fails the board) is not reconciled, so nothing ages: its short rows
// are neither named nor totalled. A board the record does not keep is counted,
// as the cut keeps failed boards before read ones.
func TestShortListingOnAFailedBoardIsNotCounted(t *testing.T) {
	w := &wired{warn: &bytes.Buffer{}}
	w.noteBoardFetch(sources.Board{ATS: posting.SmartRecruiters, Token: "broke"}, boardFetch{Short: 9, Gone: 1})
	w.noteBoardFetch(sources.Board{ATS: posting.SmartRecruiters, Token: "fine"}, boardFetch{Short: 4})
	w.noteBoardFetch(sources.Board{ATS: posting.SmartRecruiters, Token: "cut"}, boardFetch{Short: 2})
	rep := &boardsReport{Items: []boardReport{
		{ATS: string(posting.SmartRecruiters), Token: "broke", Outcome: boardFailed, Error: "detail 500"},
		{ATS: string(posting.SmartRecruiters), Token: "fine", Outcome: boardRead},
	}}
	w.mu.Lock()
	w.applyFetchesLocked(rep)
	w.mu.Unlock()
	if rep.Items[0].Short != 0 || rep.Items[0].Gone != 1 || rep.Items[1].Short != 4 || rep.Short != 6 {
		t.Fatalf("items %+v, total short %d; want the failed board's 9 left out", rep.Items, rep.Short)
	}
}

// Each adapter's counts reach the board fetch the run record keeps: deferred,
// gone and short, whichever the adapter reports.
func TestAdapterStatsReachTheBoardFetch(t *testing.T) {
	for name, tc := range map[string]struct {
		got, want boardFetch
	}{
		"workday":         {workdayFetch(workday.Stats{Deferred: 2, Gone: 3, Short: 5}), boardFetch{Deferred: 2, Gone: 3, Short: 5}},
		"smartrecruiters": {smartRecruitersFetch(smartrecruiters.Stats{Deferred: 2, Gone: 3, Short: 5}), boardFetch{Deferred: 2, Gone: 3, Short: 5}},
		"oraclehcm":       {oracleFetch(oraclehcm.Stats{Deferred: 2, Gone: 3, Short: 5}), boardFetch{Deferred: 2, Gone: 3, Short: 5}},
		"ibm":             {ibmFetch(ibm.Stats{Listed: 9, Short: 5}), boardFetch{Short: 5}},
		"workable":        {workableFetch(workable.Stats{Deferred: 2, Gone: 3, Prefiltered: 4, Unanswered: 1}), boardFetch{Deferred: 2, Gone: 3, Prefiltered: 4, Unanswered: 1}},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: %+v, want %+v", name, tc.got, tc.want)
		}
	}
}

// What a Workday listing reports reaches the run record: deferred, gone and
// short together, and a short listing is said in the run log.
func TestWorkdayStatsReachTheRunRecord(t *testing.T) {
	warn := &bytes.Buffer{}
	w := &wired{warn: warn}
	b := sources.Board{ATS: posting.Workday, Token: "acme/External"}
	w.noteBoardFetch(b, workdayFetch(workday.Stats{Listed: 40, Deferred: 2, Gone: 1, Short: 60}))
	w.noteBoardFetch(sources.Board{ATS: posting.Workday, Token: "full/Site"}, workdayFetch(workday.Stats{Listed: 5}))
	rep := &boardsReport{Items: []boardReport{{ATS: string(posting.Workday), Token: "acme/External", Outcome: boardRead}}}
	w.mu.Lock()
	w.applyFetchesLocked(rep)
	w.mu.Unlock()
	if it := rep.Items[0]; it.Short != 60 || it.Deferred != 2 || it.Gone != 1 || rep.Short != 60 {
		t.Fatalf("item %+v, total short %d", it, rep.Short)
	}
	log := warn.String()
	if !strings.Contains(log, "board wd/acme/External: listed 60 job(s) fewer than it counts") || strings.Contains(log, "full/Site") {
		t.Fatalf("run log %q", log)
	}
}

// runs show names each board that listed short and how many jobs it left
// unlisted, and says so in the Boards line. It is not a failure: the listing is
// kept by design, and the run stays clean.
func TestRunsShowNamesShortListing(t *testing.T) {
	rec := histRec(1, outcomeOK)
	fifty, seven := 250, 40
	rec.Boards = &boardsReport{Configured: 3, Read: 3, Listed: 290, Short: 57, Items: []boardReport{
		{ATS: "workday", Token: "acme/External", Company: "Acme", Outcome: boardRead, Listed: &fifty, Short: 50},
		{ATS: "greenhouse", Token: "fine", Outcome: boardRead},
		{ATS: "workday", Token: "other/Site", Outcome: boardRead, Listed: &seven, Short: 7},
	}}
	if runHasFailures(rec, histT0) {
		t.Fatal("a short listing made the run a failure")
	}
	text := (&env{stateDir: t.TempDir(), stderr: &bytes.Buffer{}}).runReportOf(runRef{ID: runID(rec), Rec: rec}, histT0, true)
	for _, want := range []string{
		"; 57 counted but not listed",
		"Listed short: workday:acme/External (Acme): 50 counted job(s) not listed",
		"Listed short: workday:other/Site: 7 counted job(s) not listed",
		"No failures in this run.",
	} {
		if !strings.Contains(text.Text, want) {
			t.Errorf("report missing %q\n%s", want, text.Text)
		}
	}
	if strings.Contains(text.Text, "greenhouse:fine") {
		t.Errorf("a full listing was named short\n%s", text.Text)
	}
	if text.HasFailures {
		t.Fatal("report marked failures")
	}

	// The copy text (runs failures) carries failures only.
	copyText := (&env{stateDir: t.TempDir(), stderr: &bytes.Buffer{}}).runReportOf(runRef{ID: runID(rec), Rec: rec}, histT0, false)
	if strings.Contains(copyText.Text, "Listed short") {
		t.Errorf("failures copy text carries a short listing\n%s", copyText.Text)
	}
}

// The largest gaps are named first, so routine one-row gaps on many boards
// cannot push a large one past the bound.
func TestRunsShowNamesTheLargestShortListingsFirst(t *testing.T) {
	rec := histRec(1, outcomeOK)
	rep := &boardsReport{Configured: maxShortLines + 2, Read: maxShortLines + 2}
	for i := range maxShortLines + 1 {
		rep.Items = append(rep.Items, boardReport{ATS: "workday", Token: fmt.Sprintf("small%d/Site", i), Outcome: boardRead, Short: 1})
		rep.Short++
	}
	rep.Items = append(rep.Items, boardReport{ATS: "workday", Token: "big/Site", Outcome: boardRead, Short: 60})
	rep.Short += 60
	rec.Boards = rep
	text := (&env{stateDir: t.TempDir(), stderr: &bytes.Buffer{}}).runReportOf(runRef{ID: runID(rec), Rec: rec}, histT0, true).Text
	big, small := strings.Index(text, "Listed short: workday:big/Site: 60"), strings.Index(text, "Listed short: workday:small0/Site")
	if big < 0 || small < 0 || big > small {
		t.Fatalf("the 60-row gap is not named first\n%s", text)
	}
	if !strings.Contains(text, "Listed short: 2 more board(s), 2 counted job(s) not listed") {
		t.Fatalf("the one-row gaps past the bound are not counted\n%s", text)
	}
}

// The per-board lines are bounded; boards past the bound, and short rows on
// boards the record could not keep, are counted rather than dropped.
func TestRunsShowShortListingIsBounded(t *testing.T) {
	rec := histRec(1, outcomeOK)
	n := maxShortLines + 4
	rep := &boardsReport{Configured: n + 1, Read: n + 1, Unrecorded: 1}
	for i := range n {
		rep.Items = append(rep.Items, boardReport{ATS: "workday", Token: fmt.Sprintf("t%d/Site", i), Outcome: boardRead, Short: 2})
		rep.Short += 2
	}
	rep.Short += 9 // the unrecorded board's
	rec.Boards = rep
	text := (&env{stateDir: t.TempDir(), stderr: &bytes.Buffer{}}).runReportOf(runRef{ID: runID(rec), Rec: rec}, histT0, true)
	if got := strings.Count(text.Text, "Listed short: workday:"); got != maxShortLines {
		t.Fatalf("%d per-board lines, want %d\n%s", got, maxShortLines, text.Text)
	}
	for _, want := range []string{
		"Listed short: 4 more board(s), 8 counted job(s) not listed",
		"Listed short: 9 counted job(s) not listed on boards the record does not keep",
	} {
		if !strings.Contains(text.Text, want) {
			t.Errorf("report missing %q\n%s", want, text.Text)
		}
	}
}
