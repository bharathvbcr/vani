package clusterstudy

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// One fit listed twice is one seed, not two: AdoptMinSeeds exists to stop that.
func TestBreakDuplicateSeedRunsAreRejected(t *testing.T) {
	run := `{"algo":"emeans_cpu","k":3,"seed":1,"assign":"a.assign","centroids":"a.cent","inertia_f64":1.5,"mse_f64":0.15,"non_empty":3,"labels_match_engine":true,"fit_ms":[1,2]}`
	dup := func(s string) string { return strings.Replace(s, run, run+","+run, 1) }
	if !strings.Contains(fitSummaryJSON(dup), run+","+run) {
		t.Fatal("test fixture did not repeat the run")
	}
	_, err := ReadFitSummary(writeSummary(t, fitSummaryJSON(dup)))
	if !errors.Is(err, ErrFormat) || !strings.Contains(err.Error(), "seed 1") {
		t.Fatalf("err = %v, want ErrFormat naming the duplicate seed", err)
	}
	// The same seed under another algorithm or k is a different run.
	other := strings.Replace(run, "emeans_cpu", "lloyd", 1)
	if _, err := ReadFitSummary(writeSummary(t, fitSummaryJSON(func(s string) string { return strings.Replace(s, run, run+","+other, 1) }))); err != nil {
		t.Errorf("same seed, other algorithm: %v", err)
	}
	otherK := strings.Replace(run, `"k":3`, `"k":4`, 1)
	if _, err := ReadFitSummary(writeSummary(t, fitSummaryJSON(func(s string) string { return strings.Replace(s, run, run+","+otherK, 1) }))); err != nil {
		t.Errorf("same seed, other k: %v", err)
	}
}

// Defence in depth: a spread that carries its seeds counts each once.
func TestBreakDecideCountsDistinctSeeds(t *testing.T) {
	ll := Spread(AlgoLloyd, 8, nil)
	same := Spread(AlgoEmeansCPU, 8, []float64{0.9, 0.9}).WithNullBand([]float64{0, 0}).WithSeedIDs([]uint64{1, 1})
	v := Decide(same, ll)
	if v.AdoptClusters || v.Seeds != 1 {
		t.Errorf("one seed twice: adopt %v, seeds %d", v.AdoptClusters, v.Seeds)
	}
	two := Spread(AlgoEmeansCPU, 8, []float64{0.9, 0.9}).WithNullBand([]float64{0, 0}).WithSeedIDs([]uint64{1, 2})
	if v := Decide(two, ll); !v.AdoptClusters || v.Seeds != 2 {
		t.Errorf("two seeds: adopt %v, seeds %d (%s)", v.AdoptClusters, v.Seeds, v.Reason)
	}
	// An undefined reduction is no seed even when its id is distinct.
	nanSeed := Spread(AlgoEmeansCPU, 8, []float64{0.9, math.NaN()}).WithNullBand([]float64{0, 0}).WithSeedIDs([]uint64{1, 2})
	if v := Decide(nanSeed, ll); v.AdoptClusters || v.Seeds != 1 {
		t.Errorf("one defined seed: adopt %v, seeds %d", v.AdoptClusters, v.Seeds)
	}
	// Ids that do not line up with the reductions cannot vouch for them.
	short := Spread(AlgoEmeansCPU, 8, []float64{0.9, 0.9}).WithNullBand([]float64{0, 0}).WithSeedIDs([]uint64{1})
	if Decide(short, ll).AdoptClusters {
		t.Error("seed ids of the wrong length adopted")
	}
}
