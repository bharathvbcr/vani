package pipeline

// Review findings on the death rule's clock handling (2026-11-12), each
// reproduced against the rule before the fix:
//
//   - F1: two runs under a clock a year ahead, 7 hours apart, froze aging for a
//     year once the clock was right, and every run said to fix the clock.
//   - F2: one stray future event on any board froze every board.
//   - F3: a clock that jumped 15 days ahead between two reads an hour apart
//     killed every mass-held posting.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// capturing keeps every event appended through it.
type capturing struct {
	ledger.Ledger
	evs []ledger.Event
}

func (c *capturing) Append(ctx context.Context, e ledger.Event) error {
	c.evs = append(c.evs, e)
	return c.Ledger.Append(ctx, e)
}

func (c *capturing) AppendBatch(ctx context.Context, evs []ledger.Event) error {
	c.evs = append(c.evs, evs...)
	return c.Ledger.AppendBatch(ctx, evs)
}

// reviewRead reconciles ids of b as a full listing at the clock reading at.
func reviewRead(t *testing.T, l ledger.Ledger, b sources.Board, at time.Time, ids []int) Summary {
	t.Helper()
	var listed []posting.Posting
	for _, id := range ids {
		listed = append(listed, probePost(t, b, id))
	}
	rc := NewReconciler(l)
	s, err := rc.Reconcile(ctx, b, listed, at)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// F1: a clock a year ahead for two runs 7 hours apart, then right. The far
// future events are not read as a clock behind: aging goes on, and the first
// miss is stamped at the corrected read, never earlier. A year later, when
// real time comes within ClockWindow (3 days) of the bad runs' events that no
// later read rewrote, the board freezes again until real time passes them, and
// says what it saw.
func TestClockAheadForTwoRunsDoesNotFreezeAYear(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("ahead")
	reviewRead(t, l, b, t0, idRange(0, 20))
	year := 365 * 24 * time.Hour
	// The bad runs list a new posting each, so their events are in the ledger.
	reviewRead(t, l, b, t0.Add(24*time.Hour+year), append(idRange(0, 20), 100))
	reviewRead(t, l, b, t0.Add(31*time.Hour+year), append(idRange(0, 20), 100, 101))

	// The clock is right again; posting 0 is no longer listed.
	at := t0.Add(48 * time.Hour)
	live := append(idRange(1, 20), 100, 101)
	s := reviewRead(t, l, b, at, live)
	if s.ClockBehind {
		t.Fatalf("a year-ahead burst over two runs froze aging under a right clock: %+v", s)
	}
	r := rec(t, l, probeKey(t, b, 0))
	if r.MissedRuns != 1 || r.Annotations[MissedSinceKey] != stampOf(at) {
		t.Fatalf("the first miss is not the corrected read's: %+v", r)
	}
	reviewRead(t, l, b, at.Add(19*time.Hour), live)
	if rec(t, l, probeKey(t, b, 0)).Status == posting.Dead {
		t.Fatal("died 19 hours into its absence")
	}
	if s := reviewRead(t, l, b, at.Add(20*time.Hour), live); s.Dead != 1 || s.ClockBehind {
		t.Fatalf("not dead 20 hours into its absence: %+v", s)
	}

	// A year on: daily reads across the bad runs' time, each leaving out
	// another posting (the one before is back, so none dies). Only reads within
	// ClockWindow of the bad events (the two new postings' "new", which no
	// later read rewrote) freeze, and they name the newest such event.
	// The freeze therefore spans at most ClockWindow plus the 7 hours between
	// the bad runs.
	bad := []time.Time{t0.Add(24*time.Hour + year), t0.Add(31*time.Hour + year)}
	var frozen []time.Time
	for d := -6; d <= 6; d++ {
		now := t0.Add(year + 28*time.Hour + time.Duration(d)*24*time.Hour)
		gone := 1 + d + 6
		live := append(append(idRange(1, gone), idRange(gone+1, 20)...), 100, 101)
		s := reviewRead(t, l, b, now, live)
		var want time.Time // the newest bad event within the window
		for _, e := range bad {
			if e.After(now.Add(time.Hour)) && !e.After(now.Add(3*24*time.Hour)) {
				want = e
			}
		}
		if s.ClockBehind != !want.IsZero() || !s.ClockAhead.Equal(want) {
			t.Fatalf("day %d: ClockBehind %v naming %v; want a freeze naming %v", d, s.ClockBehind, s.ClockAhead, want)
		}
		if s.ClockBehind {
			frozen = append(frozen, now)
		}
	}
	if len(frozen) == 0 || frozen[len(frozen)-1].Sub(frozen[0]) > 3*24*time.Hour+7*time.Hour {
		t.Fatalf("frozen reads %v: want some, spanning at most 3 days and 7 hours", frozen)
	}
}

// F2: one stray future event freezes only its own board; others age. A burst
// of future events on several records still freezes every board, at most
// ClockWindow before real time passes it.
func TestStrayFutureEventFreezesOnlyItsBoard(t *testing.T) {
	l := openLedger(t)
	a, b := probeBoard("alpha"), probeBoard("beta")
	reviewRead(t, l, a, t0, idRange(0, 20))
	reviewRead(t, l, b, t0, idRange(0, 20))
	stray := probePost(t, b, 500)
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(72 * time.Hour), Key: stray.Key, Status: posting.Live, Posting: &stray}); err != nil {
		t.Fatal(err)
	}
	at := t0.Add(24 * time.Hour)
	if s := reviewRead(t, l, a, at, idRange(1, 20)); s.ClockBehind || s.Missed != 1 {
		t.Fatalf("a stray event on another board froze this one: %+v", s)
	}
	s := reviewRead(t, l, b, at, append(idRange(1, 20), 500))
	if !s.ClockBehind || s.Missed != 0 || !s.ClockAhead.Equal(t0.Add(72*time.Hour)) {
		t.Fatalf("the board's own event 48h ahead did not freeze it, or was not named: %+v", s)
	}
}

