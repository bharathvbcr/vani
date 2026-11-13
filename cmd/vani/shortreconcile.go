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
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fetches[fetchKey(b.ATS, b.Token)].Short
}

// reconcileBoard folds one board's listing into the ledger through rec.
//
// A listing that counted more rows than it listed (Short > 0) is the board's
// word about what it listed, not about what it left out: the postings behind
// the missing rows were never seen, so they are not absent. It is reconciled
// as a partial listing (pipeline.ReconcilePartial): every listed posting is
// folded as usual, and no stored posting is missed, mass-marked or aged.
// Otherwise a daily run would kill a posting on the second day of a short
// listing. A board that is short on every run therefore ages nothing until a
// run lists it in full; the run record still names the shortfall (Boards.Short)
// and board health records the last complete listing.
func (w *wired) reconcileBoard(ctx context.Context, rec *pipeline.Reconciler, b sources.Board, listed []posting.Posting, now time.Time) (pipeline.Summary, error) {
	if short := w.boardShort(b); short > 0 {
		w.warnf("board %s/%s: counted %d job(s) it did not list; folded as a partial listing, so no stored posting aged", b.ATS, b.Token, short)
		return rec.ReconcilePartial(ctx, b, listed, now)
	}
	return rec.Reconcile(ctx, b, listed, now)
}
