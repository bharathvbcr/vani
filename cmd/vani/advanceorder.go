package main

import (
	"cmp"
	"slices"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// orderAdvance sorts todo in the order a run advances it: highest title rank
// first (rank nil ranks every posting alike), then the freshest posting first,
// then ledger order. all is every ledger record, eligible or not; it says when
// each board was first crawled.
//
// A posting's freshness is jobinsight.FreshAge, the one owner of the rule: a
// plausible posted date (never later than when Vani first saw it), else
// first-seen for a posting that appeared after its board's first crawl. One
// listed in its board's first crawl without a usable date has no known age: it
// sorts after every posting whose freshness is known, in ledger order.
// UpdatedAt is not used: Greenhouse moves it on every edit.
func orderAdvance(todo, all []ledger.Record, rank func(posting.Posting) int) {
	at := now()
	firstCrawl := map[string]time.Time{}
	for _, r := range all {
		b := jobinsight.BoardOf(string(r.Key))
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
		o.fresh, _, o.known = jobinsight.FreshAge(r.Posting.PostedAt, r.FirstSeen, firstCrawl[jobinsight.BoardOf(string(r.Key))], at)
		if !o.known {
			o.fresh = time.Time{} // unknown ages tie, in ledger order
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