// F2, the burst: three records 10 days ahead on board beta. While they are
// more than ClockWindow ahead nothing freezes; once within it, every board
// freezes (a quorum of records, not one stray), and only until real time
// passes them.
func TestFutureBurstFreezesBoundedly(t *testing.T) {
	l := openLedger(t)
	a, b := probeBoard("alpha"), probeBoard("beta")
	reviewRead(t, l, a, t0, idRange(0, 20))
	reviewRead(t, l, b, t0, idRange(0, 20))
	burst := t0.Add(10 * 24 * time.Hour)
	for i := range ClockQuorum {
		p := probePost(t, b, 500+i)
		if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: burst.Add(time.Duration(i) * time.Minute), Key: p.Key, Status: posting.Live, Posting: &p}); err != nil {
			t.Fatal(err)
		}
	}
	var frozen []int
	for d := 1; d <= 12; d++ {
		if s := reviewRead(t, l, a, t0.Add(time.Duration(d)*24*time.Hour), idRange(0, 20)); s.ClockBehind {
			t.Fatalf("day %d: nothing absent, yet the pass reported a freeze: %+v", d, s)
		}
		// A listing that leaves posting 0 absent tells whether the board froze.
		s := reviewRead(t, l, a, t0.Add(time.Duration(d)*24*time.Hour+time.Hour), idRange(1, 20))
		if s.ClockBehind {
			frozen = append(frozen, d)
		}
		reviewRead(t, l, a, t0.Add(time.Duration(d)*24*time.Hour+2*time.Hour), idRange(0, 20)) // back: no death in the way
	}
	if len(frozen) == 0 || len(frozen) > int(ClockWindow/(24*time.Hour)) || frozen[0] < 7 {
		t.Fatalf("days frozen %v: want a run of at most %d days, none before day 7", frozen, int(ClockWindow/(24*time.Hour)))
	}
}

// F3: an empty listing marks every posting mass at hour 1; an hour later the
// clock reads 15 days ahead and the board is still empty. The jump is the only
// thing that makes the hold look over, so no posting dies on it: the death
// waits a read, and the run says why. Once the clock is right again the hold
// runs from the real first miss.
func TestClockJumpAheadDoesNotEndAMassHold(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("jump")
	reviewRead(t, l, b, t0, idRange(0, 20))
	first := t0.Add(time.Hour)
	if s := reviewRead(t, l, b, first, nil); !s.Mass || s.MassMarked != 20 {
		t.Fatalf("setup: the empty listing was not a mass absence: %+v", s)
	}
	cl := &capturing{Ledger: l}
	s := reviewRead(t, cl, b, t0.Add(2*time.Hour+15*24*time.Hour), nil)
	if s.Dead != 0 || s.Held != 20 || s.Deferred != 20 {
		t.Fatalf("a 15-day clock jump ended the mass hold: %+v", s)
	}
	if len(cl.evs) != 20 || !strings.Contains(cl.evs[0].Reason, "held one more read: 361h0m0s since its last event") {
		t.Fatalf("the held postings do not say why they waited: %d events, %+v", len(cl.evs), cl.evs)
	}
	// The clock is right again: the hold runs from hour 1.
	for h := 26; h < 24*14; h += 24 {
		if s := reviewRead(t, l, b, t0.Add(time.Duration(h)*time.Hour), nil); s.Dead != 0 {
			t.Fatalf("hour %d: died inside the mass hold: %+v", h, s)
		}
	}
	if s := reviewRead(t, l, b, first.Add(14*24*time.Hour), nil); s.Dead != 20 || s.MassDead != 20 {
		t.Fatalf("not dead at the end of the mass hold: %+v", s)
	}
}

