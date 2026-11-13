package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

var starveAt = time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

func pinStarveClock(t *testing.T) {
	t.Helper()
	old := now
	now = func() time.Time { return starveAt }
	t.Cleanup(func() { now = old })
}

// freshRec is a live posting listed after its board's first crawl, posted an
// hour ago: it has a known age and the top of the freshness order.
func freshRec(i int) ledger.Record {
	k := posting.Key(fmt.Sprintf("gh:acme:fresh%03d", i))
	// The ones with a lower i are a minute fresher, so the order among them is fixed.
	posted := starveAt.Add(-time.Hour - time.Duration(i)*time.Minute)
	return ledger.Record{Key: k, Status: posting.Live, FirstSeen: posted.Add(time.Minute),
		Posting: posting.Posting{Key: k, Company: "Acme", Title: "Engineer", PostedAt: posted}}
}

// starvedRec is a live posting with no usable posted date, listed in its
// board's first crawl days ago: it has no known age, so it sorts after every
// posting that has one.
func starvedRec(i int, waited time.Duration) ledger.Record {
	k := posting.Key(fmt.Sprintf("gh:acme:old%03d", i))
	return ledger.Record{Key: k, Status: posting.Live, FirstSeen: starveAt.Add(-waited),
		Posting: posting.Posting{Key: k, Company: "Acme", Title: "Engineer"}}
}

func starveKeys(recs []ledger.Record) []posting.Key {
	out := make([]posting.Key, len(recs))
	for i, r := range recs {
		out[i] = r.Key
	}
	return out
}

// The production plan of a run and of an inbox poll lets postings that have
// waited past the cap through: with a cap of 8 and ten fresh postings ahead of
// four old undated ones, the two oldest (a quarter of the slots) advance in
// the run instead of waiting forever.
func TestAdvancePlanLetsStarvedPostingsThrough(t *testing.T) {
	pinStarveClock(t)
	for _, kind := range []advanceKind{kindRun, kindInbox} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			var recs []ledger.Record
			for i := 0; i < 10; i++ {
				recs = append(recs, freshRec(i))
			}
			for i, d := range []time.Duration{4 * 24 * time.Hour, 9 * 24 * time.Hour, 6 * 24 * time.Hour, 5 * 24 * time.Hour} {
				recs = append(recs, starvedRec(i, d))
			}
			cfg := config.Default()
			cfg.AdvancePerRun = 8
			w := newWired(cfg, t.TempDir(), io.Discard)
			plan := w.advancePlan(kind, pipeline.DropPolicy{})
			plan.Workers = 1
			var mu sync.Mutex
			var advanced []posting.Key
			_, err := pipeline.Advance(context.Background(), plan, pipeline.AdvanceDeps{Records: recs, Advance: func(_ context.Context, k posting.Key) error {
				mu.Lock()
				advanced = append(advanced, k)
				mu.Unlock()
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			want := []posting.Key{"gh:acme:old001", "gh:acme:old002"} // the nine-day and the six-day wait
			if len(advanced) != 8 || !slices.Equal(advanced[:2], want) {
				t.Fatalf("advanced %v, want the two oldest waiters first of 8", advanced)
			}
			for i, k := range advanced[2:] {
				if k != posting.Key(fmt.Sprintf("gh:acme:fresh%03d", i)) {
					t.Errorf("slot %d went to %s: the fresh top postings fill the rest, freshest first", i+3, k)
				}
			}
		})
	}
}

