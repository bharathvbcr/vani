package main

import (
	"context"
	"time"

	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// boardShort is how many rows b's adapter counted and did not list this run
// (boardFetch.Short). Adapters report it from List, before the board is
// reconciled, so it is known by the time reconcileBoard asks.
func (w *wired) boardShort(b sources.Board) int {
	return w.boardFetchOf(b).Short
}

// boardFetchOf is the fetch counts the adapters reported for b this run.
func (w *wired) boardFetchOf(b sources.Board) boardFetch {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fetches[fetchKey(b.ATS, b.Token)]
}

// reconcileBoard folds one board's listing into the ledger through rec.
//
// A listing that counted more rows than it listed (Short > 0) is the board's
// word about what it listed, not about what it left out: the postings behind
// the missing rows were never seen, so they are not known to be gone. It is
// reconciled by pipeline.ReconcileShort: a full reconcile, so sightings,
// pending reopens and field updates apply to the postings it lists, in which
// every open posting it leaves out is treated as a mass absence (the liveness
// rule's own path). Such a posting is kept and held for the mass hold from
// its own first miss, and never killed by the ordinary two-miss rule, so a
// daily run cannot kill one on the second day of a short listing, and a board
// that is short on every run still ages, after the hold. The run record names
// the shortfall (Boards.Short) and the newly held postings (Boards.Mass).
//
// A windowed listing (boardFetch.Windowed: a search that is never listed in
// full, or one restricted by recency) is reconciled by
// pipeline.ReconcileWindow: what it leaves out is held the same way, but an
// empty window is never a true mass absence, and since the window is the
// board's design rather than a fault it is not warned about on each run. Rows
// it could not read still win (ReconcileUnreadable), as on any board.
func (w *wired) reconcileBoard(ctx context.Context, rec *pipeline.Reconciler, b sources.Board, listed []posting.Posting, now time.Time) (pipeline.Summary, error) {
	f := w.boardFetchOf(b)
	if f.BadRows > 0 {
		// Rows it could not read may be postings the ledger holds: this listing
		// holds what it leaves out and kills none of it (ReconcileUnreadable).
		w.warnf("board %s/%s: %d row(s) could not be read; folded as a full listing that holds every posting it leaves out and kills none of them", b.ATS, b.Token, f.BadRows)
		return rec.ReconcileUnreadable(ctx, b, listed, now)
	}
	if f.Windowed {
		return rec.ReconcileWindow(ctx, b, listed, now)
	}
	if short := f.Short; short > 0 {
		w.warnf("board %s/%s: counted %d job(s) it did not list; folded as a full listing whose absent postings are held as a mass absence, not aged by the ordinary rule", b.ATS, b.Token, short)
		return rec.ReconcileShort(ctx, b, listed, now)
	}
	return rec.Reconcile(ctx, b, listed, now)
}
