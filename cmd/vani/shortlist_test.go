package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// A board that listed fewer jobs than it counts (workday.Stats.Short) carries
// the gap into the run record: per board, and in the total even when the board
// is not among the kept items. Two reports for one board add.
func TestBoardFetchCarriesShortListing(t *testing.T) {
	w := &wired{}
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
