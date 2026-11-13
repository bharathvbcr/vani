package pipeline

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// sightingKind is sighting for a full listing of the given kind: a complete
// one, one short of its count, or one that skipped rows it could not read.
func sightingKind(t *testing.T, l ledger.Ledger, s *PendingReopens, p DropPolicy, at time.Time, kind shortKind, ps ...posting.Posting) (Summary, *SightingReopen) {
	t.Helper()
	rc := NewReconciler(l)
	so := &SightingReopen{Policy: p, Scope: reviewScope(), Pending: s}
	rc.ReopenOnSighting(so)
	var (
		sum Summary
		err error
	)
	switch kind {
	case shortCounted:
		sum, err = rc.ReconcileShort(ctx, board, ps, at)
	case shortUnreadable:
		sum, err = rc.ReconcileUnreadable(ctx, board, ps, at)
	default:
		sum, err = rc.Reconcile(ctx, board, ps, at)
	}
	if err != nil {
		t.Fatal(err)
	}
	return sum, so
}

// A pending reopen expires on 14 days of read time in which the board's listing
// left the posting out. A listing that is short of its count, or skipped rows
// it could not read, cannot say the posting is gone (it may be the row it could
// not read, or one it did not list), so it counts nothing: a dropped posting
// hidden behind an unreadable row must stay pending, not expire and stay
// dropped. Only a clean full listing counts. Title, model and exclusion entries
// alike.
func TestOnlyACleanFullListingCountsTowardPendingReopenExpiry(t *testing.T) {
	for name, kind := range map[string]shortKind{"short": shortCounted, "unreadable": shortUnreadable} {
		t.Run(name, func(t *testing.T) {
			l := openLedger(t)
			s := NewPendingReopens()
			classes := map[string]string{"1": reopenClassTitle, "2": reopenClassModel, "3": reopenClassExclusion}
			var keys []posting.Key
			for id, class := range classes {
				k := excludedDrop(t, l, id, "London", excludeLocation("London"))
				s.Items[k] = PendingReopen{Class: class, At: t0}
				keys = append(keys, k)
			}
			other := ghPost(t, "9", "other jd")
			at := t0.Add(time.Hour)
			day := func(d int) time.Time { return at.Add(time.Duration(d) * 24 * time.Hour) }

			// A short or skipped-row listing every day for 40 days expires nothing.
			for d := 1; d <= 40; d++ {
				if _, so := sightingKind(t, l, s, loosened, day(d), kind, other); so.Expired != 0 || s.Len() != 3 {
					t.Fatalf("day %d: a %s listing expired pending reopens: expired %d, %d pending", d, name, so.Expired, s.Len())
				}
			}
			for _, k := range keys {
				if rec(t, l, k).Status != posting.Dropped {
					t.Fatalf("%s left the drop %s: %s", name, k, rec(t, l, k).Status)
				}
			}
			if s.GoneLen() != 0 {
				t.Fatalf("%d marked gone by %s listings", s.GoneLen(), name)
			}

			// Clean full listings count, and only they: about 13 days of them is not enough.
			for d := 41; d <= 53; d++ {
				if _, so := sightingKind(t, l, s, loosened, day(d), shortNone, other); so.Expired != 0 || s.Len() != 3 {
					t.Fatalf("day %d: expired with under 14 days of clean read time: expired %d, %d pending", d, so.Expired, s.Len())
				}
			}
			expired := 0
			for d := 54; d <= 60 && s.Len() > 0; d++ {
				_, so := sightingKind(t, l, s, loosened, day(d), shortNone, other)
				expired += so.Expired
			}
			if expired != 3 || s.Len() != 0 {
				t.Fatalf("clean full listings never expired them: %d expired, %d pending", expired, s.Len())
			}
		})
	}
}

// Expiry is the only thing a short or skipped-row listing leaves alone: a
// board that is short on every run still lets a drop back when the listing
// shows the posting.
func TestPendingReopenStillAppliesWhenAShortOrSkippedRowListingShowsThePosting(t *testing.T) {
	for name, kind := range map[string]shortKind{"short": shortCounted, "unreadable": shortUnreadable} {
		t.Run(name, func(t *testing.T) {
			l := openLedger(t)
			k := excludedDrop(t, l, "1", "London", excludeLocation("London"))
			other := ghPost(t, "2", "other jd")
			s := NewPendingReopens()
			sc := deferScope(s)
			at := t0.Add(time.Hour)
			if _, err := ApplyExclusions(ctx, l, at, loosened, nil, nil, &sc); err != nil || s.Len() != 1 {
				t.Fatalf("mark: %d (%v)", s.Len(), err)
			}
			// It is not listed for a while: still pending.
			for d := 1; d <= 5; d++ {
				sightingKind(t, l, s, loosened, at.Add(time.Duration(d)*24*time.Hour), kind, other)
			}
			if s.Len() != 1 || rec(t, l, k).Status != posting.Dropped {
				t.Fatalf("setup: %d pending, status %s", s.Len(), rec(t, l, k).Status)
			}
			// Then the listing shows it.
			sum, _ := sightingKind(t, l, s, loosened, at.Add(6*24*time.Hour), kind, other, listedAgain(t, l, k))
			if sum.Reopened != 1 || rec(t, l, k).Status == posting.Dropped || s.Len() != 0 {
				t.Fatalf("a %s listing that shows the posting did not reopen it: %+v, status %s, %d pending", name, sum, rec(t, l, k).Status, s.Len())
			}
		})
	}
}
