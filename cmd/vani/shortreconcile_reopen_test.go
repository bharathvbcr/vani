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

// A short listing is a real read of the postings it lists: a pending reopen
// whose posting it shows again is applied, as a complete listing applies it
// (the earlier rule folded a short listing as a partial one, which applies
// none, so a board short on every run never let a drop back). The rest of the
// listing is treated as a mass absence, so a reopen it does not show is not
// applied.
func TestShortListingAppliesAPendingReopenOfWhatItLists(t *testing.T) {
	w, b, all, t0 := shortBoardSetup(t)
	dropAt := t0.Add(time.Hour)
	drop := func(p posting.Posting) {
		t.Helper()
		if err := w.led.Append(context.Background(), ledger.Event{V: ledger.SchemaVersion, At: dropAt, Key: p.Key, Status: posting.Dropped,
			Reason: "excluded", Annotations: map[string]string{pipeline.AnnExcluded: "test"}}); err != nil {
			t.Fatal(err)
		}
	}
	listedDrop, unlistedDrop := all[8], all[9]
	drop(listedDrop)
	drop(unlistedDrop)
	path := filepath.Join(w.stateDir, pipeline.PendingReopenFile)
	pending := pipeline.NewPendingReopens()
	for _, p := range []posting.Posting{listedDrop, unlistedDrop} {
		pending.Items[p.Key] = pipeline.PendingReopen{Class: "exclusion", At: dropAt, Reason: "test"}
	}
	if err := pending.Save(path, dropAt); err != nil {
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

	at := t0.Add(24 * time.Hour)
	s := runListing(t, w, b, all[:9], 3, at) // lists listedDrop, not unlistedDrop
	if st, _ := ledgerStatusOf(t, w, listedDrop.Key); st == posting.Dropped || s.Reopened != 1 {
		t.Fatalf("a short listing that shows the dropped posting did not apply its pending reopen: status %s, %+v", st, s)
	}
	if st, _ := ledgerStatusOf(t, w, unlistedDrop.Key); st != posting.Dropped {
		t.Fatalf("a posting the short listing left out was reopened: %s", st)
	}
	if n := waiting(at); n != 1 {
		t.Fatalf("%d pending reopens waiting, want only the one the listing did not show", n)
	}

	// Past PendingReopenExpiry the unlisted one expires as in a complete
	// listing: the same 14 days a mass hold lasts.
	// (counted in read time: one short listing a day, the first starting the count).
	late := at
	for d := 1; d <= 15; d++ {
		late = at.Add(time.Duration(d) * 24 * time.Hour)
		runListing(t, w, b, all[:9], 3, late)
	}
	if n := waiting(late); n != 0 {
		t.Fatalf("a pending reopen that stayed out of short listings for %s is still waiting: %d", pipeline.PendingReopenExpiry, n)
	}
}
