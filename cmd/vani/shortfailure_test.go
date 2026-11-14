package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// A board that lists short on every run is an ongoing problem the owner sees as
// an attention line each day, and it fails no run: not the run that marks what
// it leaves out, nor the run, two weeks on, in which those postings die. A true
// mass absence's deaths still fail the run. Both go through the Reconciler the
// run uses, then the run record, so the counts are the ones a run writes.
func TestShortListingNeverFailsARunButATrueMassDeathDoes(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	board := sources.Board{ATS: posting.Greenhouse, Token: "shorty", Company: "Shorty"}
	setup := func() (*ledger.File, []posting.Posting) {
		l := newTestLedger(t)
		var all []posting.Posting
		for i := range 20 {
			k := posting.Key("gh:shorty:" + string(rune('a'+i)))
			p := posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Shorty", Title: "T" + string(rune('a'+i)), Description: "Body " + string(rune('a'+i)) + "."}
			if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0, Key: k, Status: posting.Live, Posting: &p}); err != nil {
				t.Fatal(err)
			}
			all = append(all, p)
		}
		return l, all
	}
	runOf := func(s pipeline.Summary) runRecord {
		listed := 18
		rec := histRec(1, outcomeOK)
		br := boardsReportOf(1, []boardReport{{ATS: "gh", Token: "shorty", Outcome: boardRead, Listed: &listed, Reconcile: reconcileCountsOf(s)}})
		br.Read = 1
		rec.Boards = &br
		return rec
	}
	at := func(d int) time.Time { return t0.Add(time.Duration(d) * 24 * time.Hour) }

	// Short listings: 18 of 20 every day.
	l, all := setup()
	var first, last pipeline.Summary
	for d := 1; d <= 15; d++ {
		s, err := pipeline.NewReconciler(l).ReconcileShort(ctx, board, all[2:], at(d))
		if err != nil {
			t.Fatal(err)
		}
		if d == 1 {
			first = s
		}
		last = s
		if d == 1 || d == 15 {
			if runHasFailures(runOf(s), at(d)) {
				t.Fatalf("day %d: a short listing failed the run: %+v", d, s)
			}
		}
	}
	if first.MassMarked != 2 || last.Dead != 2 || last.ShortDead != 2 || last.MassDead != 0 {
		t.Fatalf("first %+v last %+v", first, last)
	}
	rec := runOf(last)
	if rec.Boards.ShortDead != 2 || rec.Boards.MassDead != 0 {
		t.Fatalf("the record does not carry the cause: %+v", rec.Boards)
	}
	if line := boardFetchSummary(rec.Boards); !strings.Contains(line, "2 posting(s) died after a short listing") {
		t.Fatalf("no attention line for the short deaths: %q", line)
	}

	// A true mass absence (a full, empty listing): the deaths fail the run.
	l, _ = setup()
	var deadRun pipeline.Summary
	for d := 1; d <= 15; d++ {
		s, err := pipeline.NewReconciler(l).Reconcile(ctx, board, nil, at(d))
		if err != nil {
			t.Fatal(err)
		}
		if d == 1 && runHasFailures(runOf(s), at(d)) {
			t.Fatalf("the marking of a mass absence failed the run: %+v", s)
		}
		deadRun = s
	}
	if deadRun.MassDead != 20 || !runHasFailures(runOf(deadRun), at(15)) {
		t.Fatalf("a true mass absence's deaths did not fail the run: %+v", deadRun)
	}
}
