package main

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// The downgrade of a short listing's deaths must not reach a true mass absence
// by the share rule: a full listing (nothing counted but not listed) of a board
// holding 20 open postings that leaves 6 of them out (30%, over the 25% share,
// over the minimum of 10 open), held for the 14-day mass hold and still not
// listed, kills them as mass deaths, and that fails the run (runHasFailures) and
// is named per board. The Swift button follows the same rule (RunFailuresTests).
func TestAMassAbsenceByShareStillFailsTheRunWhenItsHeldPostingsDie(t *testing.T) {
	w, b, all, t0 := shortBoardSetup(t)
	more := make([]posting.Posting, 0, 10)
	for i := 11; i <= 20; i++ {
		p := all[0]
		p.Key = posting.Key(b.KeyPrefix() + "R-" + string(rune('A'+i)))
		p.Title = "Extra " + string(rune('A'+i))
		p.Description = "Extra body " + string(rune('A'+i)) + "."
		more = append(more, p)
		if err := w.led.Append(t.Context(), ledger.Event{V: ledger.SchemaVersion, At: t0, Key: p.Key, Status: posting.Live, Posting: &p}); err != nil {
			t.Fatal(err)
		}
	}
	all = append(all, more...)
	listed := all[:14] // 6 of 20 absent
	at := func(d int) time.Time { return t0.Add(time.Duration(d) * 24 * time.Hour) }
	runOf := func(s pipeline.Summary, bd sources.Board) runRecord {
		n := len(listed)
		rec := histRec(1, outcomeOK)
		br := boardsReportOf(1, []boardReport{{ATS: string(bd.ATS), Token: bd.Token, Company: bd.Company, Outcome: boardRead, Listed: &n, Reconcile: reconcileCountsOf(s)}})
		br.Read = 1
		rec.Boards = &br
		return rec
	}

	var s pipeline.Summary
	for d := 1; d <= 15; d++ {
		s = runListing(t, w, b, listed, 0, at(d)) // a full listing: nothing counted that it did not list
		if d == 1 {
			if !s.Mass || s.MassMarked != 6 {
				t.Fatalf("day 1 did not mass mark by the share rule: %+v", s)
			}
			if runHasFailures(runOf(s, b), at(d)) {
				t.Fatalf("the marking alone failed the run (attention, not a failure): %+v", s)
			}
		}
		if d < 15 && s.Dead != 0 {
			t.Fatalf("day %d: died inside the hold: %+v", d, s)
		}
	}
	if s.Dead != 6 || s.MassDead != 6 || s.ShortDead != 0 {
		t.Fatalf("deaths at the end of a true mass hold: %+v (want 6 dead, MassDead 6, ShortDead 0)", s)
	}
	rec := runOf(s, b)
	if !runHasFailures(rec, at(15)) {
		t.Fatalf("a true mass absence's deaths did not fail the run: %+v", rec.Boards)
	}
	if line := massDeadLine(rec.Boards.Items[0]); line == "" {
		t.Fatal("the failing deaths are not named per board")
	}
}