func TestOrderAdvanceStarvationIsBoundedAndOldestFirst(t *testing.T) {
	pinStarveClock(t)
	var todo []ledger.Record
	for i := 0; i < 30; i++ {
		todo = append(todo, freshRec(i))
	}
	for i := 0; i < 10; i++ { // waits of 4 to 13 days, in a scrambled order
		todo = append(todo, starvedRec(i, time.Duration(4+(i*7)%10)*24*time.Hour))
	}
	all := slices.Clone(todo)
	before := starveKeys(todo)
	orderAdvance(todo, all, nil, withSlots(8)) // a quarter: 2

	got := starveKeys(todo)
	var old []posting.Key
	for _, k := range got[:8] {
		if k[len("gh:acme:"):][:3] == "old" {
			old = append(old, k)
		}
	}
	if len(old) != 2 {
		t.Fatalf("%d starved postings in the first 8, want exactly a quarter (2): %v", len(old), got[:8])
	}
	// Oldest first: waits are 4+(i*7)%10 days: i=7 (4+9=13) then i=4 (4+8=12).
	if got[0] != "gh:acme:old007" || got[1] != "gh:acme:old004" {
		t.Errorf("first two = %v, want the 13-day and 12-day waiters", got[:2])
	}
	// The fresh top fills the rest in their own order, and nothing is lost.
	for i, k := range got[2:8] {
		if k != posting.Key(fmt.Sprintf("gh:acme:fresh%03d", i)) {
			t.Errorf("slot %d = %s", i+3, k)
		}
	}
	sortedBefore, sortedAfter := slices.Clone(before), slices.Clone(got)
	slices.Sort(sortedBefore)
	slices.Sort(sortedAfter)
	if !slices.Equal(sortedBefore, sortedAfter) {
		t.Fatalf("the order lost or added postings:\nbefore %v\nafter  %v", sortedBefore, sortedAfter)
	}
	// Without the slots the order is the old one: every fresh posting first.
	plain := slices.Clone(todo)
	slices.SortFunc(plain, func(a, b ledger.Record) int { return slices.Index(before, a.Key) - slices.Index(before, b.Key) })
	orderAdvance(plain, all, nil)
	if k := plain[0].Key; k != "gh:acme:fresh000" || starveKeys(plain)[29] != "gh:acme:fresh029" {
		t.Errorf("the plain order changed: %v", starveKeys(plain)[:3])
	}
}

// A posting has waited once it has been in the ledger three days: not at 2 days
// 23 hours, yes at exactly 72 hours.
func TestOrderAdvanceStarvesOnlyAfterThreeDays(t *testing.T) {
	pinStarveClock(t)
	for _, tc := range []struct {
		wait    time.Duration
		promote bool
	}{
		{3*24*time.Hour - time.Second, false},
		{3 * 24 * time.Hour, true},
		{40 * 24 * time.Hour, true},
		{0, false},
		{-5 * 24 * time.Hour, false}, // first seen "in the future": a clock fault, not a wait
	} {
		var todo []ledger.Record
		for i := 0; i < 8; i++ {
			todo = append(todo, freshRec(i))
		}
		old := starvedRec(0, tc.wait)
		old.Key, old.Posting.Key = "lv:solo:old000", "lv:solo:old000" // its own board: its first crawl, so no known age
		todo = append(todo, old)
		orderAdvance(todo, slices.Clone(todo), nil, withSlots(8))
		in := slices.Contains(starveKeys(todo)[:8], old.Key)
		if in != tc.promote {
			t.Errorf("waited %v: advanced %v, want %v (%v)", tc.wait, in, tc.promote, starveKeys(todo))
		}
	}
	// An unset first-seen time is not a wait either.
	todo := []ledger.Record{}
	for i := 0; i < 8; i++ {
		todo = append(todo, freshRec(i))
	}
	zero := starvedRec(1, 0)
	zero.Key, zero.Posting.Key = "lv:solo:old001", "lv:solo:old001"
	zero.FirstSeen = time.Time{}
	todo = append(todo, zero)
	orderAdvance(todo, slices.Clone(todo), nil, withSlots(8))
	if slices.Contains(starveKeys(todo)[:8], zero.Key) {
		t.Error("a record with no first-seen time was promoted")
	}
}

