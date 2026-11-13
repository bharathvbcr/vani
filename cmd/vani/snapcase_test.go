package main

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// Snap's case: a Workday board with 42 open postings answers one run with an
// empty listing (the first page had no rows, and its total was 0 or believable
// but unlisted), the next run times out (a failed board is not reconciled at
// all), and the run after lists 41 of the 42 again. None of the 42 may die at
// the empty read (42 >= mass_min_open 10), and the one that never returns is
// mass-held, not killed by the ordinary two-miss rule: it dies only at the end
// of the 14-day mass hold counted from its own first miss.
//
// The empty read is replayed both ways it can arrive: a complete listing that
// found no rows (total 0), which is a mass absence by the liveness rule, and a
// listing short by the whole total (Stats.Short = 42), which reconcileBoard
// treats as one.
func TestSnapCaseEmptyReadThenTimeoutThenMostReturn(t *testing.T) {
	for name, short := range map[string]int{"total 0": 0, "short by the whole total": 42} {
		t.Run(name, func(t *testing.T) {
			l := newTestLedger(t)
			b := sources.Board{ATS: posting.Workday, Token: "snap/External", Company: "Snap"}
			t0 := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
			var all []posting.Posting
			for i := 1; i <= 42; i++ {
				p := posting.Posting{Key: posting.Key(fmt.Sprintf("%sR-%d", b.KeyPrefix(), i)), ATS: posting.Workday, Company: "Snap",
					Title: fmt.Sprintf("Engineer %d", i), Description: fmt.Sprintf("Body %d.", i)}
				if err := l.Append(context.Background(), ledger.Event{V: ledger.SchemaVersion, At: t0, Key: p.Key, Status: posting.Live, Posting: &p}); err != nil {
					t.Fatal(err)
				}
				all = append(all, p)
			}
			w := &wired{cfg: config.Default(), stateDir: t.TempDir(), led: l, warn: &bytes.Buffer{}, live: &liveNote{}}
			never := all[41].Key
			returned := all[:41]

			first := t0.Add(24 * time.Hour)
			s := runListing(t, w, b, nil, short, first)
			if !s.Mass || s.MassMarked != 42 || s.Dead != 0 {
				t.Fatalf("the empty read: %+v", s)
			}
			// The next run's read of the board timed out: the board failed, so
			// nothing was reconciled and nothing aged.
			// Then 41 return, daily, while the one stays away.
			for day := 3; day <= 14; day++ {
				at := t0.Add(time.Duration(day) * 24 * time.Hour)
				s := runListing(t, w, b, returned, 0, at)
				if s.Dead != 0 {
					t.Fatalf("day %d: %d died: %+v", day, s.Dead, s)
				}
				for _, p := range returned {
					if r, _ := w.led.Get(p.Key); r.Status != posting.Live || r.MissedRuns != 0 {
						t.Fatalf("day %d: %s is %s with %d missed runs after returning", day, p.Key, r.Status, r.MissedRuns)
					}
				}
				if st, missed := ledgerStatusOf(t, w, never); st != posting.Live || missed < 2 {
					t.Fatalf("day %d: the one that never returns is %s with %d missed runs, want live, held past its second miss", day, st, missed)
				}
				if massSinceOf(t, w, never) == "" {
					t.Fatalf("day %d: the one that never returns lost its mass mark", day)
				}
			}
			// Held until 14 days after its own first miss (day 1), not killed
			// on the second miss.
			end := first.Add(pipeline.DefaultMassHold)
			s = runListing(t, w, b, returned, 0, end.Add(-time.Hour))
			if st, _ := ledgerStatusOf(t, w, never); st != posting.Live || s.Dead != 0 {
				t.Fatalf("an hour before the mass hold ends: %s, %+v", st, s)
			}
			s = runListing(t, w, b, returned, 0, end)
			if st, _ := ledgerStatusOf(t, w, never); st != posting.Dead || s.Dead != 1 || s.MassDead != 1 {
				t.Fatalf("at the end of the mass hold: %s, %+v", st, s)
			}
		})
	}
}
