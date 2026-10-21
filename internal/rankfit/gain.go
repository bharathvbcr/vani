package rankfit

import (
	"fmt"
	"math/rand/v2"
	"slices"
)

const (
	// MinHeldOutPairs is the fewest held-out judged pairs a fit may rest on
	// before it can beat the hand weights. Below it the held-out accuracies are
	// too coarse to tell a gain from chance, whatever the interval says.
	MinHeldOutPairs = 200
	// MinScoredStratumPairs is the fewest held-out pairs whose two postings are
	// both from the audit's scored deciles. Ranking orders scored postings; the
	// audit oversamples the dropped ones, so a held-out set that is mostly
	// dropped says little about the order that matters. It applies when the
	// held-out set holds any audit-sampled posting.
	MinScoredStratumPairs = 50
	// BootstrapResamples is how many item resamples the gain interval is taken
	// over. The percentile ends are then 25 draws in from the extremes.
	BootstrapResamples = 1000
	// gainAlpha is the tail each end of the gain interval leaves out: the
	// interval is 95% two-sided, and Beats needs its lower end above 0.
	gainAlpha = 0.025
	// MaxHeldOutPairs bounds the held-out pairs one fit scores and resamples
	// (the resampling costs BootstrapResamples times this many steps). A larger
	// held-out set is refused, not sampled: a sampled pair set would break the
	// item-level resampling the interval rests on.
	MaxHeldOutPairs = 2_000_000
)

// StratumAccuracy is the held-out accuracy over the pairs of one stratum. A
// pair's stratum is its postings' own when they share one ("scored",
// "dropped"), "unstratified" when neither has one, else "mixed".
type StratumAccuracy struct {
	Stratum      string  `json:"stratum"`
	Pairs        int     `json:"pairs"`
	FitAccuracy  float64 `json:"fit_accuracy"`
	HandAccuracy float64 `json:"hand_accuracy"`
}

const (
	pairUnstratified = "unstratified"
	pairMixed        = "mixed"
)

func pairStratum(a, b *Item) string {
	sa, sb := a.stratum(), b.stratum()
	switch {
	case sa == "" && sb == "":
		return pairUnstratified
	case sa == sb:
		return sa
	}
	return pairMixed
}

// heldPair is one held-out pair with what each share set earns on it: 1 when
// it orders the winner first, half on a tie. w is the pair's Horvitz-Thompson
// weight, the product of its postings'.
type heldPair struct {
	a, b      int // positions in the held-out item list
	w         float64
	fit, hand float64
}

func credit(d float64) float64 {
	switch {
	case d > 0:
		return 1
	case d == 0:
		return 0.5
	}
	return 0
}

// heldOutEval is the held-out accuracy of the fitted and the hand shares, the
// same by stratum, and the gain interval between them.
type heldOutEval struct {
	fit, hand        float64
	gainLo, gainHi   float64
	strata           []StratumAccuracy
	scoredPairs      int
	stratified       bool // any held-out posting is audit-sampled
	pairs, items     int
	bootstrapSamples int
}

