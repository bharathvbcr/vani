package pipeline

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A clock set back by more than ClockWindow, after a run that wrote a quorum of
// events on the board: those events are now days ahead of the clock. Aging
// goes on under the set-back clock (nothing within ClockWindow ahead), so first
// misses are stamped early and, once the clock is right, a mass hold ends early
// by the set-back. The board's own quorum of events up to SetBackCap ahead now
// freezes it like the ones within ClockWindow, and says so (ClockBehind).
func TestClockSetBackOverTheWindowFreezesTheBoardWithAQuorum(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("setback10")
	reviewRead(t, l, b, t0, idRange(0, 20))
	d0 := t0.Add(20 * 24 * time.Hour)
	if s := reviewRead(t, l, b, d0, nil); !s.Mass || s.MassMarked != 20 {
		t.Fatalf("setup: %+v", s)
	}
	// The clock is set back 10 days for one read.
	back := d0.Add(24 * time.Hour).Add(-10 * 24 * time.Hour)
	s := reviewRead(t, l, b, back, nil)
	if !s.ClockBehind || s.Missed+s.Held+s.Dead != 0 || !s.ClockAhead.Equal(d0) {
		t.Fatalf("the set-back read aged the board or did not name the events ahead: %+v", s)
	}
	if r := rec(t, l, probeKey(t, b, 0)); r.MissedRuns != 1 || r.Annotations[MissedSinceKey] != stampOf(d0) {
		t.Fatalf("the set-back read changed the first miss: %+v", r)
	}
	// The clock is right again: the hold runs from the real first miss.
	for d := 2; d <= 13; d++ {
		if s := reviewRead(t, l, b, d0.Add(time.Duration(d)*24*time.Hour), nil); s.Dead != 0 || s.ClockBehind {
			t.Fatalf("day %d after the mass absence: died inside the 14-day hold, or still frozen: %+v", d, s)
		}
	}
	if s := reviewRead(t, l, b, d0.Add(14*24*time.Hour), nil); s.Dead != 20 {
		t.Fatalf("not dead when the real hold ended: %+v", s)
	}
}

// The guard is bounded and needs a quorum on the board's own records: two
// stray events, three on another board, and events beyond SetBackCap (a clock
// that ran ahead) freeze nothing.
func TestClockSetBackGuardNeedsAnOwnQuorumAndIsBounded(t *testing.T) {
	now := t0.Add(24 * time.Hour)
	cases := []struct {
		name   string
		own    bool
		n      int
		ahead  time.Duration
		frozen bool
	}{
		{"one stray own event 10 days ahead", true, 1, 10 * 24 * time.Hour, false},
		{"two own events 10 days ahead", true, 2, 10 * 24 * time.Hour, false},
		{"three own events 10 days ahead", true, 3, 10 * 24 * time.Hour, true},
		{"three own events at the cap", true, 3, SetBackCap, true},
		{"three own events past the cap", true, 3, SetBackCap + 10*time.Second, false},
		{"three events 10 days ahead on another board", false, 3, 10 * 24 * time.Hour, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := openLedger(t)
			b := probeBoard("guard")
			ps := seedBoard(t, l, b, 20)
			other := probeBoard("elsewhere")
			if tc.own {
				other = b
			}
			keep := append([]posting.Posting(nil), ps[1:]...) // only posting 0 is absent
			for i := range tc.n {
				p := probePost(t, other, 700+i)
				if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: now.Add(tc.ahead).Add(-time.Duration(i) * time.Second),
					Key: p.Key, Status: posting.Live, Posting: &p}); err != nil {
					t.Fatal(err)
				}
				if tc.own {
					keep = append(keep, p)
				}
			}
			s, err := Reconcile(ctx, l, b, keep, now)
			if err != nil {
				t.Fatal(err)
			}
			if s.ClockBehind != tc.frozen {
				t.Fatalf("ClockBehind = %v, want %v: %+v", s.ClockBehind, tc.frozen, s)
			}
			if r := rec(t, l, ps[0].Key); (r.MissedRuns == 0) != tc.frozen {
				t.Fatalf("missed runs %d with frozen = %v", r.MissedRuns, tc.frozen)
			}
		})
	}
}
