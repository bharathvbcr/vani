package main

import (
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// runWithBadRows is one run's reconcile of b whose adapter skipped bad rows it
// could not read (through the run's own badRowSink, as an adapter reports them).
func runWithBadRows(t *testing.T, w *wired, b sources.Board, listed []posting.Posting, bad int, at time.Time) pipeline.Summary {
	t.Helper()
	w.mu.Lock()
	w.fetches = nil
	w.mu.Unlock()
	if bad > 0 {
		w.badRowSink(b, bad, "empty title")
	}
	rec, finish, err := w.runReconciler(nil, at)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	sum, err := w.reconcileBoard(t.Context(), rec, b, listed, at)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func badRowRun(b sources.Board, s pipeline.Summary) runRecord {
	listed := 9
	rec := histRec(1, outcomeOK)
	br := boardsReportOf(1, []boardReport{{ATS: string(b.ATS), Token: b.Token, Company: b.Company, Outcome: boardRead, Listed: &listed, Reconcile: reconcileCountsOf(s)}})
	br.Read = 1
	rec.Boards = &br
	return rec
}

// A posting whose own row the adapter cannot read is absent from every listing,
// and looks exactly like a posting its board stopped listing. Treated as an
// ordinary short listing, it would die after the mass hold as an "expected
// closure" that fails no run: a probably open posting killed with no sign. A
// listing that skipped rows never kills what it leaves out; once the rows read
// again (and the posting is still not listed) its death fails the run and the
// report names the board.
func TestAPostingWhoseRowIsUnreadableEveryRunIsNeverKilledQuietly(t *testing.T) {
	w, b, all, t0 := shortBoardSetup(t)
	missing := all[9].Key
	listed := all[:9]
	at := func(d int) time.Time { return t0.Add(time.Duration(d) * 24 * time.Hour) }

	for d := 1; d <= 20; d++ {
		s := runWithBadRows(t, w, b, listed, 1, at(d))
		if st, _ := ledgerStatusOf(t, w, missing); st != posting.Live {
			t.Fatalf("day %d: the posting behind an unreadable row is %s after %d days of unreadable rows (summary %+v), with failure=%v",
				d, st, d, s, runHasFailures(badRowRun(b, s), at(d)))
		}
		if s.Dead != 0 {
			t.Fatalf("day %d: a listing that skipped rows killed %d posting(s): %+v", d, s.Dead, s)
		}
	}

	// The rows read again and the posting is still not listed: it dies, loudly.
	s := runWithBadRows(t, w, b, listed, 0, at(21))
	if st, _ := ledgerStatusOf(t, w, missing); st != posting.Dead {
		t.Fatalf("after a clean listing that still lacks it: %s, summary %+v", st, s)
	}
	if s.MassDead != 1 || s.ShortDead != 0 || s.UnreadableDead != 1 {
		t.Fatalf("the death is not counted as a failing one with its cause: %+v", s)
	}
	rec := badRowRun(b, s)
	if !runHasFailures(rec, at(21)) {
		t.Fatalf("a posting held by unreadable rows died and the run did not fail: %+v", rec.Boards)
	}
	line := massDeadLine(rec.Boards.Items[0])
	if !strings.Contains(line, b.Token) || !strings.Contains(line, "unreadable") {
		t.Fatalf("the report does not name the board and the cause: %q", line)
	}
}

// Skipped rows are not an ordinary short listing: a board that lists short for
// a detail cap keeps the short rule (its deaths are expected closures), and the
// same board with unreadable rows does not.
func TestOrdinaryShortListingStillDiesAsAShortDeath(t *testing.T) {
	w, b, all, t0 := shortBoardSetup(t)
	missing := all[9].Key
	var last pipeline.Summary
	for d := 1; d <= 15; d++ {
		last = runListing(t, w, b, all[:9], 2, t0.Add(time.Duration(d)*24*time.Hour))
	}
	if st, _ := ledgerStatusOf(t, w, missing); st != posting.Dead || last.ShortDead != 1 || last.MassDead != 0 || last.UnreadableDead != 0 {
		t.Fatalf("an ordinary short listing changed: %s %+v", st, last)
	}
}
