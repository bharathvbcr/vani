package main

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// orderAdvance sorts todo in the order a run advances it: highest title rank
// first (rank nil ranks every posting alike), then the freshest posting first,
// then ledger order. all is every ledger record, eligible or not; it says when
// each board was first crawled.
//
// A posting's freshness is its posted date when the source gives one, never
// later than when Vani first saw it (a future date cannot jump the queue).
// Without a posted date, a posting that appeared after its board's first
// crawl was posted at most one run before Vani first saw it, so first-seen
// stands in. One listed in its board's first crawl has no known age: it sorts
// after every posting whose freshness is known, in ledger order. UpdatedAt is
// not used: Greenhouse moves it on every edit.
func orderAdvance(todo, all []ledger.Record, rank func(posting.Posting) int) {
	firstCrawl := map[string]time.Time{}
	for _, r := range all {
		b := boardOf(r.Key)
		if t, ok := firstCrawl[b]; !ok || r.FirstSeen.Before(t) {
			firstCrawl[b] = r.FirstSeen
		}
	}
	type order struct {
		rank  int
		known bool
		fresh time.Time
	}
	keys := make(map[posting.Key]order, len(todo))
	for _, r := range todo {
		o := order{}
		if rank != nil {
			o.rank = rank(r.Posting)
		}
		switch p := r.Posting.PostedAt; {
		case !p.IsZero():
			o.known, o.fresh = true, p
			if !r.FirstSeen.IsZero() && p.After(r.FirstSeen) {
				o.fresh = r.FirstSeen
			}
		case !r.FirstSeen.Equal(firstCrawl[boardOf(r.Key)]):
			o.known, o.fresh = true, r.FirstSeen
		}
		keys[r.Key] = o
	}
	slices.SortStableFunc(todo, func(a, b ledger.Record) int {
		x, y := keys[a.Key], keys[b.Key]
		if c := cmp.Compare(y.rank, x.rank); c != 0 {
			return c
		}
		if x.known != y.known {
			if x.known {
				return -1
			}
			return 1
		}
		return y.fresh.Compare(x.fresh)
	})
}

// boardOf is the board part of a key: everything before its last component
// ("gh:acme" for "gh:acme:123", "wd:tenant:site" for a Workday key).
func boardOf(k posting.Key) string {
	s := string(k)
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		return s[:i]
	}
	return s
}
