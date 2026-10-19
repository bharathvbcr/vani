package pipeline

import (
	"context"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// reconcileAll is runOnce's board loop.
func reconcileAll(ctx context.Context, l ledger.Ledger, boards []sources.Board, listed [][]posting.Posting, now time.Time) error {
	rc := NewReconciler(l)
	for i, b := range boards {
		if _, err := rc.Reconcile(ctx, b, listed[i], now); err != nil {
			return err
		}
	}
	return nil
}
