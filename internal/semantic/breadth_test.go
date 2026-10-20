package semantic

import (
	"math"
	"testing"
)

// The calibrated score used to clip at 1 (every posting at or above the
// on-domain anchor tied) and to ignore how much evidence a posting offered (two
// good lines scored as high as eight). Both are fixed in profileData.score.

func testProfile() *profileData { return &profileData{floor: 0.6, ceil: 0.8} }

func TestScoreKeepsOrderAboveTheCeiling(t *testing.T) {
	pd := testProfile()
	prev := -1.0
	for _, raw := range []float64{0.60, 0.70, 0.78, 0.80, 0.82, 0.85, 0.90, 1.0} {
		s := pd.score(raw, topChunks)
		if !(s > prev) {
			t.Errorf("raw %.2f scores %.4f, not above the previous %.4f: order lost", raw, s, prev)
		}
		if s < 0 || s >= 1 {
			t.Errorf("raw %.2f scores %.4f, want [0,1)", raw, s)
		}
		prev = s
	}
	// Below the knee the score is the plain affine map between floor and ceiling.
	if got, want := pd.score(0.70, topChunks), (0.70-0.6)/(0.8-0.6); math.Abs(got-want) > 1e-9 {
		t.Errorf("below the knee the score is the affine value: %.4f vs %.4f", got, want)
	}
}

func TestScoreDiscountsThinEvidenceButNotFullEvidence(t *testing.T) {
	pd := testProfile()
	full := pd.score(0.72, topChunks)
	if want := (0.72 - 0.6) / (0.8 - 0.6); math.Abs(full-want) > 1e-9 {
		t.Errorf("a posting with a full set of distinct chunks is not discounted: %.4f vs %.4f", full, want)
	}
	if more := pd.score(0.72, 3*topChunks); more != full {
		t.Errorf("more chunks than the cap cannot raise the score: %.4f vs %.4f", more, full)
	}
	prev := full
	for n := topChunks - 1; n >= 1; n-- {
		s := pd.score(0.72, n)
		if !(s < prev) {
			t.Errorf("%d chunks score %.4f, not below %d chunks' %.4f", n, s, n+1, prev)
		}
		prev = s
	}
	// Two chunks at the ceiling do not read as a posting that is all about the
	// owner's work.
	if two, all := pd.score(0.80, 2), pd.score(0.80, topChunks); !(two < 0.7*all) {
		t.Errorf("two chunks at the ceiling score %.3f against %.3f for a full set", two, all)
	}
}

func TestScoreHostileInputs(t *testing.T) {
	pd := testProfile()
	for name, raw := range map[string]float64{"nan": math.NaN(), "inf": math.Inf(1), "-inf": math.Inf(-1), "negative": -3} {
		if s := pd.score(raw, topChunks); s != 0 && !(raw > 0 && math.IsInf(raw, 1)) {
			t.Errorf("%s: score %v, want 0", name, s)
		}
	}
	for _, n := range []int{-5, 0} {
		if s := pd.score(0.9, n); s != 0 {
			t.Errorf("%d chunks: score %v, want 0 (no evidence)", n, s)
		}
	}
	if s := pd.score(math.Inf(1), topChunks); math.IsNaN(s) || s < 0 || s > 1 {
		t.Errorf("an infinite raw scored %v", s)
	}
}
