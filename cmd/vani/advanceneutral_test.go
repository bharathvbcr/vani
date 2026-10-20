package main

import (
	"slices"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// advance orders an age-neutral employer's undated posting as `vani jobs` and
// the digest rank it (held fresh, not aged by first-seen), not by the aging the
// advance order used to apply because it built its ranking with no config.
func TestOrderAdvanceHonoursAgeNeutralEmployers(t *testing.T) {
	at := now()
	crawl := at.AddDate(0, 0, -20)
	undated := ledger.Record{Key: "gh:google:1", Status: posting.Live, FirstSeen: crawl,
		Posting: posting.Posting{Key: "gh:google:1", Company: "Google", Title: "Software Engineer III, AI/ML"}}
	dated := ledger.Record{Key: "gh:other:1", Status: posting.Live, FirstSeen: at.AddDate(0, 0, -12),
		Posting: posting.Posting{Key: "gh:other:1", Company: "Other", Title: "ML Engineer", PostedAt: at.AddDate(0, 0, -12)}}
	// The other board's first crawl was earlier, so its posting is dated, not first-crawl.
	otherCrawl := ledger.Record{Key: "gh:other:0", Status: posting.Dead, FirstSeen: crawl.AddDate(0, 0, -30),
		Posting: posting.Posting{Key: "gh:other:0", Company: "Other"}}
	all := []ledger.Record{undated, dated, otherCrawl}
	same := func(ledger.Record) (float64, bool) { return 0.8, true }
	order := func(opts ...orderOption) []posting.Key {
		todo := []ledger.Record{undated, dated}
		orderAdvance(todo, all, nil, opts...)
		return []posting.Key{todo[0].Key, todo[1].Key}
	}
	if got := order(withSemantic(same)); !slices.Equal(got, []posting.Key{"gh:other:1", "gh:google:1"}) {
		t.Fatalf("without age-neutral: %v, want the dated posting first", got)
	}
	neutral := jobinsight.AgeNeutralSet([]string{"Google"})
	if got := order(withSemantic(same), withAgeNeutral(neutral)); !slices.Equal(got, []posting.Key{"gh:google:1", "gh:other:1"}) {
		t.Fatalf("with age-neutral Google: %v, want Google first", got)
	}
	// Without the semantic order age-neutral changes nothing (it reads age
	// only through Score).
	if got := order(withAgeNeutral(neutral)); !slices.Equal(got, order()) {
		t.Fatalf("age-neutral alone changed the order: %v", got)
	}
}
