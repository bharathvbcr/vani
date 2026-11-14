package main

import (
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// A run fails only when postings its board held die. A freeze kills nothing, so
// the freeze a clock set back by days causes (a quorum of the board's own
// events 3 to 30 days ahead, liveness.go "The clock") is an attention line and
// not a failure. The older guard (events within ClockWindow ahead: a fresh
// jump, probably real) keeps failing the run, as it always did.
func TestClockSetBackFreezeIsAttentionAndTheOlderGuardStillFailsTheRun(t *testing.T) {
	ctx := t.Context()
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	board := sources.Board{ATS: posting.Greenhouse, Token: "freeze", Company: "Freeze"}
	seed := func() *ledger.File {
		l := newTestLedger(t)
		for i := range 20 {
			k := posting.Key("gh:freeze:" + string(rune('a'+i)))
			p := posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Freeze", Title: "T" + string(rune('a'+i)), Description: "Body " + string(rune('a'+i)) + "."}
			if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0, Key: k, Status: posting.Live, Posting: &p}); err != nil {
				t.Fatal(err)
			}
		}
		return l
	}
	runOf := func(s pipeline.Summary) runRecord {
		n := 0
		rec := histRec(1, outcomeOK)
		br := boardsReportOf(1, []boardReport{{ATS: "gh", Token: "freeze", Company: "Freeze", Outcome: boardRead, Listed: &n, Reconcile: reconcileCountsOf(s)}})
		br.Read = 1
		rec.Boards = &br
		return rec
	}
	d0 := t0.Add(20 * 24 * time.Hour)

	// Set back by days: the events of a mass-marking read at d0 are 9 days ahead.
	l := seed()
	if _, err := pipeline.NewReconciler(l).Reconcile(ctx, board, nil, d0); err != nil {
		t.Fatal(err)
	}
	back := d0.Add(24 * time.Hour).Add(-10 * 24 * time.Hour)
	s, err := pipeline.NewReconciler(l).Reconcile(ctx, board, nil, back)
	if err != nil {
		t.Fatal(err)
	}
	if !s.ClockBehind || !s.ClockSetBack || s.Dead != 0 {
		t.Fatalf("setup: the set-back read should freeze the board and say it is a set-back: %+v", s)
	}
	rec := runOf(s)
	if runHasFailures(rec, back) {
		t.Fatalf("a freeze that kills nothing failed the run: %+v", rec.Boards)
	}
	if rec.Boards.ClockBehind != 0 || rec.Boards.ClockSetBack != 1 {
		t.Fatalf("the record counts the freeze as a failing clock-behind board: %+v", rec.Boards)
	}
	if line := boardFetchSummary(rec.Boards); !strings.Contains(line, "clock set back") {
		t.Fatalf("no attention line for the freeze: %q", line)
	}

	// Events a day ahead (a fresh jump): the older guard, still a failure.
	l = seed()
	if _, err := pipeline.NewReconciler(l).Reconcile(ctx, board, nil, d0); err != nil {
		t.Fatal(err)
	}
	s, err = pipeline.NewReconciler(l).Reconcile(ctx, board, nil, d0.Add(-20*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !s.ClockBehind || s.ClockSetBack {
		t.Fatalf("setup: events within the window should be the older guard: %+v", s)
	}
	rec = runOf(s)
	if !runHasFailures(rec, d0) || rec.Boards.ClockBehind != 1 || rec.Boards.ClockSetBack != 0 {
		t.Fatalf("the older guard stopped failing the run: %+v", rec.Boards)
	}
}