// A waiter already inside the cap takes no slot, so it does not use up the
// quarter: the quarter is for the ones past the cap.
func TestOrderAdvanceStarvationCountsOnlyWaitersPastTheCap(t *testing.T) {
	pinStarveClock(t)
	var todo []ledger.Record
	todo = append(todo, starvedRec(100, 20*24*time.Hour)) // an old posting that has a title rank: inside the cap
	for i := 0; i < 10; i++ {
		todo = append(todo, freshRec(i))
	}
	todo = append(todo, starvedRec(1, 5*24*time.Hour), starvedRec(2, 6*24*time.Hour), starvedRec(3, 7*24*time.Hour))
	rank := func(p posting.Posting) int {
		if p.Key == "gh:acme:old100" {
			return 5
		}
		return 0
	}
	orderAdvance(todo, slices.Clone(todo), rank, withSlots(8))
	got := starveKeys(todo)[:8]
	n := 0
	for _, k := range got {
		if k != "gh:acme:old100" && k[len("gh:acme:"):][:3] == "old" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d promoted besides the ranked one, want a quarter of 8 (2): %v", n, got)
	}
	if !slices.Contains(got, "gh:acme:old100") {
		t.Errorf("the ranked old posting left the window: %v", got)
	}
}

// What costs no call (a scored posting whose note is deferred) is not what a
// starved slot is for, and the warm's order and a plain order do not take the
// guard: only an advance order given its slots does.
func TestOrderAdvanceStarvationLeavesNotesDeferredAndOtherOrdersAlone(t *testing.T) {
	pinStarveClock(t)
	build := func() []ledger.Record {
		var todo []ledger.Record
		for i := 0; i < 8; i++ {
			todo = append(todo, freshRec(i))
		}
		deferred := starvedRec(0, 30*24*time.Hour)
		deferred.Status = posting.Scored
		deferred.Annotations = map[string]string{pipeline.AnnNotesDeferred: "budget"}
		return append(todo, deferred)
	}
	todo := build()
	orderAdvance(todo, slices.Clone(todo), nil, withSlots(8))
	if slices.Contains(starveKeys(todo)[:8], "gh:acme:old000") {
		t.Error("a posting whose note is deferred took a starved slot")
	}
	for name, opts := range map[string][]orderOption{
		"no slots":  nil,
		"zero":      {withSlots(0)},
		"too small": {withSlots(3)}, // a quarter of 3 is none
		"warm":      {withWarmPriority(), withSlots(8)},
		"all fit":   {withSlots(100)},
	} {
		todo := build()
		todo[len(todo)-1].Annotations = nil
		todo[len(todo)-1].Status = posting.Live
		orderAdvance(todo, slices.Clone(todo), nil, opts...)
		if starveKeys(todo)[0] == "gh:acme:old000" {
			t.Errorf("%s: the guard ran", name)
		}
		if name == "all fit" && starveKeys(todo)[len(todo)-1] != "gh:acme:old000" {
			t.Errorf("%s: the order changed though everything fits", name)
		}
	}
}

// Undated postings from a board's first crawl have no known age, so they sorted
// after everything with one and never reached the front. Once they have waited
// three days they do, a quarter of a run at a time, oldest first, until none
// is left.
func TestUndatedFirstCrawlPostingsDrainAcrossRuns(t *testing.T) {
	pinStarveClock(t)
	var pool []ledger.Record
	for i := 0; i < 40; i++ {
		pool = append(pool, starvedRec(i, 4*24*time.Hour+time.Duration(i)*time.Minute))
	}
	runs := 0
	for len(pool) > 0 && runs < 50 {
		runs++
		// Each run brings 20 fresh postings that outrank the waiters.
		todo := slices.Clone(pool)
		for i := 0; i < 20; i++ {
			todo = append(todo, freshRec(runs*100+i))
		}
		orderAdvance(todo, slices.Clone(todo), nil, withSlots(8))
		done := map[posting.Key]bool{}
		for _, r := range todo[:8] {
			done[r.Key] = true
		}
		var rest []ledger.Record
		for _, r := range pool {
			if !done[r.Key] {
				rest = append(rest, r)
			}
		}
		if len(pool)-len(rest) != 2 {
			t.Fatalf("run %d advanced %d waiters, want a quarter of 8", runs, len(pool)-len(rest))
		}
		pool = rest
	}
	if len(pool) != 0 || runs != 20 {
		t.Errorf("%d waiters left after %d runs, want 40 drained in 20", len(pool), runs)
	}
}
