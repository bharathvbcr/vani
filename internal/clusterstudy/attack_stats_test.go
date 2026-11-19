package clusterstudy

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// Attack: boundary, non-finite and JSON behaviour of the adopt verdict.

func TestAttackDecideNonFiniteReductionsAreNotSeeds(t *testing.T) {
	ll := Spread(AlgoLloyd, 8, nil)
	inf, nan := math.Inf(1), math.NaN()
	cases := map[string][]float64{
		"+Inf and one finite": {inf, 0.9},
		"-Inf and one finite": {math.Inf(-1), 0.9},
		"NaN and one finite":  {nan, 0.9},
		"all NaN":             {nan, nan},
		"all +Inf":            {inf, inf},
	}
	for name, r := range cases {
		hi := make([]float64, len(r))
		em := Spread(AlgoEmeansCPU, 8, r).WithNullBand(hi)
		v := Decide(em, ll)
		if v.AdoptClusters {
			t.Errorf("%s adopted: %+v", name, v)
		}
		if _, err := json.Marshal(v); err != nil {
			t.Errorf("%s: verdict does not encode: %v", name, err)
		}
	}
}

func TestAttackDecideNonFiniteNullBands(t *testing.T) {
	ll := Spread(AlgoLloyd, 8, nil)
	for name, hi := range map[string][]float64{
		"NaN band":  {math.NaN(), math.NaN()},
		"+Inf band": {math.Inf(1), math.Inf(1)},
		"mixed":     {0.01, math.NaN()},
	} {
		v := Decide(Spread(AlgoEmeansCPU, 8, []float64{0.9, 0.9}).WithNullBand(hi), ll)
		if v.AdoptClusters {
			t.Errorf("%s adopted: %+v", name, v)
		}
	}
	// Extra band entries beyond the reductions are an unavailable band too.
	if Decide(Spread(AlgoEmeansCPU, 8, []float64{0.9, 0.9}).WithNullBand([]float64{0, 0, 0}), ll).AdoptClusters {
		t.Error("a longer band adopted")
	}
}

func TestAttackVerdictAndSpreadJSONStayValid(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	em := Spread(AlgoEmeansCPU, 8, []float64{nan, inf, 0.5}).WithNullBand([]float64{nan, inf, math.Inf(-1)})
	v := Decide(em, Spread(AlgoLloyd, 8, []float64{nan}))
	b, err := json.Marshal(v)
	if err != nil || !json.Valid(b) {
		t.Fatalf("invalid json %v: %s", err, b)
	}
	var back Verdict
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if back.AdoptClusters || back.Seeds != 1 {
		t.Errorf("round trip changed the verdict: %+v", back)
	}
	// A Compared with NaN everywhere encodes, null for the undefined share.
	c := Compared{NullAtOrAbove: Num(nan), Reduction: Num(nan)}
	cb, err := json.Marshal(c)
	if err != nil || !strings.Contains(string(cb), "\"null_at_or_above\":null") {
		t.Errorf("compared json %v: %s", err, cb)
	}
}

// Two seeds whose median is exactly the bar: the lerp must not land under it.
func TestAttackDecideAtTheBarWithTwoSeeds(t *testing.T) {
	for _, pair := range [][2]float64{{0.2, 0.3}, {0.1, 0.4}, {0.25, 0.25}, {0.15, 0.35}, {0.05, 0.45}} {
		em := Spread(AlgoEmeansCPU, 8, pair[:]).WithNullBand([]float64{0, 0})
		if float64(em.Median) < AdoptReduction {
			t.Errorf("median of %v is %v (under the bar %v by %g)", pair, float64(em.Median), AdoptReduction, AdoptReduction-float64(em.Median))
		}
	}
}

func TestAttackNullHiNeverPanicsOnEmptyBands(t *testing.T) {
	var s SeedSpread
	if v := Decide(s, s); v.AdoptClusters {
		t.Errorf("zero spreads adopted: %+v", v)
	}
	if v := Decide(Spread(AlgoEmeansCPU, 8, nil).WithNullBand(nil), Spread(AlgoLloyd, 8, nil)); v.AdoptClusters {
		t.Errorf("no seeds adopted: %+v", v)
	}
}
