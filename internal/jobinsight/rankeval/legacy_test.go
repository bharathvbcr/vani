package rankeval

import (
	"math"
	"sort"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// Frozen copies of the pre-Score rankers. They are copied, not imported, so
// the baseline survives the removal of jobinsight.Relevance and Priority.

var legacyFitBase = map[string]float64{
	"strong_fit":         0.85,
	"partial_fit":        0.65,
	"adjacent":           0.45,
	"mismatch_seniority": 0.20,
	"mismatch_domain":    0.10,
}

func legacyClamp(v, lo, hi float64) float64 {
	switch {
	case math.IsNaN(v) || v < lo:
		return lo
	case v > hi:
		return hi
	}
	return v
}

func legacyRound1(v float64) float64 { return math.Round(v*10+1e-9) / 10 }

// legacyRelevance is jobinsight.Relevance: 75% rank-or-fit base, 25% keywords.
func legacyRelevance(rank *float64, roleFit string, targetHits int) float64 {
	base := 0.35
	b, ok := legacyFitBase[roleFit]
	switch {
	case rank != nil && !math.IsNaN(*rank):
		base = legacyClamp(*rank/100, 0, 1)
	case ok:
		base = b
	}
	if targetHits < 0 {
		targetHits = 0
	}
	kw := 1 - math.Exp(-float64(targetHits)/3.0)
	return legacyRound1(100 * (0.75*base + 0.25*kw))
}

// legacyPriorityOf is jobinsight.Priority with the default 14-day half-life.
func legacyPriorityOf(relevance float64, ageDays int) float64 {
	fresh := 0.0
	if ageDays >= 0 {
		fresh = math.Exp2(-float64(ageDays) / 14)
	}
	return legacyRound1(legacyClamp(relevance, 0, 100) * (0.5 + 0.5*fresh))
}

// legacyRec is a posting as the legacy rankers see it.
type legacyRec struct {
	id        int
	rank      float64
	hasRank   bool
	fit       string
	hits      int
	age       int
	firstSeen int // days since first seen; smaller is newer
	unsure    bool
}

// legacyFrom derives the legacy fields from the (scenario-masked) Inputs:
// rank = coverage + 10 for a preferred employer, fit string only when
// answered, target hits only with a target summary.
func legacyFrom(o obs) legacyRec {
	in := o.in
	l := legacyRec{id: o.id, age: o.legacyAge, firstSeen: o.firstSeenAge, unsure: in.Unsure}
	if in.HasCoverage {
		l.hasRank = true
		l.rank = in.Coverage
		if in.PreferredEmployer {
			l.rank += 10
		}
	}
	if in.RoleFit.Answered {
		l.fit = in.RoleFit.Value
	}
	if in.Target {
		l.hits = in.TitleHits + in.BodyHits
	}
	return l
}

func (l legacyRec) relevance() float64 {
	var rk *float64
	if l.hasRank {
		rk = &l.rank
	}
	return legacyRelevance(rk, l.fit, l.hits)
}

func legacyFitOrder(fit string) int {
	for i, v := range roleFits {
		if v == fit {
			return i
		}
	}
	return len(roleFits)
}

// legacyDefaultLess is cmd/vani sortJobs "default": rank desc (any rank beats
// none), role_fit order, newest first seen, then key.
func legacyDefaultLess(a, b legacyRec) bool {
	if a.hasRank != b.hasRank {
		return a.hasRank
	}
	if a.hasRank && a.rank != b.rank {
		return a.rank > b.rank
	}
	if fa, fb := legacyFitOrder(a.fit), legacyFitOrder(b.fit); fa != fb {
		return fa < fb
	}
	if a.firstSeen != b.firstSeen {
		return a.firstSeen < b.firstSeen
	}
	return a.id < b.id
}

// legacyDigestLess is internal/digest sortDigest: unsure last, then rank desc.
func legacyDigestLess(a, b legacyRec) bool {
	if a.unsure != b.unsure {
		return !a.unsure
	}
	if a.hasRank != b.hasRank {
		return a.hasRank
	}
	if a.hasRank && a.rank != b.rank {
		return a.rank > b.rank
	}
	return a.id < b.id
}

// legacyPriorityLess is sortJobs "priority": priority desc, relevance desc.
func legacyPriorityLess(a, b legacyRec) bool {
	ra, rb := a.relevance(), b.relevance()
	pa, pb := legacyPriorityOf(ra, a.age), legacyPriorityOf(rb, b.age)
	if pa != pb {
		return pa > pb
	}
	if ra != rb {
		return ra > rb
	}
	return a.id < b.id
}

// ranker orders a scenario's observations best first, returning posting ids.
type ranker struct {
	name  string
	order func(os []obs) []int
}

func legacyRanker(name string, less func(a, b legacyRec) bool) ranker {
	return ranker{name: name, order: func(os []obs) []int {
		recs := make([]legacyRec, len(os))
		for i, o := range os {
			recs[i] = legacyFrom(o)
		}
		sort.SliceStable(recs, func(i, j int) bool { return less(recs[i], recs[j]) })
		ids := make([]int, len(recs))
		for i, r := range recs {
			ids[i] = r.id
		}
		return ids
	}}
}

func scoreRanker(name string, w jobinsight.Weights) ranker {
	return ranker{name: name, order: func(os []obs) []int {
		return scoreRankerFunc(os, func(in jobinsight.Inputs) float64 { return jobinsight.ScoreWith(in, w).Score })
	}}
}

// scoreRankerFunc orders os by f descending, ties by id.
func scoreRankerFunc(os []obs, f func(jobinsight.Inputs) float64) []int {
	type sc struct {
		id int
		s  float64
	}
	xs := make([]sc, len(os))
	for i, o := range os {
		xs[i] = sc{o.id, f(o.in)}
	}
	sort.SliceStable(xs, func(i, j int) bool {
		if xs[i].s != xs[j].s {
			return xs[i].s > xs[j].s
		}
		return xs[i].id < xs[j].id
	})
	ids := make([]int, len(xs))
	for i, x := range xs {
		ids[i] = x.id
	}
	return ids
}

// referenceRankers are context rows in the table, never asserted: arrival
// order (random with respect to quality) and a freshest-first sort.
var referenceRankers = []ranker{
	{name: "ref-arrival", order: func(os []obs) []int {
		ids := make([]int, len(os))
		for i, o := range os {
			ids[i] = o.id
		}
		return ids
	}},
	{name: "ref-freshest", order: func(os []obs) []int {
		type ag struct{ id, age int }
		xs := make([]ag, len(os))
		for i, o := range os {
			xs[i] = ag{o.id, o.in.AgeDays}
			if o.in.AgeDays < 0 {
				xs[i].age = 1 << 20
			}
		}
		sort.SliceStable(xs, func(i, j int) bool { return xs[i].age < xs[j].age })
		ids := make([]int, len(xs))
		for i, x := range xs {
			ids[i] = x.id
		}
		return ids
	}},
}

var legacyRankers = []ranker{
	legacyRanker("legacyDefault", legacyDefaultLess),
	legacyRanker("legacyPriority", legacyPriorityLess),
	legacyRanker("legacyDigest", legacyDigestLess),
}
