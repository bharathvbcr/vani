package rankeval

import (
	"fmt"
	"math"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// sweepSeeds equals evalSeeds; the 135-variant sweep still runs in seconds, well
// inside two minutes.
const sweepSeeds = 24

type variant struct {
	w           jobinsight.Weights
	label       string
	mean, worst float64 // NDCG@10 averaged / minimum over scenario x utility cells
}

func (p *prepared) sweepScore(w jobinsight.Weights) (mean, worst float64) {
	r := scoreRanker("sweep", w)
	worst = math.Inf(1)
	cells := 0
	for si := range scenarios {
		for ui := range utilities {
			m := p.evaluate(r, si, ui).n10.mean
			mean += m
			worst = math.Min(worst, m)
			cells++
		}
	}
	return mean / float64(cells), worst
}

// TestVariantSweep ranks Weights variants by mean NDCG@10. It only runs with
// VANI_RANK_SWEEP=1 so the normal suite stays fast.
func TestVariantSweep(t *testing.T) {
	if os.Getenv("VANI_RANK_SWEEP") != "1" {
		t.Skip("set VANI_RANK_SWEEP=1 to run the variant sweep")
	}
	start := time.Now()
	p := prepare(sweepSeeds)
	base := jobinsight.DefaultWeights()

	var kept []variant
	discarded := 0
	for _, hl := range []float64{5, 7, 10, 14, 21} {
		for _, ff := range []float64{0.15, 0.25, 0.35} {
			for _, sem := range []float64{0.15, 0.25, 0.35} {
				for _, ft := range []float64{0.2, 0.3, 0.4} {
					w := base
					w.HalfLifeDays, w.FreshFloor, w.Semantic, w.Fit = hl, ff, sem, ft
					if v := invariantViolations(w); len(v) != 0 {
						discarded++
						continue
					}
					m, worst := p.sweepScore(w)
					kept = append(kept, variant{w: w, mean: m, worst: worst,
						label: fmt.Sprintf("halfLife=%g freshFloor=%.2f semantic=%.2f fit=%.1f", hl, ff, sem, ft)})
				}
			}
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].mean > kept[j].mean })

	dm, dworst := p.sweepScore(base)
	pos := 1
	for _, v := range kept {
		if v.mean > dm {
			pos++
		}
	}
	t.Logf("sweep: %d seeds, %d variants kept, %d discarded for invariant violations, %s", sweepSeeds, len(kept), discarded, time.Since(start).Round(time.Millisecond))
	for i, v := range kept[:min(10, len(kept))] {
		t.Logf("  #%-2d mean ndcg@10 %.4f  worst %.4f  %s", i+1, v.mean, v.worst, v.label)
	}
	total := len(kept)
	onGrid := false
	for _, v := range kept {
		onGrid = onGrid || v.w == base
	}
	if !onGrid {
		total++ // DefaultWeights is not one of the swept variants
	}
	t.Logf("DefaultWeights (halfLife=%g freshFloor=%.2f semantic=%.2f fit=%.2f): mean %.4f worst %.4f; ranks #%d of %d (on the swept grid: %v)",
		base.HalfLifeDays, base.FreshFloor, base.Semantic, base.Fit, dm, dworst, pos, total, onGrid)
	if el := time.Since(start); el > 2*time.Minute {
		t.Errorf("sweep took %s, want under 2m", el)
	}
}
