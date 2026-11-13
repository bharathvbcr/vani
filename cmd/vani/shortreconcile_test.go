package main

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// shortBoardSetup stores ten live postings of a Workday board, first seen at
// t0, and returns what a run needs to list that board again.
func shortBoardSetup(t *testing.T) (w *wired, b sources.Board, all []posting.Posting, t0 time.Time) {
	t.Helper()
	l := newTestLedger(t)
	b = sources.Board{ATS: posting.Workday, Token: "acme/External", Company: "Acme"}
	t0 = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	for i := 1; i <= 10; i++ {
		p := posting.Posting{Key: posting.Key(fmt.Sprintf("%sR-%d", b.KeyPrefix(), i)), ATS: posting.Workday, Company: "Acme",
			Title: fmt.Sprintf("Engineer %d", i), Description: fmt.Sprintf("Body %d.", i)}
		if err := l.Append(context.Background(), ledger.Event{V: ledger.SchemaVersion, At: t0, Key: p.Key, Status: posting.Live, Posting: &p}); err != nil {
			t.Fatal(err)
		}
		all = append(all, p)
	}
	w = &wired{cfg: config.Default(), stateDir: t.TempDir(), led: l, warn: &bytes.Buffer{}, live: &liveNote{}}
	return w, b, all, t0
}

// runListing is one run's reconcile of b: a fresh Reconciler, as a run makes,
// folding listed in at at. short is what the adapter reported (Stats.Short).
func runListing(t *testing.T, w *wired, b sources.Board, listed []posting.Posting, short int, at time.Time) pipeline.Summary {
	t.Helper()
	w.mu.Lock()
	w.fetches = nil // a run's fetch counts start empty
	w.mu.Unlock()
	if short > 0 {
		w.noteBoardFetch(b, boardFetch{Short: short})
	}
	rec, finish, err := w.runReconciler(nil, at)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	sum, err := w.reconcileBoard(context.Background(), rec, b, listed, at)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func ledgerStatusOf(t *testing.T, w *wired, k posting.Key) (posting.Status, int) {
	t.Helper()
	r, ok := w.led.Get(k)
	if !ok {
		t.Fatalf("%s is not in the ledger", k)
	}
	return r.Status, r.MissedRuns
}

// A short listing still folds what it lists: new postings are stored, and a
// posting that was absent and is listed again ends its absence episode.
func TestShortListingStillFoldsWhatItLists(t *testing.T) {
	w, b, all, t0 := shortBoardSetup(t)
	gone := all[9].Key
	runListing(t, w, b, all[:9], 0, t0.Add(24*time.Hour)) // a complete read misses it once
	if _, missed := ledgerStatusOf(t, w, gone); missed != 1 {
		t.Fatalf("setup: %d missed runs", missed)
	}
	fresh := posting.Posting{Key: posting.Key(b.KeyPrefix() + "R-99"), ATS: posting.Workday, Company: "Acme", Title: "New role", Description: "New body."}
	listed := append(append([]posting.Posting{}, all...), fresh)
	s := runListing(t, w, b, listed, 3, t0.Add(48*time.Hour))
	if s.New != 1 || s.Reappeared != 1 {
		t.Fatalf("short listing did not fold its rows: %+v", s)
	}
	if st, missed := ledgerStatusOf(t, w, gone); st != posting.Live || missed != 0 {
		t.Fatalf("relisted posting: %s with %d missed", st, missed)
	}
}

// An empty listing that is also short (a board that counted jobs and listed
// none) holds every open posting as a mass absence and kills none; a later
// complete empty listing finds them already marked and holds them too.
func TestShortEmptyListingMassHoldsEveryPosting(t *testing.T) {
	w, b, all, t0 := shortBoardSetup(t)
	s := runListing(t, w, b, nil, 10, t0.Add(24*time.Hour))
	if !s.Mass || s.MassMarked != 10 || s.Dead != 0 {
		t.Fatalf("short empty listing: %+v", s)
	}
	if st, missed := ledgerStatusOf(t, w, all[0].Key); st != posting.Live || missed != 1 {
		t.Fatalf("after a short empty listing: %s, %d missed runs", st, missed)
	}
	s = runListing(t, w, b, nil, 0, t0.Add(48*time.Hour))
	if !s.Mass || s.MassMarked != 0 || s.Held != 10 || s.Dead != 0 {
		t.Fatalf("a complete empty listing after it: %+v", s)
	}
}

// Another board's shortfall does not stop this board aging: the signal is per
// board.
func TestShortListingIsPerBoard(t *testing.T) {
	w, b, all, t0 := shortBoardSetup(t)
	other := sources.Board{ATS: posting.Workday, Token: "other/Site"}
	w.mu.Lock()
	w.fetches = nil
	w.mu.Unlock()
	w.noteBoardFetch(other, boardFetch{Short: 5})
	rec, finish, err := w.runReconciler(nil, t0.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	s, err := w.reconcileBoard(context.Background(), rec, b, all[:9], t0.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if s.Missed != 1 {
		t.Fatalf("another board's shortfall stopped this board aging: %+v", s)
	}
}