// evaluateHeldOut scores test (pairs of positions in items) under the fitted
// and the hand shares. Accuracy is weighted by each pair's Horvitz-Thompson
// weight, so the oversampled dropped stratum counts for the postings it stands
// for. The gain interval is a percentile bootstrap over the held-out ITEMS:
// each resample draws as many items as there are, with replacement, and a pair
// counts m_a*m_b times. Pairs share postings, so resampling pairs would make
// the interval far too narrow; a sign test over pairs would share that fault.
// The draws come from a generator seeded with seed, so the interval is a
// function of its inputs.
func evaluateHeldOut(items []Item, testItems []int, test []pair, b blend, fitted, hand [NumParts]float64, seed uint64) heldOutEval {
	var ev heldOutEval
	ev.items, ev.pairs = len(testItems), len(test)
	if len(test) == 0 {
		return ev
	}
	uf, uh := make([]float64, len(items)), make([]float64, len(items))
	for i := range items {
		uf[i] = utility(&items[i], b, &fitted, 1, 1)
		uh[i] = utility(&items[i], b, &hand, 1, 1)
	}
	pos := make(map[int]int, len(testItems))
	for p, i := range testItems {
		pos[i] = p
		if Sampled(items[i].Source) {
			ev.stratified = true
		}
	}
	names := map[string]int{}
	var strata []StratumAccuracy
	var sw, sf, sh []float64 // per stratum: weight, fit credit, hand credit
	hp := make([]heldPair, len(test))
	var totW, totF, totH float64
	for k, p := range test {
		wi, li := &items[p.win], &items[p.lose]
		h := heldPair{a: pos[p.win], b: pos[p.lose], w: wi.weight() * li.weight(),
			fit: credit(uf[p.win] - uf[p.lose]), hand: credit(uh[p.win] - uh[p.lose])}
		name := pairStratum(wi, li)
		idx, ok := names[name]
		if !ok {
			idx = len(strata)
			names[name] = idx
			strata = append(strata, StratumAccuracy{Stratum: name})
			sw, sf, sh = append(sw, 0), append(sf, 0), append(sh, 0)
		}
		hp[k] = h
		strata[idx].Pairs++
		sw[idx] += h.w
		sf[idx] += h.w * h.fit
		sh[idx] += h.w * h.hand
		totW += h.w
		totF += h.w * h.fit
		totH += h.w * h.hand
		if name == StratumScored {
			ev.scoredPairs++
		}
	}
	ev.fit, ev.hand = totF/totW, totH/totW
	for i := range strata {
		strata[i].FitAccuracy, strata[i].HandAccuracy = sf[i]/sw[i], sh[i]/sw[i]
	}
	slices.SortFunc(strata, func(x, y StratumAccuracy) int {
		switch {
		case x.Stratum < y.Stratum:
			return -1
		case x.Stratum > y.Stratum:
			return 1
		}
		return 0
	})
	ev.strata = strata

	rng := rand.New(rand.NewPCG(seed, 0x626f6f747374)) // "bootst"
	n := len(testItems)
	m := make([]float64, n)
	gains := make([]float64, BootstrapResamples)
	for r := range gains {
		clear(m)
		for range n {
			m[rng.IntN(n)]++
		}
		var num, den float64
		for _, h := range hp {
			if w := m[h.a] * m[h.b] * h.w; w != 0 {
				num += w * (h.fit - h.hand)
				den += w
			}
		}
		if den > 0 {
			gains[r] = num / den
		}
	}
	slices.Sort(gains)
	cut := int(gainAlpha * BootstrapResamples)
	ev.gainLo, ev.gainHi = gains[cut], gains[BootstrapResamples-1-cut]
	ev.bootstrapSamples = BootstrapResamples
	return ev
}

// NoGainReason says why the fit does not beat the hand weights, or "" when it
// does: too few held-out pairs, too few scored-stratum pairs, or a gain
// interval that does not clear 0. A result that was never evaluated (no
// resamples) never beats.
func (r FitResult) NoGainReason() string {
	switch {
	case r.Resamples == 0:
		return "the held-out gain was never evaluated"
	case r.HeldOutPairs < MinHeldOutPairs:
		return fmt.Sprintf("only %d held-out pairs, fewer than the %d a gain needs", r.HeldOutPairs, MinHeldOutPairs)
	case r.Stratified && r.ScoredPairs < MinScoredStratumPairs:
		return fmt.Sprintf("only %d held-out pairs between scored-stratum postings, fewer than the %d needed", r.ScoredPairs, MinScoredStratumPairs)
	case !(r.GainLo > 0):
		return fmt.Sprintf("the gain interval [%+.4f, %+.4f] does not clear 0", r.GainLo, r.GainHi)
	}
	return ""
}

// Beats reports whether the fitted shares order the held-out pairs better than
// the hand weights (Phase 5's exit test): enough held-out pairs, enough of them
// between scored-stratum postings, and the lower end of the 95% item-bootstrap
// interval of the accuracy gain above 0. A bare FitAccuracy > HandAccuracy is
// true for about half of the null worlds.
func (r FitResult) Beats() bool { return r.NoGainReason() == "" }
