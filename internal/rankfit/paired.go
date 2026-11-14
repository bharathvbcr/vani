package rankfit

import (
	"math"
	"math/rand/v2"
	"slices"
)

// MaxPairedResamples bounds PairedAUCDiff's bootstrap: each resample computes
// two quadratic AUCs.
const MaxPairedResamples = 4000

// PairedAUCDiff compares two scorings of the same judged postings: posA[i] and
// posB[i] are wanted posting i under arms A and B, negA[j] and negB[j] unwanted
// posting j. Diff is AUC(B) - AUC(A). Lo and Hi are its two-sided (1-alpha)
// interval from a stratified bootstrap that resamples postings, not scores, so
// both arms see the same draw (the arms are correlated; two separate intervals
// would overstate the noise). alpha outside (0, 0.5) means 0.05; resamples is
// clamped to [1, MaxPairedResamples] (0 means MaxPairedResamples). ok is false
// when the arms' lengths disagree, a class has fewer than MinPartAUCClass or
// more than MaxPartAUCItems postings, or a score is NaN.
func PairedAUCDiff(posA, posB, negA, negB []float64, alpha float64, resamples int, seed uint64) (diff, lo, hi float64, ok bool) {
	if len(posA) != len(posB) || len(negA) != len(negB) {
		return 0, 0, 0, false
	}
	if len(posA) < MinPartAUCClass || len(negA) < MinPartAUCClass || len(posA) > MaxPartAUCItems || len(negA) > MaxPartAUCItems {
		return 0, 0, 0, false
	}
	for _, s := range [][]float64{posA, posB, negA, negB} {
		if slices.ContainsFunc(s, math.IsNaN) {
			return 0, 0, 0, false
		}
	}
	if !(alpha > 0 && alpha < 0.5) {
		alpha = 0.05
	}
	if resamples <= 0 || resamples > MaxPairedResamples {
		resamples = MaxPairedResamples
	}
	diff = auc(posB, negB) - auc(posA, negA)
	r := rand.New(rand.NewPCG(seed, 7))
	stats := make([]float64, resamples)
	pa, pb := make([]float64, len(posA)), make([]float64, len(posA))
	na, nb := make([]float64, len(negA)), make([]float64, len(negA))
	for i := range stats {
		for j := range pa {
			k := r.IntN(len(posA))
			pa[j], pb[j] = posA[k], posB[k]
		}
		for j := range na {
			k := r.IntN(len(negA))
			na[j], nb[j] = negA[k], negB[k]
		}
		stats[i] = auc(pb, nb) - auc(pa, na)
	}
	slices.Sort(stats)
	at := func(q float64) float64 {
		return stats[min(max(int(q*float64(resamples)), 0), resamples-1)]
	}
	return diff, at(alpha / 2), at(1 - alpha/2), true
}
