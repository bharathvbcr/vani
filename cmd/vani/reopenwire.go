package main

import (
	"path/filepath"
	"time"

	"github.com/bharathvbcr/Vani/internal/pipeline"
)

// reopenOnSighting has rec apply the pending reopens `vani retriage` and the
// run's exclusion pass (applyExclusions) recorded as the run's full listings
// show their postings again, judged under the run's drop policy, and returns
// what saves the spent set once the listing is done. The listing is the run's
// first stage and the exclusion pass a later one, so a reopen marked by a pass
// is applied by the next run's listing. The pass stays after the listing on
// purpose (checked 2026-11-12, leftover 5): it drops the postings this run's
// listing just added that an exclusion matches before anything is advanced
// (pipeline.Run), and it reads the body of every excluded or excludable record,
// which a second pass before the listing would repeat for the whole ledger. A
// reopen-only pre-pass would need a stage of its own and the same body reads.
// The caller holds the ledger lock, as the retriage that writes the set
// does. A set that cannot be read is applied to nothing and left as it is
// (never taken for an empty one, never overwritten), and the run says so.
func (w *wired) reopenOnSighting(rec *pipeline.Reconciler, at time.Time) (finish func()) {
	path := filepath.Join(w.stateDir, pipeline.PendingReopenFile)
	pending, err := pipeline.LoadPendingReopens(path, at)
	if err != nil {
		w.warnf("pending reopens not applied this run: %v", err)
		return func() {}
	}
	// Entries of a board no longer on the watchlist will never be shown (an empty
	// watchlist clears nothing: that is a config the run reports elsewhere).
	// The whole watchlist, never a run's cut: a run limited to some boards
	// would otherwise clear, and save away, every other board's entries.
	whole := w.wholeWatchlist()
	if len(whole) > 0 {
		pending.ClearUnwatched(pipeline.WatchedBy(whole))
	}
	// Always attached, even with nothing pending: a full listing that shows an
	// exclusion drop moved is judged by it too (SightingReopen.movedExclusion).
	sc := reopenScope(w.cfg)
	sc.Watched = pipeline.WatchedBy(whole)
	so := &pipeline.SightingReopen{Policy: dropPolicy(w.cfg, w.cfg.Decide.DecisionName()), Scope: sc, Pending: pending}
	rec.ReopenOnSighting(so)
	return func() {
		// A board's events become durable together at the end of the board
		// (pipeline.Reconciler, ledger.Group), and the set forgets a reopen when
		// the board writes it. If the ledger broke, the board's reopens may never
		// have reached the disk: keep the set as it was, so the next run applies
		// them again (one already durable is named again and cleared then).
		if err := ledgerBroken(w.led); err != nil {
			w.warnf("pending reopens: not saved, the ledger failed during the listing: %v", err)
			return
		}
		if so.Changed() || pending.Expired > 0 {
			if err := pending.Save(path, time.Now()); err != nil {
				w.warnf("pending reopens: %v (the ones applied are applied; the set may name them again, and the next run clears them)", err)
			}
		}
		if so.Applied+so.Cleared+so.Expired+pending.Expired+pending.Len()+pending.GoneLen() > 0 {
			w.warnf("pending reopens: %d applied, %d judged again and left dropped, %d cleared unseen, %d still waiting, %d expired unseen and not marked again until listed",
				so.Applied, so.Cleared, so.Expired+pending.Expired, pending.Len(), pending.GoneLen())
		}
	}
}
