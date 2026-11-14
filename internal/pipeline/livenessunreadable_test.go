package pipeline

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// unreadableRead reconciles ids of b as a listing that skipped rows it could not
// read (ReconcileUnreadable).
func unreadableRead(t *testing.T, l ledger.Ledger, b sources.Board, at time.Time, ids []int) Summary {
	t.Helper()
	var listed []posting.Posting
	for _, id := range ids {
		listed = append(listed, probePost(t, b, id))
	}
	s, err := NewReconciler(l).ReconcileUnreadable(ctx, b, listed, at)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A listing that skipped unreadable rows never kills what it leaves out, past
// any hold; the posting is marked with its own cause, so when a later listing
// (short or complete) that skipped nothing still lacks it, it dies as a mass
// death (a failing one), counted as unreadable. The cause survives an ordinary
// short listing in between, and a true mass absence takes it over.
func TestUnreadableListingHoldsForeverAndItsDeathFails(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("unread")
	reviewRead(t, l, b, t0, idRange(0, 20))
	var s Summary
	for d := 1; d <= 40; d++ {
		s = unreadableRead(t, l, b, t0.Add(time.Duration(d)*24*time.Hour), idRange(2, 20))
		if s.Dead != 0 {
			t.Fatalf("day %d: a listing that skipped rows killed %d: %+v", d, s.Dead, s)
		}
	}
	if r := rec(t, l, probeKey(t, b, 0)); r.Status != posting.Live || r.Annotations[MassShortKey] != "unreadable" {
		t.Fatalf("held posting: %s %v", r.Status, r.Annotations)
	}
	// An ordinary short listing in between keeps the cause.
	s = shortRead(t, l, b, t0.Add(41*24*time.Hour), idRange(2, 20))
	if s.Dead != 2 || s.MassDead != 2 || s.UnreadableDead != 2 || s.ShortDead != 0 {
		t.Fatalf("the death after the unreadable hold: %+v (want 2 dead, MassDead 2, UnreadableDead 2, ShortDead 0)", s)
	}
}

// A true mass absence (an empty listing) in the episode is a mass death whose
// cause is the mass, not unreadable rows.
func TestTrueMassTakesOverFromUnreadable(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("unread2")
	reviewRead(t, l, b, t0, idRange(0, 20))
	unreadableRead(t, l, b, t0.Add(24*time.Hour), idRange(2, 20))
	if _, err := NewReconciler(l).Reconcile(ctx, b, nil, t0.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if r := rec(t, l, probeKey(t, b, 5)); r.Annotations[MassShortKey] != "" {
		t.Fatalf("a true mass absence left the cause %q", r.Annotations[MassShortKey])
	}
}
