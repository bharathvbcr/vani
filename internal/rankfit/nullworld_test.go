package rankfit

import (
	"fmt"
	"slices"
	"sync"
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
	saved := plantedShares
	plantedShares = SharesOf(jobinsight.DefaultWeights()).Array()

	// Every seed is its own parallel subtest (a Fit shares nothing with another
	// but the read-only planted shares, set before any of them starts). The
	// group subtest returns only when all seeds are done, so the shares are
	// restored after the last reader.
	var mu sync.Mutex
	var wins []uint64
	t.Run("seeds", func(t *testing.T) {
		for seed := uint64(1); seed <= nullSeeds; seed++ {
			t.Run(fmt.Sprintf("seed%02d", seed), func(t *testing.T) {
				t.Parallel()
				res, err := Fit(world(1000+seed, 1000, 300), jobinsight.DefaultWeights(), Options{Seed: seed})
				if err != nil {
					t.Fatalf("seed %d: %v", seed, err)
				}
				if res.Beats() {
					mu.Lock()
					wins = append(wins, seed)
					mu.Unlock()
				}
				t.Logf("seed %2d: n=%d fitted %.4f hand %.4f beats=%v", seed, res.HeldOutPairs, res.FitAccuracy, res.HandAccuracy, res.Beats())
			})
		}
	})
	plantedShares = saved
	if len(wins) > 0 {
		slices.Sort(wins)
		t.Errorf("%d of %d null worlds were declared a win (seeds %v)", len(wins), nullSeeds, wins)
	}
}
