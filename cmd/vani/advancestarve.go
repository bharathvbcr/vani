package main

import (
	"slices"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
)

// Starvation guard for the advance order. A run advances its first
// advance_per_run postings and the rest wait (they stay Live, "pending"). The
// order puts the highest title rank and the freshest first, so while fresh
// postings keep arriving faster than the cap, an older one never reaches the
// front: above all a posting listed in its board's first crawl with no usable
// posted date, which has no known age and sorts after every posting that has
// one. It would wait for ever.
//
// So a posting that has waited starvedAfter in the ledger moves ahead of the
// fresh ones, oldest first, but only into a quarter of the run's slots
// (starvedShare): the fresh top-ranked postings fill the rest. It only orders
// (Rule 7): nothing is dropped or skipped.

const (
	// starvedAfter is how long a posting waits before it counts as starved.
	starvedAfter = 3 * 24 * time.Hour
	// starvedShare is the part of a run's slots starved postings may take, as a
	// divisor: a quarter.
	starvedShare = 4
)

// promoteStarved reorders todo (already in advance order) so that the oldest
// starved postings past the first `slots` take the front of the run, at most
// slots/starvedShare of them. A posting is starved when its ledger record was
// first written starvedAfter ago or longer: FirstSeen is when it entered the
// ledger, which for a Live posting is when it began to wait. (A reopened
// posting keeps its first sighting, so it reads as having waited from then.)
//
// Left alone: a posting already inside the cap (it takes no slot, so it does
// not use up the share), one whose note is deferred (it costs no call and waits
// behind every posting that can move, notes budget or not), one with no
// first-seen time or one in the future (a clock fault is not a wait), and a run
// whose slots give the share none (under 4) or that fits everything.
func promoteStarved(todo []ledger.Record, slots int, at time.Time) {
	share := slots / starvedShare
	if share < 1 || len(todo) <= slots {
		return
	}
	type waiter struct {
		idx  int
		seen time.Time
	}
	var waiting []waiter
	for i := slots; i < len(todo); i++ {
		r := todo[i]
		if r.FirstSeen.IsZero() || at.Sub(r.FirstSeen) < starvedAfter || pipeline.NotesDeferred(r) {
			continue
		}
		waiting = append(waiting, waiter{i, r.FirstSeen})
	}
	if len(waiting) == 0 {
		return
	}
	// Oldest first; a tie keeps the order the posting already had.
	slices.SortStableFunc(waiting, func(a, b waiter) int { return a.seen.Compare(b.seen) })
	waiting = waiting[:min(share, len(waiting))]
	promoted := make(map[int]bool, len(waiting))
	out := make([]ledger.Record, 0, len(todo))
	for _, w := range waiting {
		promoted[w.idx] = true
		out = append(out, todo[w.idx])
	}
	for i, r := range todo {
		if !promoted[i] {
			out = append(out, r)
		}
	}
	copy(todo, out)
}
