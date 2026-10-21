package rankfit

import (
	"math"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// A p_select below eval.MinInclusionProbability is refused at the boundary: a
// pair weight 1/(p_i p_j) overflows to +Inf under about 1e-154 and used to
// leave NaN accuracies, a [0, 0] gain and hand shares, silently. Refusal must
// be an error, never a result; the floor itself is accepted and finite.
func TestTinyPSelectIsRefusedNotSilentlyPoisoned(t *testing.T) {
	t.Parallel()
	build := func(p float64) []Item {
		items := world(10, 600, 300)
		for i := range items {
			if i%2 == 0 {
				items[i].Source, items[i].PSelect = "decile:5", p
			}
		}
		return items
	}
	for _, p := range []float64{1e-100, 1e-160, 1e-200, 1e-300, math.SmallestNonzeroFloat64, eval.MinInclusionProbability / 2} {
		if res, err := Fit(build(p), jobinsight.DefaultWeights(), Options{Seed: 1}); err == nil {
			t.Errorf("p_select %g accepted; result fit %v hand %v gain [%v, %v]", p, res.FitAccuracy, res.HandAccuracy, res.GainLo, res.GainHi)
		}
	}
	res, err := Fit(build(eval.MinInclusionProbability), jobinsight.DefaultWeights(), Options{Seed: 1})
	if err != nil {
		t.Fatalf("p_select at the floor refused: %v", err)
	}
	if !resultFinite(res) || !finite(res.Temperature) {
		t.Errorf("p_select at the floor: non-finite result %+v", res)
	}
}

// The held-out evaluation itself refuses a non-finite weight even when the
// boundary check was bypassed (a caller building Items by hand).
func TestHeldOutEvaluationRefusesNonFiniteWeight(t *testing.T) {
	t.Parallel()
	items := world(11, 40, 40)
	for i := range items {
		items[i].Source, items[i].PSelect = "decile:5", 1e-200
	}
	hand := SharesOf(jobinsight.DefaultWeights()).Array()
	idx := make([]int, len(items))
	for i := range idx {
		idx[i] = i
	}
	test := []pair{{win: 0, lose: 1}, {win: 2, lose: 3}}
	if _, err := evaluateHeldOut(items, idx, test, blendOf(jobinsight.DefaultWeights()), hand, hand, 1); err == nil {
		t.Fatal("non-finite held-out weights were scored")
	}
}
