package main

// Review F4 (2026-11-12): an ongoing mass outage read as "no failures" from
// its second day, and postings dying at the end of a 14-day mass hold were not
// flagged. Now postings in a mass hold are an attention line every run (not a
// failure), and mass-held postings that die are a failure.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func heldRec(b sources.Board, id int, firstMiss time.Time) ledger.Record {
	r := noneRec(b, id, posting.Live, map[string]string{
		pipeline.MissedSinceKey:     firstMiss.UTC().Format(time.RFC3339Nano),
		pipeline.MassMissedSinceKey: firstMiss.UTC().Format(time.RFC3339Nano),
	}, 2)
	r.FirstSeen = firstMiss.Add(-24 * time.Hour)
	return r
}

// massHeldOf names the boards from the ledger, with when their holds end, the
// earliest first; a board off the watchlist by its key; a cleared mark, a closed
// posting or one not missed is not held; the list is bounded and the counts
// are exact.
func TestMassHeldOfNamesBoardsAndHoldEnds(t *testing.T) {
	snap, acme, gone := bhBoard("snap"), bhBoard("acme"), bhBoard("gone")
	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	recs := []ledger.Record{
		heldRec(snap, 1, at.Add(-48*time.Hour)),
		heldRec(snap, 2, at.Add(-24*time.Hour)),
		heldRec(acme, 1, at.Add(-10*24*time.Hour)),
		heldRec(gone, 1, at.Add(-24*time.Hour)),
		noneRec(acme, 2, posting.Live, map[string]string{pipeline.MassMissedSinceKey: ""}, 1), // a cleared mark
		noneRec(acme, 3, posting.Dead, map[string]string{pipeline.MassMissedSinceKey: "2026-11-01T00:00:00Z"}, 2),
		noneRec(acme, 4, posting.Live, map[string]string{pipeline.MassMissedSinceKey: "2026-11-01T00:00:00Z"}, 0),
	}
	held, boards, postings := massHeldOf(recs, []sources.Board{snap, acme}, pipeline.DefaultLiveness(), at)
	if boards != 3 || postings != 4 || len(held) != 3 {
		t.Fatalf("%d boards %d postings: %+v", boards, postings, held)
	}
	hold := pipeline.DefaultMassHold
	if held[0].Token != "acme" || !held[0].Until.Equal(at.Add(-10*24*time.Hour+hold)) || !held[0].Last.IsZero() {
		t.Fatalf("earliest end first: %+v", held[0])
	}
	if held[1].Token != "snap" || held[1].Held != 2 || !held[1].Until.Equal(at.Add(-48*time.Hour+hold)) || !held[1].Last.Equal(at.Add(-24*time.Hour+hold)) {
		t.Fatalf("snap: %+v", held[1])
	}
	if held[2].ATS != "gh" || held[2].Token != "gone" || held[2].Company != "" {
		t.Fatalf("a board off the watchlist is named by its key: %+v", held[2])
	}
	line := massHeldLine(held, boards, postings)
	for _, want := range []string{"Mass hold (attention, not a failure): 4 posting(s) on 3 board(s)", "acme (gh:acme) 1 until 2026-11-16",
		"snap (gh:snap) 2 until 2026-11-24..2026-11-25", "gh:gone 1 until 2026-11-25"} {
		if !strings.Contains(line, want) {
			t.Errorf("line lacks %q: %s", want, line)
		}
	}
	// A configured hold reaches the dates.
	short, _, _ := massHeldOf(recs[:1], nil, pipeline.Liveness{MassHold: 72 * time.Hour}, at)
	if !short[0].Until.Equal(at.Add(24 * time.Hour)) {
		t.Fatalf("configured hold: %+v", short)
	}
	// Bounded, with the exact counts.
	var many []ledger.Record
	for i := range maxStaleListed + 5 {
		many = append(many, heldRec(bhBoard(fmt.Sprintf("b%02d", i)), 1, at.Add(-time.Duration(i)*time.Hour)))
	}
	held, boards, postings = massHeldOf(many, nil, pipeline.DefaultLiveness(), at)
	if len(held) != maxStaleListed || boards != maxStaleListed+5 || postings != maxStaleListed+5 {
		t.Fatalf("bounds: %d listed, %d boards, %d postings", len(held), boards, postings)
	}
	if line := massHeldLine(held, boards, postings); !strings.Contains(line, "5 more board(s)") {
		t.Fatalf("hidden boards not counted: %s", line)
	}
	if massHeldLine(nil, 0, 0) != "" {
		t.Fatal("a line with nothing held")
	}
}