// F3's residual, pinned so the documentation stays true: a clock that jumps
// less than ClockWindow between two reads is indistinguishable from reads that
// far apart. A 20-hour jump between reads an hour apart ends an ordinary hold.
func TestShortClockJumpIsUndetectable(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("short")
	reviewRead(t, l, b, t0, idRange(0, 20))
	reviewRead(t, l, b, t0.Add(time.Hour), idRange(1, 20))
	if s := reviewRead(t, l, b, t0.Add(2*time.Hour+20*time.Hour), idRange(1, 20)); s.Dead != 1 {
		t.Fatalf("the documented residual changed (update docs/RUNTIME.md and liveness.go): %+v", s)
	}
}

// A stamp taken under a clock set back past the posting's own history (before
// it was first seen) is unreadable: the episode restarts at the next read,
// later, never earlier. Without the rule, the five-year-old stamp would kill at
// the corrected read.
func TestStampBeforeFirstSeenRestarts(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("setback")
	reviewRead(t, l, b, t0, idRange(0, 20))
	back := -5 * 365 * 24 * time.Hour
	reviewRead(t, l, b, t0.Add(24*time.Hour+back), idRange(1, 20)) // nothing in the ledger within 3 days ahead: it ages
	if r := rec(t, l, probeKey(t, b, 0)); r.MissedRuns != 1 || r.Annotations[MissedSinceKey] != stampOf(t0.Add(24*time.Hour+back)) {
		t.Fatalf("setup: the set-back read did not stamp its miss: %+v", r)
	}
	at := t0.Add(25 * time.Hour)
	if s := reviewRead(t, l, b, at, idRange(1, 20)); s.Dead != 0 {
		t.Fatalf("a stamp before the posting existed killed it: %+v", s)
	}
	if r := rec(t, l, probeKey(t, b, 0)); r.Annotations[MissedSinceKey] != stampOf(at) {
		t.Fatalf("the episode did not restart at the corrected read: %+v", r)
	}
}

// MassHeld counts, per board, the open postings in a mass hold and when the
// holds end; an unreadable stamp counts from now.
func TestMassHeldPerBoard(t *testing.T) {
	l := openLedger(t)
	a, b := probeBoard("alpha"), probeBoard("beta")
	reviewRead(t, l, a, t0, idRange(0, 20))
	reviewRead(t, l, b, t0, idRange(0, 20))
	reviewRead(t, l, a, t0.Add(time.Hour), nil)              // alpha: all 20 mass marked at hour 1
	reviewRead(t, l, b, t0.Add(2*time.Hour), idRange(1, 20)) // beta: one ordinary miss, not mass
	now := t0.Add(3 * time.Hour)
	got := MassHeld(l.All(), Liveness{}, now)
	want := t0.Add(time.Hour + specMassHold)
	if len(got) != 1 || got[0].Prefix != a.KeyPrefix() || got[0].Held != 20 || !got[0].Until.Equal(want) || !got[0].Last.Equal(want) {
		t.Fatalf("%+v", got)
	}
	if got := MassHeld(l.All(), Liveness{MassHold: 48 * time.Hour}, now); !got[0].Until.Equal(t0.Add(49 * time.Hour)) {
		t.Fatalf("a configured hold: %+v", got)
	}
	// Read under a clock five years back, every stamp is ahead: counted from now.
	early := now.Add(-5 * 365 * 24 * time.Hour)
	if got := MassHeld(l.All(), Liveness{}, early); !got[0].Until.Equal(early.Add(specMassHold)) {
		t.Fatalf("an unreadable stamp: %+v", got)
	}
}
