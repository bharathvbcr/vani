package rankfit

import (
	"fmt"
	"math"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// The silver pretrain (stage 8 of build/fable-plan-2026-11-12-ranking.md): the
// pairwise would_apply auditor's picks fit Score's shares first, and that fit
// becomes the point the owner-labelled fit is pulled toward (Options.Prior) in
// place of the hand weights. The gold gate is unchanged: Fit still measures
// the result against the hand weights on held-out owner judgments, and Beats
// decides whether anything is written. Silver answers never become labels.

// MinSilverPairs is the fewest decisive auditor answers a prior is fitted on.
const MinSilverPairs = 100

// SilverPair is one auditor answer that chose Win over Lose (item keys).
type SilverPair struct{ Win, Lose string }

// SilverPrior is the shares the auditor's picks fit, and what they rest on.
type SilverPrior struct {
	Shares      Shares  `json:"shares"`
	Pairs       int     `json:"pairs"`
	Excluded    int     `json:"excluded"` // pairs touching an owner-judged posting, left out
	Items       int     `json:"items"`
	Temperature float64 `json:"temperature"`
	Converged   bool    `json:"converged"`
	Iterations  int     `json:"iterations"`
	Lambda      float64 `json:"lambda"`
}

// FitSilverPrior fits Score's shares to the auditor's decisive answers over
// items (the postings they compare, scored as the ranking scores them), pulled
// toward base's hand shares with lambda as in Fit. Every pair must name two
// different items of items. A pair that touches a posting judged reports
// owner-judged is dropped (Excluded): it would teach the prior the answers the
// gold evaluation grades. judged is required. Below MinSilverPairs of what is
// left it refuses with a NotRunError.
func FitSilverPrior(items []Item, pairs []SilverPair, judged func(key string) bool, base jobinsight.Weights, lambda float64) (SilverPrior, error) {
	if !(lambda > 0) || math.IsInf(lambda, 0) {
		lambda = DefaultLambda
	}
	out := SilverPrior{Lambda: lambda, Items: len(items)}
	if judged == nil {
		return out, fmt.Errorf("rankfit: a silver prior needs the owner-judged test")
	}
	if len(items) > MaxItems {
		return out, fmt.Errorf("rankfit: %d silver items, max %d", len(items), MaxItems)
	}
	if len(pairs) > MaxPairsLimit {
		return out, fmt.Errorf("rankfit: %d silver pairs, max %d", len(pairs), MaxPairsLimit)
	}
	idx := make(map[string]int, len(items))
	for i := range items {
		it := &items[i]
		if _, dup := idx[it.Key]; dup {
			return out, fmt.Errorf("rankfit: silver item %q appears twice", it.Key)
		}
		if err := it.check(); err != nil {
			return out, err
		}
		idx[it.Key] = i
	}
	ps := make([]pair, 0, len(pairs))
	for _, p := range pairs {
		if judged(p.Win) || judged(p.Lose) {
			out.Excluded++
			continue
		}
		w, okW := idx[p.Win]
		l, okL := idx[p.Lose]
		switch {
		case !okW || !okL:
			return out, fmt.Errorf("rankfit: silver pair %s over %s names a posting with no item", p.Win, p.Lose)
		case w == l:
			return out, fmt.Errorf("rankfit: silver pair compares %s with itself", p.Win)
		}
		ps = append(ps, pair{win: w, lose: l})
	}
	out.Pairs = len(ps)
	if err := needAtLeast("silver prior", "decisive auditor answers", len(ps), MinSilverPairs); err != nil {
		return out, err
	}
	b := blendOf(base)
	m := newModel(items, ps, b, SharesOf(base).Array(), lambda, float64(len(items)), false)
	x, iters, conv := minimize(m.objective, m.start())
	out.Shares = sharesFrom(m.shares(x))
	out.Temperature = math.Exp(x[NumParts])
	out.Iterations, out.Converged = iters, conv
	return out, nil
}

// priorCenter is where Fit pulls the log-shares: the hand shares, or prior
// when set. A prior must give a positive finite share to exactly the parts the
// hand weights do: a part the hand weights leave out stays out, and a prior
// cannot switch one off.
func priorCenter(hand [NumParts]float64, prior *Shares) ([NumParts]float64, error) {
	if prior == nil {
		return hand, nil
	}
	p := prior.Array()
	for i := range NumParts {
		ok := p[i] > 0 && !math.IsInf(p[i], 0)
		if (hand[i] > 0) != ok {
			return hand, fmt.Errorf("rankfit: the prior's %s share %v does not match the hand weights' %v (a prior gives a share to exactly the parts they do)", PartNames[i], p[i], hand[i])
		}
	}
	return p, nil
}
