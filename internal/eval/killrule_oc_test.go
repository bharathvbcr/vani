package eval

import (
	"math"
	"testing"
)

// binomPMF is the binomial distribution's probabilities for 0..n successes.
func binomPMF(n int, p float64) []float64 {
	out := make([]float64, n+1)
	for k := range out {
		lc, _ := math.Lgamma(float64(n + 1))
		la, _ := math.Lgamma(float64(k + 1))
		lb, _ := math.Lgamma(float64(n - k + 1))
		out[k] = math.Exp(lc - la - lb + float64(k)*math.Log(p) + float64(n-k)*math.Log1p(-p))
		if p == 0 {
			out[k] = 0
			if k == 0 {
				out[k] = 1
			}
		}
	}
	return out
}

// keepChance is the exact chance the kill rule keeps a cell whose answers are
// yes at rate cell, against a comparator with compN answers at rate comp by
// the cell's 40th answer, and the chance the first look turns it off. It runs
// the rule's own firstLook and secondLook over every outcome.
func keepChance(cell, comp float64, compN int) (keep, offFirst float64) {
	first := binomPMF(KillFirstLook, cell)
	second := binomPMF(KillSecondLook-KillFirstLook, cell)
	cmp := binomPMF(compN, comp)
	for y1, p1 := range first {
		if firstLook(y1) {
			offFirst += p1
			continue
		}
		for y2, p2 := range second {
			for c, pc := range cmp {
				if secondLook(y1+y2, compN, c) == CellKeep {
					keep += p1 * p2 * pc
				}
			}
		}
	}
	return keep, offFirst
}

// The kill rule's operating characteristics, pinned by exact binomial sums (no
// seeds). Targets set by Fable when the rule was reworked (2026-11-14): a cell
// no better than its comparator is kept at most 10% of the time, and one three
// times as good at least 80% of the time at the base rates seen so far. The
// wants cell draws 5 a week, so its 40th answer comes in week 8, by when the
// random strata (15 scored and dropped places a week) have 120 answers; the
// yes-neighbour cell draws 3 a week against the deciles' 5, so 66 by its 40th.
// Below a 5% base rate the floor of 5 yes dominates and the rule has little
// power; that is accepted, and logged rather than asserted.
func TestKillRuleOperatingCharacteristics(t *testing.T) {
	for _, c := range []struct {
		name  string
		base  float64
		compN int
		check bool
	}{
		{"wants vs random", 0.03, 120, false},
		{"wants vs random", 0.05, 120, false},
		{"wants vs random", 0.09, 120, true},
		{"wants vs random", 0.15, 120, true},
		{"neighbour vs deciles", 0.20, 66, true},
	} {
		equal, offEqual := keepChance(c.base, c.base, c.compN)
		double, _ := keepChance(2*c.base, c.base, c.compN)
		triple, _ := keepChance(3*c.base, c.base, c.compN)
		t.Logf("%s at %.0f%%: keep %.1f%% at equal (off at the first look %.1f%%), %.1f%% at 2x, %.1f%% at 3x",
			c.name, 100*c.base, 100*equal, 100*offEqual, 100*double, 100*triple)
		if equal > 0.10 {
			t.Errorf("%s at %.0f%%: a cell no better than its comparator is kept %.1f%% of the time, want at most 10%%", c.name, 100*c.base, 100*equal)
		}
		if c.check && triple < 0.80 {
			t.Errorf("%s at %.0f%%: a cell three times as good is kept %.1f%% of the time, want at least 80%%", c.name, 100*c.base, 100*triple)
		}
	}
	// A cell that never finds a yes is always off at the first look.
	if keep, off := keepChance(0, 0.09, 120); keep != 0 || math.Abs(off-1) > 1e-12 {
		t.Errorf("a cell with no yes: keep %v off %v", keep, off)
	}
}
