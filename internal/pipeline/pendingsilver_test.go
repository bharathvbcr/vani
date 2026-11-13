package pipeline

import (
	"fmt"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// silverPending marks the silver drop of silverDropped as a pending silver
// reopen, as `vani retriage --silver-drops --rescue` does on a watched board.
func silverPending(t *testing.T) (*Runner, *ledger.File, posting.Key, *PendingReopens, time.Time) {
	t.Helper()
	r, l, k := silverDropped(t)
	s := NewPendingReopens()
	at := t0.Add(time.Hour)
	out, err := RescueSilverDrops(ctx, l, at, map[posting.Key]SilverPick{k: {Score: 0.9, Games: 6}}, silverRule, never, deferScope(s), false)
	if err != nil || len(out.Deferred) != 1 || s.Items[k].Class != reopenClassSilver {
		t.Fatalf("mark: %+v, %v, %+v", out, err, s.Items[k])
	}
	return r, l, k, s, at
}

// A silver entry ages like the others: only a clean full listing counts toward
// its 14 days, a short or skipped-row listing counts nothing, and when it does
// expire the posting stays dropped and nothing is remembered in gone (that
// memory is for exclusion drops: a sighting of a gone key is judged as an
// exclusion drop, which a silver drop is not).
func TestSilverPendingExpiresOnlyOnCleanFullListings(t *testing.T) {
	for name, kind := range map[string]shortKind{"short": shortCounted, "unreadable": shortUnreadable} {
		t.Run(name, func(t *testing.T) {
			r, l, k, s, at := silverPending(t)
			other := ghPost(t, "9", "other jd")
			day := func(d int) time.Time { return at.Add(time.Duration(d) * 24 * time.Hour) }
			for d := 1; d <= 40; d++ {
				if _, so := sightingKind(t, l, s, r.Policy, day(d), kind, other); so.Expired != 0 || s.Len() != 1 {
					t.Fatalf("day %d: a %s listing expired the silver entry: %+v", d, name, so)
				}
			}
			for d := 41; d <= 53; d++ {
				if _, so := sightingKind(t, l, s, r.Policy, day(d), shortNone, other); so.Expired != 0 || s.Len() != 1 {
					t.Fatalf("day %d: expired with under 14 days of clean read time: %+v", d, so)
				}
			}
			expired := 0
			for d := 54; d <= 60 && s.Len() > 0; d++ {
				_, so := sightingKind(t, l, s, r.Policy, day(d), shortNone, other)
				expired += so.Expired
			}
			if expired != 1 || s.Len() != 0 || s.GoneLen() != 0 || rec(t, l, k).Status != posting.Dropped {
				t.Fatalf("clean listings: %d expired, %d pending, %d gone, status %s", expired, s.Len(), s.GoneLen(), rec(t, l, k).Status)
			}
		})
	}
}

// A board that is short on every run, or skips rows, still lets a silver drop
// go to unsure when its listing shows the posting (what the rescue is for).
func TestSilverPendingStillAppliesWhenAShortOrSkippedRowListingShowsIt(t *testing.T) {
	for name, kind := range map[string]shortKind{"short": shortCounted, "unreadable": shortUnreadable} {
		t.Run(name, func(t *testing.T) {
			r, l, k, s, at := silverPending(t)
			other := ghPost(t, "9", "other jd")
			for d := 1; d <= 5; d++ {
				sightingKind(t, l, s, r.Policy, at.Add(time.Duration(d)*24*time.Hour), kind, other)
			}
			if s.Len() != 1 {
				t.Fatalf("setup: %d pending", s.Len())
			}
			_, so := sightingKind(t, l, s, r.Policy, at.Add(6*24*time.Hour), kind, other, listedAgain(t, l, k))
			if got := rec(t, l, k); so.Applied != 1 || got.Status != posting.Unsure || s.Len() != 0 {
				t.Fatalf("a %s listing that shows the silver drop: %+v, status %s, %d pending", name, so, got.Status, s.Len())
			}
		})
	}
}

// Silver counts toward the 40,000 total only: a set full of exclusion marks at
// their sub-cap still takes a silver one (it is not starved by them), a full
// set refuses it loudly (counted, never silent), and silver marks, at most 50 a
// day, are not counted against the exclusion sub-cap (they cannot starve it).
func TestSilverPendingCountsTowardTheTotalOnly(t *testing.T) {
	r, l, k := silverDropped(t)
	_ = r
	s := NewPendingReopens()
	for i := 0; i < MaxPendingExclusions; i++ {
		s.Items[posting.Key(fmt.Sprintf("gh:fill:%d", i))] = PendingReopen{Class: reopenClassExclusion, At: t0}
	}
	at := t0.Add(time.Hour)
	picks := map[posting.Key]SilverPick{k: {Score: 0.9, Games: 6}}
	out, err := RescueSilverDrops(ctx, l, at, picks, silverRule, never, deferScope(s), false)
	if err != nil || len(out.Deferred) != 1 {
		t.Fatalf("a set at the exclusion sub-cap refused a silver mark: %+v, %v", out, err)
	}
	if s.count(reopenClassExclusion) != MaxPendingExclusions || s.count(reopenClassSilver) != 1 {
		t.Fatalf("silver was counted as an exclusion: %d exclusion, %d silver", s.count(reopenClassExclusion), s.count(reopenClassSilver))
	}

	// A set at the total bound refuses it, and says so.
	full := NewPendingReopens()
	for i := 0; i < MaxPendingReopens; i++ {
		full.Items[posting.Key(fmt.Sprintf("gh:fill:%d", i))] = PendingReopen{Class: reopenClassTitle, At: t0}
	}
	out, err = RescueSilverDrops(ctx, l, at, picks, silverRule, never, deferScope(full), false)
	if err != nil || len(out.Deferred) != 0 || out.Left[silverLeftOverBound] != 1 || full.Len() != MaxPendingReopens {
		t.Fatalf("a full set: %+v, %v, %d pending", out, err, full.Len())
	}
}
