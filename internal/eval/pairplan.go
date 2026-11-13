package eval

import (
	"bytes"
	"slices"
	"sort"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Pair sampling for the pairwise would_apply auditor. A pair is two postings of
// one audit stratum (a score decile or a drop class, AuditStrata), so the model
// compares like with like and the answer says something about taste, not about
// which stratum a posting came from. Everything is a function of (candidates,
// seed, what is already stored), so a run is resumable and the same seed
// draws the same pairs.

// PairCandidate is a posting the auditor may be asked about, with the stratum it
// stands in now.
type PairCandidate struct {
	Posting posting.Posting
	Stratum string
}

// PairPlanOpts are one plan's settings.
type PairPlanOpts struct {
	Seed string
	// N is the pairs to plan, at most; with BothOrders each is asked in both
	// orders, so up to 2N questions.
	N          int
	BothOrders bool
	// Cover are the postings that must be compared: the owner's gold-labelled
	// postings made after the freeze. Random stratum pairs would almost never
	// touch the few of them, and a posting with no comparison has no score to
	// evaluate. Each gets Mates stratum-mates chosen by a hash of the seed and the
	// two keys, never by anything the owner said about either posting, so the
	// labels cannot steer who it is compared with.
	Cover map[posting.Key]bool
	Mates int
}

// PlannedAsk is one question to ask: the pair in the order it is shown.
type PlannedAsk struct {
	A, B    posting.Posting
	Stratum string
}

// PlanStats says what a plan did, so a short plan is visible as short.
type PlanStats struct {
	// Pairs is the pairs planned; Stored counts pairs that were already
	// answered under the run's identity and so were passed over.
	Pairs, Stored int
	// Cover counts postings asked to be covered; CoverShort counts those that
	// did not get Mates mates: not a candidate (no text, closed, excluded),
	// alone in their stratum, or left over when the plan filled.
	Cover, CoverShort int
	Strata            int
}

func hashLess(a, b [32]byte) int { return bytes.Compare(a[:], b[:]) }

// PlanPairs plans up to o.N pairs from cands. pending reports whether a
// question still has to be asked (it is not already stored); an order it
// reports done is left out and, for a pair with no pending order, the pair is
// passed over and does not count toward N.
func PlanPairs(cands []PairCandidate, o PairPlanOpts, pending func(PlannedAsk) bool) ([]PlannedAsk, PlanStats) {
	var st PlanStats
	byKey := map[posting.Key]PairCandidate{}
	members := map[string][]posting.Key{}
	for _, c := range cands {
		k := c.Posting.Key
		if _, dup := byKey[k]; dup || k == "" || c.Stratum == "" {
			continue
		}
		byKey[k] = c
		members[c.Stratum] = append(members[c.Stratum], k)
	}
	names := make([]string, 0, len(members))
	for n, ks := range members {
		names = append(names, n)
		slices.SortFunc(ks, func(a, b posting.Key) int {
			return hashLess(hashOf(o.Seed, "pairmember", string(a)), hashOf(o.Seed, "pairmember", string(b)))
		})
	}
	sort.Slice(names, func(i, j int) bool {
		return hashLess(hashOf(o.Seed, "pairstratum", names[i]), hashOf(o.Seed, "pairstratum", names[j])) < 0
	})
	st.Strata = len(names)

	var asks []PlannedAsk
	emitted := map[[2]posting.Key]bool{}
	// try plans the pair (x, y) once: a pair already planned is left alone, one
	// with every order already stored is counted in Stored and not planned.
	try := func(x, y posting.Key) {
		lo, hi := PairKeyOf(x, y)
		if emitted[[2]posting.Key{lo, hi}] {
			return
		}
		emitted[[2]posting.Key{lo, hi}] = true
		first, second := byKey[lo], byKey[hi]
		if hashOf(o.Seed, "order", string(lo)+"\x00"+string(hi))[0]&1 == 1 {
			first, second = second, first
		}
		orders := [][2]PairCandidate{{first, second}}
		if o.BothOrders {
			orders = append(orders, [2]PairCandidate{second, first})
		}
		added := false
		for _, ord := range orders {
			ask := PlannedAsk{A: ord[0].Posting, B: ord[1].Posting, Stratum: ord[0].Stratum}
			if pending(ask) {
				asks = append(asks, ask)
				added = true
			}
		}
		if added {
			st.Pairs++
		} else {
			st.Stored++
		}
	}
	full := func() bool { return st.Pairs >= o.N }

	// Coverage first: the evaluation needs these postings compared.
	cover := make([]posting.Key, 0, len(o.Cover))
	for k := range o.Cover {
		if _, ok := byKey[k]; ok {
			cover = append(cover, k)
		}
	}
	slices.SortFunc(cover, func(a, b posting.Key) int {
		return hashLess(hashOf(o.Seed, "cover", string(a)), hashOf(o.Seed, "cover", string(b)))
	})
	st.Cover = len(o.Cover)
	covered := 0
	for _, k := range cover {
		pool := slices.Clone(members[byKey[k].Stratum])
		slices.SortFunc(pool, func(a, b posting.Key) int {
			return hashLess(hashOf(o.Seed, "mate:"+string(k), string(a)), hashOf(o.Seed, "mate:"+string(k), string(b)))
		})
		got := 0
		for _, m := range pool {
			if got == o.Mates || full() {
				break
			}
			if m == k {
				continue
			}
			try(k, m)
			got++
		}
		if got == o.Mates {
			covered++
		}
		if full() {
			break
		}
	}
	st.CoverShort = st.Cover - covered

	// Then the stratum round-robin: consecutive members, disjoint pairs.
	cursor := map[string]int{}
	for !full() {
		moved := false
		for _, n := range names {
			ks := members[n]
			if i := cursor[n]; i+1 < len(ks) {
				cursor[n] = i + 2
				moved = true
				try(ks[i], ks[i+1])
				if full() {
					break
				}
			}
		}
		if !moved {
			break
		}
	}
	return asks, st
}
