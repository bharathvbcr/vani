package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A short listing is reconciled as a partial one, and a partial listing
// deliberately skips the pending-reopen sighting path (reconcile.go: the apply
// is `full && ...`, and the expiry pass runs only after the `!full` return):
// a short listing's silence about a posting is not an absence, so it neither
// reopens a drop nor clears a reopen that waits. The reconciler is still the
// run's (runReconciler), so a later complete listing that shows the posting
// applies the reopen (here an exclusion-class one).
func TestShortListingLeavesPendingReopensWaitingForAFullOne(t *testing.T) {
	w, b, all, t0 := shortBoardSetup(t)
	target := all[9].Key
	p := all[9]
	if err := w.led.Append(context.Background(), ledger.Event{V: ledger.SchemaVersion, At: t0.Add(time.Hour), Key: target, Status: posting.Dropped, Reason: "excluded", Annotations: map[string]string{pipeline.AnnExcluded: "test"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(w.stateDir, pipeline.PendingReopenFile)
	pending := pipeline.NewPendingReopens()
	pending.Items[target] = pipeline.PendingReopen{Class: "exclusion", At: t0.Add(time.Hour), Reason: "test"}
	if err := pending.Save(path, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	waiting := func(at time.Time) int {
		t.Helper()
		s, err := pipeline.LoadPendingReopens(path, at)
		if err != nil {
			t.Fatal(err)
		}
		return s.Len()
	}

	// Short runs that do list the dropped posting, and one past the expiry
	// that does not list it: nothing is applied, nothing is cleared.
	runListing(t, w, b, all, 3, t0.Add(24*time.Hour))
	if st, _ := ledgerStatusOf(t, w, target); st != posting.Dropped {
		t.Fatalf("a short listing reopened a drop: %s", st)
	}
	runListing(t, w, b, all[:9], 3, t0.Add(pipeline.PendingReopenExpiry+48*time.Hour))
	if n := waiting(t0.Add(48 * time.Hour)); n != 1 {
		t.Fatalf("a short listing that left the posting out cleared its pending reopen: %d waiting", n)
	}

	// A complete listing that shows the posting again applies the reopen.
	s := runListing(t, w, b, all, 0, t0.Add(pipeline.PendingReopenExpiry+72*time.Hour))
	if st, _ := ledgerStatusOf(t, w, target); st == posting.Dropped || s.Reopened != 1 {
		t.Fatalf("a complete listing did not apply the pending reopen: status %s, %+v (posting %s)", st, s, p.Key)
	}
	if n := waiting(t0.Add(pipeline.PendingReopenExpiry + 72*time.Hour)); n != 0 {
		t.Fatalf("an applied reopen is still waiting: %d", n)
	}
}
