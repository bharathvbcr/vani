package pipeline

// Leftovers of the liveness review (docs/FOLLOWUPS.md A2-4 and the clock
// items), each shown failing against 1ad3d2e before its fix.

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// A2-4: a board under the floor (10 open) whose non-empty listing is missing
// more than half of its open postings, and at least 2, is a mass absence: it
// holds them 14 days instead of killing them on the second miss. Half or less,
// or one posting, stays an ordinary absence.
func TestSmallBoardMassAbsenceIsMoreThanHalf(t *testing.T) {
	cases := []struct {
		name         string
		open, listed int
		mass         bool
	}{
		{"6 open, 2 listed: 4 absent", 6, 2, true},
		{"6 open, 3 listed: exactly half", 6, 3, false},
		{"3 open, 1 listed: 2 absent", 3, 1, true},
		{"3 open, 2 listed: 1 absent", 3, 2, false},
		{"2 open, 1 listed: 1 absent (under 2)", 2, 1, false},
		{"9 open, 4 listed: 5 absent", 9, 4, true},
		{"9 open, 5 listed: 4 absent", 9, 5, false},
		{"10 open, 8 listed: 2 absent is the 25% rule's, not half", 10, 8, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := openLedger(t)
			b := probeBoard("small")
			reviewRead(t, l, b, t0, idRange(0, tc.open))
			listed := idRange(0, tc.listed)
			s1 := reviewRead(t, l, b, t0.Add(24*time.Hour), listed)
			if s1.Mass != tc.mass {
				t.Fatalf("Mass = %v, want %v (%+v)", s1.Mass, tc.mass, s1)
			}
			s2 := reviewRead(t, l, b, t0.Add(48*time.Hour), listed)
			absent := tc.open - tc.listed
			if tc.mass {
				if s2.Dead != 0 || s2.Held != absent {
					t.Fatalf("second miss killed or lost a mass-held posting: %+v", s2)
				}
				if r := rec(t, l, probeKey(t, b, tc.open-1)); r.Status == posting.Dead || r.Annotations[MassMissedSinceKey] == "" {
					t.Fatalf("not mass marked: %+v", r)
				}
			} else if s2.Dead != absent {
				t.Fatalf("an ordinary absence did not die on its second miss: %+v", s2)
			}
		})
	}
}
