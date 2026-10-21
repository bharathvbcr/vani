package eval

import (
	"math"
	"testing"
)

// The store refuses a would_apply p_select below MinInclusionProbability, in
// range or not, on write (Validate) and therefore on read (an unreadable line).
func TestPSelectBelowFloorIsRefused(t *testing.T) {
	mk := func(p float64) Entry {
		return Entry{Key: "k", Labels: map[string]string{WouldApply: "yes"}, Source: "decile:5", PSelect: p}
	}
	for _, p := range []float64{1e-320, math.SmallestNonzeroFloat64, 1e-160, MinInclusionProbability / 2, 0, -0.1, math.NaN(), math.Inf(1), 1.5} {
		if err := mk(p).Validate(); err == nil {
			t.Errorf("p_select %g accepted", p)
		}
	}
	for _, p := range []float64{MinInclusionProbability, 1e-6, 0.5, 1} {
		if err := mk(p).Validate(); err != nil {
			t.Errorf("p_select %g refused: %v", p, err)
		}
	}
}
