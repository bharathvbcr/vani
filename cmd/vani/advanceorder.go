package main

import (
	"cmp"
	"slices"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// orderOption adjusts advanceAll and orderAdvance; nil options are ignored.
type orderOption func(*orderSettings)

type orderSettings struct {
	semantic func(ledger.Record) (float64, bool)
	// ageNeutral is jobinsight.RankContext.AgeNeutral for the Score order, so
	// advance ages an age-neutral employer's undated postings no more than
	// `vani jobs` and the digest do.
	ageNeutral map[string]bool
	// rules and titleRank are the config's drop rules and title signals
	// (rankRules), so the Score order reads them as `vani jobs` and the digest do.
	rules     []jobinsight.DropRule
	titleRank func(string) int
	// todo, when set, is which records advanceAll advances, in place of its
	// pipeline-status rule.
	todo func(ledger.Record) bool
}

func settingsOf(opts []orderOption) orderSettings {
	var set orderSettings
	for _, o := range opts {
		if o != nil {
			o(&set)
		}
	}
	return set
}

// withSemantic orders by Score with f as the knowledge-base similarity (see
// semanticRank); a nil f changes nothing.
func withSemantic(f func(ledger.Record) (float64, bool)) orderOption {
	return func(s *orderSettings) { s.semantic = f }
}

// withAgeNeutral passes the config's age_neutral_companies (as
// jobinsight.AgeNeutralSet) to the Score order; it changes nothing without
// withSemantic, whose Score order is the only one that reads age through Score.
func withAgeNeutral(set map[string]bool) orderOption {
	return func(s *orderSettings) { s.ageNeutral = set }
}

// withRank passes the config's drop rules and title signals (rankRules) to the
// Score order; like withAgeNeutral it changes nothing without withSemantic.
func withRank(rules []jobinsight.DropRule, titleRank func(string) int) orderOption {
	return func(s *orderSettings) { s.rules, s.titleRank = rules, titleRank }
}

// withTodo makes advanceAll advance exactly the records f selects (vani
// rescore's Scored and Explained records without a score) instead of those
// still in the pipeline. Its cap, concurrency, stop rules and ordering apply
// unchanged. A nil f changes nothing.
func withTodo(f func(ledger.Record) bool) orderOption {
	return func(s *orderSettings) { s.todo = f }
}

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
//
// With withSemantic, postings of the same title rank go in the order of
// jobinsight.Score (the one ranking owner) over their Inputs, so a posting that
// matches the owner's knowledge base is advanced before an equally fresh one
// that does not, and freshness still counts through Score's own age term. A
// live posting has no triage readings: fit and coverage are Score's unknown
// priors. Ties fall back to the freshness and ledger-order rules above. Without
// it the order is exactly the one above. Either way this only orders (Rule 7):
// no posting is dropped or skipped.
func orderAdvance(todo, all []ledger.Record, rank func(posting.Posting) int, opts ...orderOption) {
	set := settingsOf(opts)
	at := now()
	var rc jobinsight.RankContext
	if set.semantic != nil {
		rc = jobinsight.NewRankContext(all, nil, at)
		rc.Semantic, rc.AgeNeutral = set.semantic, set.ageNeutral
		rc.DropRules, rc.TitleRank = set.rules, set.titleRank
	}
	firstCrawl := map[string]time.Time{}
	for _, r := range all {
		b := jobinsight.BoardOf(string(r.Key))
		if t, ok := firstCrawl[b]; !ok || r.FirstSeen.Before(t) {
			firstCrawl[b] = r.FirstSeen
		}
	}
	type order struct {
		rank  int
		score float64
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
		if set.semantic != nil {
			o.score = jobinsight.Score(jobinsight.InputsFor(r, rc)).Score
		}
		keys[r.Key] = o
	}
	slices.SortStableFunc(todo, func(a, b ledger.Record) int {
		x, y := keys[a.Key], keys[b.Key]
		if c := cmp.Compare(y.rank, x.rank); c != 0 {
			return c
		}
		if c := cmp.Compare(y.score, x.score); c != 0 { // all 0 without a semantic cache
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
	// A scored posting whose note is deferred (unknown relevance, notes budget
	// spent) costs no call but still takes an advance slot each run; it waits
	// behind every posting that can move. Stable, so the order above holds.
	slices.SortStableFunc(todo, func(a, b ledger.Record) int {
		return cmp.Compare(boolInt(pipeline.NotesDeferred(a)), boolInt(pipeline.NotesDeferred(b)))
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
