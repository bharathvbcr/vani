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
// is applied by the next run's listing. The caller holds the ledger lock, as the retriage that writes the set
// does. A set that cannot be read is applied to nothing and left as it is
// (never taken for an empty one, never overwritten), and the run says so.
func (w *wired) reopenOnSighting(rec *pipeline.Reconciler, at time.Time) (finish func()) {
	path := filepath.Join(w.stateDir, pipeline.PendingReopenFile)
	pending, err := pipeline.LoadPendingReopens(path, at)
	if err != nil {
		w.warnf("pending reopens not applied this run: %v", err)
		return func() {}
	}
	// Always attached, even with nothing pending: a full listing that shows an
	// exclusion drop moved is judged by it too (SightingReopen.movedExclusion).
	so := &pipeline.SightingReopen{Policy: dropPolicy(w.cfg, w.cfg.Decide.OpenRouterName()), Scope: reopenScope(w.cfg), Pending: pending}
	rec.ReopenOnSighting(so)
	return func() {
		if so.Changed() || pending.Expired > 0 {
			if err := pending.Save(path, time.Now()); err != nil {
				w.warnf("pending reopens: %v (the ones applied are applied; the set may name them again, and the next run clears them)", err)
			}
		}
		if so.Applied+so.Cleared+so.Expired+pending.Expired+pending.Len() > 0 {
			w.warnf("pending reopens: %d applied, %d judged again and left dropped, %d cleared unseen, %d still waiting",
				so.Applied, so.Cleared, so.Expired+pending.Expired, pending.Len())
		}
	}
}
