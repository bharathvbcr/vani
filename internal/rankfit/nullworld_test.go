package rankfit

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// nullSeeds is how many independent null worlds the gate is held to.
const nullSeeds = 20

// A null world: the labels follow the HAND weights (plus noise), so the fitted
// shares have nothing real to find and any held-out edge is chance. The gate
// must not declare a win for any of the seeds (A3-1: it used to be a bare
// FitAccuracy > HandAccuracy, which is true for about half of them).
func TestNullWorldNeverBeatsTheHandWeights(t *testing.T) {
	t.Parallel()
	hand := SharesOf(jobinsight.DefaultWeights()).Array()
	var wins []uint64
	for seed := uint64(1); seed <= nullSeeds; seed++ {
		res, err := Fit(worldWith(hand, 1000+seed, 1000, 300), jobinsight.DefaultWeights(), Options{Seed: seed})
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if res.Beats() {
			wins = append(wins, seed)
		}
		t.Logf("seed %2d: n=%d fitted %.4f hand %.4f beats=%v", seed, res.HeldOutPairs, res.FitAccuracy, res.HandAccuracy, res.Beats())
	}
	if len(wins) > 0 {
		t.Errorf("%d of %d null worlds were declared a win (seeds %v)", len(wins), nullSeeds, wins)
	}
}
