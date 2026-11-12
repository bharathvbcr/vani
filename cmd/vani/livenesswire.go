package main

import (
	"fmt"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// livenessOf is the config's mass-absence settings as the reconcile rule takes
// them. config cannot import pipeline, so TestLivenessDefaultsAgree pins the two
// sets of defaults equal.
func livenessOf(cfg config.Config) pipeline.Liveness {
	return pipeline.Liveness{MassShare: cfg.Liveness.Share(), MassMinOpen: cfg.Liveness.MinOpen(), MassHold: cfg.Liveness.Hold()}
}

// newBoardReconciler is the one place a Reconciler that ages postings is made
// for a run: the daemon, run-once and a run request all list boards through
// readBoards, which calls it with the config that run reloaded. (The inbox poll
// reconciles partial listings, which age nothing, so it takes no liveness.) A
// setting the rule refuses fails the run before any board is read, so an
// invalid value is never quietly replaced.
func newBoardReconciler(cfg config.Config, led ledger.Ledger) (*pipeline.Reconciler, error) {
	rec := pipeline.NewReconciler(led)
	if err := rec.SetLiveness(livenessOf(cfg)); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return rec, nil
}

// runReconciler is the Reconciler a run's board listing ages postings with,
// made in one place: the config's liveness settings (newBoardReconciler), the
// records the owner marked protected, and the pending reopens applied as full
// listings show their postings again and cleared when they stay away
// (reopenOnSighting). finish saves the spent reopen set; the caller runs it
// once the listing is done.
func (w *wired) runReconciler(protect func(posting.Key) bool, at time.Time) (rec *pipeline.Reconciler, finish func(), err error) {
	rec, err = newBoardReconciler(w.cfg, w.led)
	if err != nil {
		return nil, nil, err
	}
	rec.Protect(protect)
	return rec, w.reopenOnSighting(rec, at), nil
}