// A run whose only news is postings carried through a mass hold: the report
// names them every run, and says there are no failures.
func TestMassHoldIsAnAttentionLineNotAFailure(t *testing.T) {
	listed := 3
	r := histRec(1, outcomeOK)
	until := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	r.Boards = &boardsReport{Configured: 1, Read: 1, Listed: 3, Held: 20,
		MassHeld: []massHeldBoard{{ATS: "gh", Token: "snap", Company: "Snap", Held: 20, Until: until}}, MassHeldCount: 1, MassHeldPostings: 20,
		Items: []boardReport{{ATS: "gh", Token: "snap", Outcome: boardRead, Listed: &listed, Reconcile: &reconcileCounts{Open: 23, Absent: 20, Held: 20}}}}
	if runHasFailures(r, histT0) {
		t.Fatal("postings carried through a mass hold were a failure")
	}
	text := (&env{stateDir: t.TempDir(), stderr: &bytes.Buffer{}}).runReportOf(runRef{ID: runID(r), Rec: r}, histT0, true)
	for _, want := range []string{"Mass hold (attention, not a failure): 20 posting(s) on 1 board(s)", "Snap (gh:snap) 20 until 2026-11-26",
		"No failures in this run.", "20 posting(s) on 1 board(s) in a mass hold"} {
		if !strings.Contains(text.Text, want) {
			t.Errorf("report lacks %q:\n%s", want, text.Text)
		}
	}
	if text.HasFailures {
		t.Fatal("the report has failures")
	}
	// The record carries it as JSON the next reader can decode.
	b, err := json.Marshal(r.Boards)
	if err != nil || !strings.Contains(string(b), `"mass_held":[{"ats":"gh","token":"snap","company":"Snap","held":20,"until":"2026-11-26T09:00:00Z"}]`) ||
		!strings.Contains(string(b), `"mass_held_count":1,"mass_held_postings":20`) {
		t.Fatalf("json: %s %v", b, err)
	}
}

// Postings that die at the end of a mass hold are a failure, named by board.
// A death that waits a read is shown and is not.
func TestMassDeathsAreAFailure(t *testing.T) {
	listed := 0
	r := histRec(1, outcomeOK)
	r.Boards = &boardsReport{Configured: 1, Read: 1, MassDead: 20, Items: []boardReport{
		{ATS: "gh", Token: "snap", Company: "Snap", Outcome: boardRead, Listed: &listed, Reconcile: &reconcileCounts{Open: 20, Absent: 20, Dead: 20, MassDead: 20}}}}
	if !runHasFailures(r, histT0) {
		t.Fatal("mass-held postings that died were not a failure")
	}
	if !recordFirst(r.Boards.Items[0]) {
		t.Fatal("a board whose mass-held postings died is not kept first when the record is cut")
	}
	text := (&env{stateDir: t.TempDir(), stderr: &bytes.Buffer{}}).runReportOf(runRef{ID: runID(r), Rec: r}, histT0, true)
	for _, want := range []string{"Died after a mass hold (20)", "gh:snap (Snap): listed 0; 20 posting(s) held after a mass absence died at the end of the hold",
		"20 posting(s) died after a mass hold"} {
		if !strings.Contains(text.Text, want) {
			t.Errorf("report lacks %q:\n%s", want, text.Text)
		}
	}
	if !text.HasFailures || strings.Contains(text.Text, "No failures") {
		t.Fatalf("report:\n%s", text.Text)
	}
	// Totals come from the boards, and a death that waited is not a failure.
	rep := boardsReportOf(2, []boardReport{
		{ATS: "gh", Token: "a", Outcome: boardRead, Listed: &listed, Reconcile: reconcileCountsOf(pipeline.Summary{Dead: 3, MassDead: 2})},
		{ATS: "gh", Token: "b", Outcome: boardRead, Listed: &listed, Reconcile: reconcileCountsOf(pipeline.Summary{Held: 4, Deferred: 4})},
	})
	if rep.MassDead != 2 || rep.DeathsDeferred != 4 {
		t.Fatalf("totals: %+v", rep)
	}
	waited := histRec(2, outcomeOK)
	waited.Boards = &rep
	waited.Boards.MassDead = 0
	if runHasFailures(waited, histT0) {
		t.Fatal("deaths that wait a read were a failure")
	}
	if s := boardFetchSummary(&rep); !strings.Contains(s, "4 death(s) wait one more read (over 72h0m0s since their last event)") {
		t.Fatalf("summary %q", s)
	}
}

// A clock-behind line says what the ledger holds, and which event.
func TestClockBehindLineNamesTheEventAhead(t *testing.T) {
	n := 5
	ahead := time.Date(2027, 10, 2, 16, 0, 0, 0, time.UTC)
	it := boardReport{ATS: "gh", Token: "x", Outcome: boardRead, Listed: &n,
		Reconcile: reconcileCountsOf(pipeline.Summary{Open: 9, Absent: 4, ClockBehind: true, ClockAhead: ahead})}
	if l := clockBehindLine(it); l != "gh:x: 4 open posting(s) absent were not aged: the ledger holds events stamped ahead of the run's clock, up to 2027-10-02T16:00:00Z" {
		t.Fatalf("line %q", l)
	}
}
