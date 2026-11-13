package main

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func massSinceOf(t *testing.T, w *wired, k posting.Key) string {
	t.Helper()
	r, ok := w.led.Get(k)
	if !ok {
		t.Fatalf("%s is not in the ledger", k)
	}
	return r.Annotations[pipeline.MassMissedSinceKey]
}

// A listing short of its count (Stats.Short > 0) could not see every posting,
// so the ones it left out are not known to be gone: a posting that three short
// listings in a row leave out is still live, and mass-held (the ordinary rule
// would have killed it on the second miss).
func TestShortListingMassHoldsWhatItLeavesOut(t *testing.T) {
	w, b, all, t0 := shortBoardSetup(t)
	missing := all[9].Key
	listed := all[:9]

	for day := 1; day <= 3; day++ {
		s := runListing(t, w, b, listed, 2, t0.Add(time.Duration(day)*24*time.Hour))
		if s.Dead != 0 || s.Absent != 1 || !s.Mass {
			t.Fatalf("short listing %d: %+v", day, s)
		}
		if day == 1 && s.MassMarked != 1 {
			t.Fatalf("the first short listing did not mark the posting: %+v", s)
		}
		if day >= 2 && s.Held != 1 {
			t.Fatalf("short listing %d did not hold the posting past its miss count: %+v", day, s)
		}
	}
	if st, missed := ledgerStatusOf(t, w, missing); st != posting.Live || missed != 3 {
		t.Fatalf("after 3 short listings: %s with %d missed runs, want live with 3", st, missed)
	}
	if massSinceOf(t, w, missing) == "" {
		t.Fatal("the posting a short listing left out carries no mass mark")
	}
}

// The mass hold is 14 days from the posting's own first miss: still live just
// before, dead at it, by a listing that is short every day and never shows it.
func TestShortListingMassHoldEndsAtFourteenDays(t *testing.T) {
	w, b, all, t0 := shortBoardSetup(t)
	missing := all[9].Key
	listed := all[:9]
	first := t0.Add(24 * time.Hour)
	for at := first; at.Before(first.Add(pipeline.DefaultMassHold)); at = at.Add(24 * time.Hour) {
		runListing(t, w, b, listed, 2, at)
		if st, _ := ledgerStatusOf(t, w, missing); st != posting.Live {
			t.Fatalf("dead at %s, before the 14-day mass hold (first miss %s)", at, first)
		}
	}
	s := runListing(t, w, b, listed, 2, first.Add(pipeline.DefaultMassHold))
	if st, _ := ledgerStatusOf(t, w, missing); st != posting.Dead || s.Dead != 1 || s.MassDead != 1 {
		t.Fatalf("after 14 days of short listings that never show it: %s, %+v", st, s)
	}
}

// A posting that a later short listing shows again ends its episode: every
// miss mark, the mass mark included, is cleared.
func TestShortListingThatShowsThePostingClearsItsMarks(t *testing.T) {
	w, b, all, t0 := shortBoardSetup(t)
	missing := all[9].Key
	runListing(t, w, b, all[:9], 2, t0.Add(24*time.Hour))
	runListing(t, w, b, all[:9], 2, t0.Add(48*time.Hour))
	if massSinceOf(t, w, missing) == "" {
		t.Fatal("setup: the posting is not mass held")
	}
	s := runListing(t, w, b, all, 2, t0.Add(72*time.Hour))
	if s.Reappeared != 1 {
		t.Fatalf("the short listing that lists it did not see it reappear: %+v", s)
	}
	if st, missed := ledgerStatusOf(t, w, missing); st != posting.Live || missed != 0 {
		t.Fatalf("after reappearing: %s with %d missed runs", st, missed)
	}
	if massSinceOf(t, w, missing) != "" {
		t.Fatal("the mass mark survived the posting reappearing")
	}
	// Its next absence is a new episode, held from its own new first miss.
	runListing(t, w, b, all[:9], 2, t0.Add(96*time.Hour))
	if _, missed := ledgerStatusOf(t, w, missing); missed != 1 {
		t.Fatalf("a new episode did not restart at one miss: %d", missed)
	}
}

// A complete listing is still the board's whole word: an ordinary absence
// dies on the second daily miss (the 20-hour hold), not after 14 days.
func TestCompleteListingStillKillsAnOrdinaryAbsenceOnTheSecondMiss(t *testing.T) {
	w, b, all, t0 := shortBoardSetup(t)
	missing := all[9].Key
	s := runListing(t, w, b, all[:9], 0, t0.Add(24*time.Hour))
	if st, missed := ledgerStatusOf(t, w, missing); st != posting.Live || missed != 1 || s.Mass {
		t.Fatalf("first complete miss: %s, %d missed, %+v", st, missed, s)
	}
	s = runListing(t, w, b, all[:9], 0, t0.Add(48*time.Hour))
	if st, _ := ledgerStatusOf(t, w, missing); st != posting.Dead || s.Dead != 1 || s.MassDead != 0 {
		t.Fatalf("a complete listing that leaves it out twice: %s, %+v", st, s)
	}
}
