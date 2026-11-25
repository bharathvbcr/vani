package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// windowRead reconciles ids of b as a windowed listing (a search listed in part
// by design, or restricted by recency) at at.
func windowRead(t *testing.T, l ledger.Ledger, b sources.Board, at time.Time, ids []int) Summary {
	t.Helper()
	s, err := NewReconciler(l).ReconcileWindow(ctx, b, probePosts(t, b, ids), at)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func probePosts(t *testing.T, b sources.Board, ids []int) []posting.Posting {
	t.Helper()
	var listed []posting.Posting
	for _, id := range ids {
		listed = append(listed, probePost(t, b, id))
	}
	return listed
}

func day(d int) time.Time { return t0.Add(time.Duration(d) * 24 * time.Hour) }

// What a windowed listing leaves out is held for the mass hold from its own
// first miss and then dies as an expected closure (ShortDead), never as a mass
// death, however much of the board it leaves out: an empty window (nothing
// posted in the last N days) included. It reports no mass absence. The short
// listing row is the contrast: a short listing that shows none of the board is
// a true mass absence, and its deaths are MassDead.
func TestWindowedAbsencesDieAsShortDeadAfterTheHold(t *testing.T) {
	for name, tc := range map[string]struct {
		open   int
		listed []int
		short  bool // read as ReconcileShort instead (contrast)
		// want: deaths on day 15, and how they count
		dead, shortDead, massDead int
	}{
		"two of twenty out of the window":     {open: 20, listed: idRange(2, 20), dead: 2, shortDead: 2},
		"most of the board out of the window": {open: 20, listed: idRange(0, 5), dead: 15, shortDead: 15},
		"empty window, twenty open":           {open: 20, listed: nil, dead: 20, shortDead: 20},
		"empty window, two open":              {open: 2, listed: nil, dead: 2, shortDead: 2},
		"empty window, one open":              {open: 1, listed: nil, dead: 1, shortDead: 1},
		"page of thirty of a large board":     {open: 300, listed: idRange(0, 30), dead: 270, shortDead: 270},
		"contrast: empty short listing":       {open: 20, listed: nil, short: true, dead: 20, massDead: 20},
	} {
		t.Run(name, func(t *testing.T) {
			l := openLedger(t)
			b := probeBoard("win")
			reviewRead(t, l, b, t0, idRange(0, tc.open))
			absent := tc.open - len(tc.listed)
			read := func(d int) Summary {
				if tc.short {
					return shortRead(t, l, b, day(d), tc.listed)
				}
				return windowRead(t, l, b, day(d), tc.listed)
			}
			var total Summary
			for d := 1; d <= 15; d++ {
				s := read(d)
				if !tc.short && (s.Mass || s.MassMarked != 0) {
					t.Fatalf("day %d: a windowed listing reported a mass absence: %+v", d, s)
				}
				if s.Open != tc.open-total.Dead || s.Absent != absent-total.Dead {
					t.Fatalf("day %d: open %d absent %d", d, s.Open, s.Absent)
				}
				switch {
				case d == 1 && (s.Missed != absent || s.Held != 0):
					t.Fatalf("day 1: %+v, want %d missed", s, absent)
				case d > 1 && d < 15 && (s.Held != absent || s.Dead != 0):
					t.Fatalf("day %d: died or not held inside the hold: %+v", d, s)
				}
				total.Dead += s.Dead
				total.ShortDead += s.ShortDead
				total.MassDead += s.MassDead
				total.UnreadableDead += s.UnreadableDead
			}
			if total.Dead != tc.dead || total.ShortDead != tc.shortDead || total.MassDead != tc.massDead || total.UnreadableDead != 0 {
				t.Fatalf("deaths %+v, want dead %d short %d mass %d", total, tc.dead, tc.shortDead, tc.massDead)
			}
			listed := map[int]bool{}
			for _, id := range tc.listed {
				listed[id] = true
			}
			for id := range tc.open {
				if listed[id] {
					continue
				}
				if r := rec(t, l, probeKey(t, b, id)); r.Status != posting.Dead || r.Annotations[DiedFromKey] != string(posting.Live) {
					t.Fatalf("held posting %d: %s %v", id, r.Status, r.Annotations)
				}
			}
			for _, id := range tc.listed {
				if r := rec(t, l, probeKey(t, b, id)); r.Status != posting.Live || r.MissedRuns != 0 {
					t.Fatalf("listed posting %d: %s, %d missed", id, r.Status, r.MissedRuns)
				}
			}
		})
	}
}

// The held posting carries the short cause from its first windowed miss, so
// its death is an expected closure whatever reads it later.
func TestWindowedMissRecordsTheShortCause(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("cause")
	reviewRead(t, l, b, t0, idRange(0, 4))
	windowRead(t, l, b, day(1), nil)
	r := rec(t, l, probeKey(t, b, 0))
	if r.Status != posting.Live || r.MissedRuns != 1 || r.Annotations[MassShortKey] != "short" || r.Annotations[MassMissedSinceKey] == "" {
		t.Fatalf("after an empty window: %s, %d missed, %v", r.Status, r.MissedRuns, r.Annotations)
	}
}

// A posting the window shows again before its hold ends is live again, its
// miss marks forgotten; a later absence starts a new episode with a full hold.
func TestWindowedPostingSeenAgainInsideTheHoldReturns(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("back")
	reviewRead(t, l, b, t0, idRange(0, 5))
	for d := 1; d <= 10; d++ {
		windowRead(t, l, b, day(d), idRange(1, 5))
	}
	s := windowRead(t, l, b, day(11), idRange(0, 5))
	r := rec(t, l, probeKey(t, b, 0))
	if s.Reappeared != 1 || r.Status != posting.Live || r.MissedRuns != 0 ||
		r.Annotations[MissedSinceKey] != "" || r.Annotations[MassMissedSinceKey] != "" || r.Annotations[MassShortKey] != "" {
		t.Fatalf("seen again: %+v; %s, %d missed, %v", s, r.Status, r.MissedRuns, r.Annotations)
	}
	// Absent again from day 12: the old episode's first miss (day 1) does not
	// count, so day 15 (the first episode's end) kills nothing.
	for d := 12; d <= 25; d++ {
		if s := windowRead(t, l, b, day(d), idRange(1, 5)); s.Dead != 0 {
			t.Fatalf("day %d: died before the new episode's hold ended: %+v", d, s)
		}
	}
	if s := windowRead(t, l, b, day(26), idRange(1, 5)); s.Dead != 1 || s.ShortDead != 1 {
		t.Fatalf("day 26: %+v", s)
	}
}

// A windowed board read later in other ways: the cause a window recorded and
// the hold it started carry through a full listing, a full listing that shows
// the posting ends the episode, a true mass absence upgrades the cause, and a
// window after an ordinary miss turns the ordinary hold into the long one.
func TestWindowThenOtherListings(t *testing.T) {
	t.Run("window then ordinary full listings", func(t *testing.T) {
		l := openLedger(t)
		b := probeBoard("wfull")
		reviewRead(t, l, b, t0, idRange(0, 20))
		windowRead(t, l, b, day(1), idRange(2, 20))
		var total Summary
		for d := 2; d <= 15; d++ {
			s := reviewRead(t, l, b, day(d), idRange(2, 20))
			if d < 15 && s.Dead != 0 {
				t.Fatalf("day %d: a full listing killed a window-held posting by the ordinary rule: %+v", d, s)
			}
			total.Dead += s.Dead
			total.ShortDead += s.ShortDead
			total.MassDead += s.MassDead
		}
		if total.Dead != 2 || total.ShortDead != 2 || total.MassDead != 0 {
			t.Fatalf("deaths %+v", total)
		}
	})
	t.Run("window then a full listing that shows it", func(t *testing.T) {
		l := openLedger(t)
		b := probeBoard("wshow")
		reviewRead(t, l, b, t0, idRange(0, 20))
		windowRead(t, l, b, day(1), idRange(2, 20))
		s := reviewRead(t, l, b, day(2), idRange(0, 20))
		if s.Reappeared != 2 || s.Absent != 0 {
			t.Fatalf("%+v", s)
		}
		if r := rec(t, l, probeKey(t, b, 0)); r.MissedRuns != 0 || r.Annotations[MassShortKey] != "" {
			t.Fatalf("%d missed, %v", r.MissedRuns, r.Annotations)
		}
	})
	t.Run("window then a true mass absence", func(t *testing.T) {
		l := openLedger(t)
		b := probeBoard("wmass")
		reviewRead(t, l, b, t0, idRange(0, 20))
		windowRead(t, l, b, day(1), idRange(2, 20))
		if s := reviewRead(t, l, b, day(2), nil); !s.Mass {
			t.Fatalf("setup: %+v", s)
		}
		var total Summary
		for d := 3; d <= 17; d++ {
			s := windowRead(t, l, b, day(d), nil) // a later window does not downgrade the cause
			total.Dead += s.Dead
			total.ShortDead += s.ShortDead
			total.MassDead += s.MassDead
		}
		// 0 and 1 (window-marked day 1) and 2..19 (true-mass-marked day 2): all
		// carry the true mass cause now.
		if total.Dead != 20 || total.MassDead != 20 || total.ShortDead != 0 {
			t.Fatalf("deaths %+v", total)
		}
	})
	t.Run("ordinary miss then window", func(t *testing.T) {
		l := openLedger(t)
		b := probeBoard("fwin")
		reviewRead(t, l, b, t0, idRange(0, 20))
		reviewRead(t, l, b, day(1), idRange(1, 20)) // 0 missed once, ordinarily
		// The ordinary rule would kill 0 here (two misses, 48h > OrdinaryHold).
		s := windowRead(t, l, b, day(2), idRange(1, 20))
		if s.Dead != 0 || s.Held != 1 {
			t.Fatalf("a window killed by the ordinary rule: %+v", s)
		}
		if r := rec(t, l, probeKey(t, b, 0)); r.Status != posting.Live || r.Annotations[MassShortKey] != "short" {
			t.Fatalf("%s %v", r.Status, r.Annotations)
		}
		// The hold runs from the episode's first miss (day 1).
		var total Summary
		for d := 3; d <= 15; d++ {
			s := windowRead(t, l, b, day(d), idRange(1, 20))
			total.Dead += s.Dead
			total.ShortDead += s.ShortDead
			if d < 15 && s.Dead != 0 {
				t.Fatalf("day %d: %+v", d, s)
			}
		}
		if total.Dead != 1 || total.ShortDead != 1 {
			t.Fatalf("deaths %+v", total)
		}
	})
	t.Run("unreadable rows then window keeps the failing cause", func(t *testing.T) {
		l := openLedger(t)
		b := probeBoard("uwin")
		reviewRead(t, l, b, t0, idRange(0, 20))
		if _, err := NewReconciler(l).ReconcileUnreadable(ctx, b, probePosts(t, b, idRange(1, 20)), day(1)); err != nil {
			t.Fatal(err)
		}
		var total Summary
		for d := 2; d <= 15; d++ {
			s := windowRead(t, l, b, day(d), idRange(1, 20))
			total.Dead += s.Dead
			total.ShortDead += s.ShortDead
			total.MassDead += s.MassDead
			total.UnreadableDead += s.UnreadableDead
		}
		if total.Dead != 1 || total.MassDead != 1 || total.UnreadableDead != 1 || total.ShortDead != 0 {
			t.Fatalf("deaths %+v", total)
		}
	})
}

// Adversarial inputs: an empty listing of a board with nothing stored,
// duplicate and foreign keys in the listing, a cancelled context, and a clock
// behind the ledger.
func TestReconcileWindowAdversarial(t *testing.T) {
	t.Run("empty listing, nothing stored", func(t *testing.T) {
		l := openLedger(t)
		s, err := NewReconciler(l).ReconcileWindow(ctx, probeBoard("none"), nil, t0)
		if err != nil || s != (Summary{}) {
			t.Fatalf("%+v %v", s, err)
		}
	})
	t.Run("duplicate keys in the listing", func(t *testing.T) {
		l := openLedger(t)
		b := probeBoard("dups")
		reviewRead(t, l, b, t0, idRange(0, 4))
		s := windowRead(t, l, b, day(1), []int{0, 0, 1, 1, 1})
		if s.Unchanged != 2 || s.Absent != 2 || s.Missed != 2 || s.Invalid != 0 {
			t.Fatalf("%+v", s)
		}
		s = windowRead(t, l, probeBoard("fresh"), day(1), []int{7, 7})
		if s.New != 1 {
			t.Fatalf("a repeated new key stored twice: %+v", s)
		}
	})
	t.Run("malformed and foreign keys are invalid and age nothing they name", func(t *testing.T) {
		l := openLedger(t)
		b := probeBoard("keys")
		reviewRead(t, l, b, t0, idRange(0, 2))
		foreign := probePost(t, probeBoard("elsewhere"), 0)
		listed := append(probePosts(t, b, []int{0}), foreign, posting.Posting{Title: "no key"})
		s, err := NewReconciler(l).ReconcileWindow(ctx, b, listed, day(1))
		if err != nil || s.Invalid != 2 || s.Absent != 1 || s.Missed != 1 {
			t.Fatalf("%+v %v", s, err)
		}
		if _, ok := l.Get(foreign.Key); ok {
			t.Fatal("a foreign key was stored under this board")
		}
	})
	t.Run("cancelled context", func(t *testing.T) {
		l := openLedger(t)
		b := probeBoard("cancel")
		reviewRead(t, l, b, t0, idRange(0, 3))
		c, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := NewReconciler(l).ReconcileWindow(c, b, probePosts(t, b, []int{0}), day(1)); err == nil {
			t.Fatal("a cancelled context reconciled")
		}
		for id := range 3 {
			if r := rec(t, l, probeKey(t, b, id)); r.MissedRuns != 0 || r.Status != posting.Live {
				t.Fatalf("%d: %s, %d missed after a cancelled read", id, r.Status, r.MissedRuns)
			}
		}
	})
	t.Run("clock behind the ledger ages nothing", func(t *testing.T) {
		l := openLedger(t)
		b := probeBoard("behind")
		reviewRead(t, l, b, day(2), idRange(0, 3)) // events two days ahead of the next read
		s := windowRead(t, l, b, day(1), nil)
		if !s.ClockBehind || s.Missed != 0 || s.Dead != 0 || s.Mass {
			t.Fatalf("%+v", s)
		}
		if r := rec(t, l, probeKey(t, b, 0)); r.MissedRuns != 0 {
			t.Fatalf("aged under a clock behind: %d missed", r.MissedRuns)
		}
	})
}
