package pipeline

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// shortRead reconciles ids of b as a short listing (the board counted more
// rows than it listed) at at.
func shortRead(t *testing.T, l ledger.Ledger, b sources.Board, at time.Time, ids []int) Summary {
	t.Helper()
	var listed []posting.Posting
	for _, id := range ids {
		listed = append(listed, probePost(t, b, id))
	}
	s, err := NewReconciler(l).ReconcileShort(ctx, b, listed, at)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A board that lists short on every run holds what it leaves out for the mass
// hold, and those deaths are expected closures: ShortDead, not MassDead (which
// fails the run). The cause is recorded on the posting (MassShortKey). A true
// mass absence in the same episode upgrades it: its deaths are MassDead.
func TestShortListingDeathsAreShortDeadNotMassDead(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("shorty")
	reviewRead(t, l, b, t0, idRange(0, 20))
	// Day 1: a short listing missing 2 of 20 (10%: not a mass absence by share).
	s := shortRead(t, l, b, t0.Add(24*time.Hour), idRange(2, 20))
	if !s.Mass || s.MassMarked != 2 {
		t.Fatalf("the short listing did not mass mark what it left out: %+v", s)
	}
	if r := rec(t, l, probeKey(t, b, 0)); r.Annotations[MassShortKey] != "short" {
		t.Fatalf("no cause recorded on the held posting: %v", r.Annotations)
	}
	var last Summary
	for d := 2; d <= 15; d++ {
		last = shortRead(t, l, b, t0.Add(time.Duration(d)*24*time.Hour), idRange(2, 20))
		if d < 15 && last.Dead != 0 {
			t.Fatalf("day %d: died inside the hold: %+v", d, last)
		}
	}
	if last.Dead != 2 || last.ShortDead != 2 || last.MassDead != 0 {
		t.Fatalf("deaths after the short hold: %+v (want 2 dead, ShortDead 2, MassDead 0)", last)
	}
}

func TestShortThenTrueMassUpgradesTheCause(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("upgrade")
	reviewRead(t, l, b, t0, idRange(0, 20))
	shortRead(t, l, b, t0.Add(24*time.Hour), idRange(2, 20)) // 0 and 1 short-marked
	// A full listing that shows none of the board: a true mass absence.
	s := reviewRead(t, l, b, t0.Add(48*time.Hour), nil)
	if !s.Mass {
		t.Fatalf("setup: %+v", s)
	}
	if r := rec(t, l, probeKey(t, b, 0)); r.Annotations[MassShortKey] != "" || r.Annotations[MassMissedSinceKey] == "" {
		t.Fatalf("a true mass absence did not upgrade the cause: %v", r.Annotations)
	}
	var total Summary
	for d := 3; d <= 17; d++ {
		s := reviewRead(t, l, b, t0.Add(time.Duration(d)*24*time.Hour), nil)
		total.Dead, total.MassDead, total.ShortDead = total.Dead+s.Dead, total.MassDead+s.MassDead, total.ShortDead+s.ShortDead
	}
	if total.Dead != 20 || total.MassDead != 20 || total.ShortDead != 0 {
		t.Fatalf("deaths: %+v", total)
	}
}

// A posting marked before the cause was recorded (no mass_short) counts as a
// mass death: the check fails closed.
func TestMassMarkWithoutACauseCountsAsMass(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("legacy")
	reviewRead(t, l, b, t0, idRange(0, 20))
	reviewRead(t, l, b, t0.Add(24*time.Hour), nil) // true mass: marked, no short cause
	var last Summary
	for d := 2; d <= 15; d++ {
		last = reviewRead(t, l, b, t0.Add(time.Duration(d)*24*time.Hour), nil)
	}
	if last.MassDead != 20 || last.ShortDead != 0 {
		t.Fatalf("%+v", last)
	}
}
