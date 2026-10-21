package rankfit

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// Two planted label models: the owner's would-apply read follows one set of
// shares among the postings the ranking scores and another among those the
// engine dropped.
var (
	scoredShares  = [NumParts]float64{0.05, 0.60, 0.05, 0.03, 0.10, 0.05, 0.12}
	droppedShares = [NumParts]float64{0.55, 0.05, 0.20, 0.03, 0.07, 0.05, 0.05}
)

// stratumWorld is n postings labelled by shares, keys prefixed by tag.
func stratumWorld(tag string, seed uint64, n int, shares [NumParts]float64) []Item {
	saved := plantedShares
	plantedShares = shares
	defer func() { plantedShares = saved }()
	items := world(seed, n, n)
	for i := range items {
		items[i].Key = fmt.Sprintf("%s:%05d", tag, i)
	}
	return items
}

// shareGap is the largest distance between two share sets, each normalised.
func shareGap(a, b Shares) float64 {
	worst := 0.0
	for k := range NumParts {
		worst = max(worst, math.Abs(share(a, k)-share(b, k)))
	}
	return worst
}

// sampleStrata draws each posting with its stratum's inclusion probability,
// from a generator with an explicit seed, and records the audit stratum and
// probability on the posting.
func sampleStrata(seed uint64, scored, dropped []Item, pScored, pDropped float64) []Item {
	rng := rand.New(rand.NewPCG(seed, 5))
	var out []Item
	for _, it := range scored {
		if rng.Float64() < pScored {
			it.Source, it.PSelect = "decile:5", pScored
			out = append(out, it)
		}
	}
	for _, it := range dropped {
		if rng.Float64() < pDropped {
			it.Source, it.PSelect = "dropped:title:role_fit", pDropped
			out = append(out, it)
		}
	}
	return out
}

// A3-2: the audit oversamples the dropped postings 10 to 1. The population's
// true shares are what a fit over the whole population finds; the
// Horvitz-Thompson weighted fit of the 10:1 sample recovers them, and the
// unweighted fit of the same sample does not (it learns the dropped stratum's
// shares). Both are pinned.
func TestWeightedFitRecoversPopulationSharesUnderOversampling(t *testing.T) {
	scored := stratumWorld("sc", 31, 3000, scoredShares)
	dropped := stratumWorld("dr", 32, 3000, droppedShares)
	base := jobinsight.DefaultWeights()

	pop := append(append([]Item(nil), scored...), dropped...)
	truth, err := Fit(pop, base, Options{Seed: 1, HoldoutPct: 1})
	if err != nil {
		t.Fatal(err)
	}

	sample := sampleStrata(7, scored, dropped, 0.1, 1.0)
	nScored := 0
	for _, it := range sample {
		if it.stratum() == StratumScored {
			nScored++
		}
	}
	if len(sample)-nScored < 8*nScored {
		t.Fatalf("sample has %d scored, %d dropped; not oversampled", nScored, len(sample)-nScored)
	}

	weighted, err := Fit(sample, base, Options{Seed: 1, HoldoutPct: 1})
	if err != nil {
		t.Fatal(err)
	}
	unweighted := append([]Item(nil), sample...)
	for i := range unweighted {
		unweighted[i].Source, unweighted[i].PSelect = "", 0 // forget the design
	}
	plain, err := Fit(unweighted, base, Options{Seed: 1, HoldoutPct: 1})
	if err != nil {
		t.Fatal(err)
	}
	gw, gu := shareGap(weighted.Shares, truth.Shares), shareGap(plain.Shares, truth.Shares)
	t.Logf("population %+v\nweighted gap %.4f %+v\nunweighted gap %.4f %+v", truth.Shares, gw, weighted.Shares, gu, plain.Shares)
	if gw > 0.08 {
		t.Errorf("weighted fit is %.4f from the population shares, want within 0.08", gw)
	}
	if gu < 0.15 {
		t.Errorf("unweighted fit is only %.4f from the population shares: the test no longer shows the bias", gu)
	}
	if gu < 3*gw {
		t.Errorf("unweighted gap %.4f is not clearly above the weighted gap %.4f", gu, gw)
	}
}
