package rankfit

import (
	"bytes"
	"encoding/json"
	"math"
	"math/rand/v2"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

func resultFinite(r FitResult) bool {
	ok := finite(r.FitAccuracy) && finite(r.HandAccuracy) && finite(r.GainLo) && finite(r.GainHi)
	for _, s := range r.Strata {
		ok = ok && finite(s.FitAccuracy) && finite(s.HandAccuracy)
	}
	return ok
}

// Attack 1: the same input and seed give byte-identical results across runs
// and across GOMAXPROCS 1 and 8. Eight runs alternate the two settings.
// The check is equality, so the first mismatch fails the test; the count is
// not a sample size the verdict depends on. This test stays sequential
// because GOMAXPROCS is process-wide.
func TestAttackDeterminismAcrossRunsAndProcs(t *testing.T) {
	scored := stratumWorld("sc", 31, 500, scoredShares)
	dropped := stratumWorld("dr", 32, 500, droppedShares)
	items := sampleStrata(7, scored, dropped, 0.2, 1.0)
	var first []byte
	prev := runtime.GOMAXPROCS(0)
	defer runtime.GOMAXPROCS(prev)
	for run := range 8 {
		runtime.GOMAXPROCS([]int{1, 8}[run%2])
		res, err := Fit(append([]Item(nil), items...), jobinsight.DefaultWeights(), Options{Seed: 5})
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = b
		} else if !bytes.Equal(first, b) {
			t.Fatalf("run %d differs:\n%s\n%s", run, first, b)
		}
	}
}

// Attack 2: gain exactly 0 in every resample (fitted shares == hand shares)
// must not beat, whatever the n.
func TestAttackZeroGainEverywhereNeverBeats(t *testing.T) {
	t.Parallel()
	items := world(3, 1500, 1500)
	base := jobinsight.DefaultWeights()
	hand := SharesOf(base).Array()
	var testItems []int
	by := grades{}
	for i, it := range items {
		if i%3 == 0 {
			by[it.Grade] = append(by[it.Grade], i)
			testItems = append(testItems, i)
		}
	}
	test := by.sample(1<<30, 0)
	ev, err := evaluateHeldOut(items, testItems, test, blendOf(base), hand, hand, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ev.gainLo != 0 || ev.gainHi != 0 {
		t.Fatalf("identical shares gave interval [%v, %v]", ev.gainLo, ev.gainHi)
	}
	r := FitResult{HeldOutPairs: len(test), GainLo: ev.gainLo, GainHi: ev.gainHi, Resamples: ev.bootstrapSamples}
	if r.Beats() {
		t.Fatal("zero gain beats")
	}
}

// Attack 2: 1-eps and 1 are fine.
func TestAttackPSelectJustBelowOne(t *testing.T) {
	t.Parallel()
	items := world(11, 600, 300)
	for i := range items {
		items[i].Source, items[i].PSelect = "decile:5", math.Nextafter(1, 0)
	}
	res, err := Fit(items, jobinsight.DefaultWeights(), Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !resultFinite(res) {
		t.Fatalf("non-finite: %+v", res)
	}
}

// Attack 3: coverage of the 95% interval for the gain, over 100 seeds. The
// estimand is the population gain of the FITTED shares over the hand shares:
// pair accuracy over every cross-grade pair of a large independent world drawn
// from the same label model.
func TestAttackIntervalCoverage(t *testing.T) {
	if os.Getenv("VANI_ATTACK_SLOW") == "" {
		t.Skip("slow; set VANI_ATTACK_SLOW=1")
	}
	base := jobinsight.DefaultWeights()
	b := blendOf(base)
	hand := SharesOf(base).Array()
	pop := world(777777, 6000, 6000)
	truth := func(sh [NumParts]float64) float64 {
		var byG grades
		for i, it := range pop {
			byG[it.Grade] = append(byG[it.Grade], i)
		}
		u, uh := make([]float64, len(pop)), make([]float64, len(pop))
		for i := range pop {
			u[i] = utility(&pop[i], b, &sh, 1, 1)
			uh[i] = utility(&pop[i], b, &hand, 1, 1)
		}
		var g, n float64
		for _, gp := range gradePairs {
			for _, w := range byG[gp[0]] {
				for _, l := range byG[gp[1]] {
					g += credit(u[w]-u[l]) - credit(uh[w]-uh[l])
					n++
				}
			}
		}
		return g / n
	}
	covered, runs := 0, 100
	var widths float64
	start := time.Now()
	for s := range runs {
		items := world(5000+uint64(s), 1200, 400)
		res, err := Fit(items, base, Options{Seed: uint64(s + 1)})
		if err != nil {
			t.Fatalf("seed %d: %v", s, err)
		}
		tg := truth(res.Shares.Array())
		if res.GainLo <= tg && tg <= res.GainHi {
			covered++
		}
		widths += res.GainHi - res.GainLo
	}
	t.Logf("COVERAGE %d/%d, mean width %.4f, %v", covered, runs, widths/float64(runs), time.Since(start))
	if covered < 85 {
		t.Errorf("coverage %d/%d below 85", covered, runs)
	}
}

// Attack 8: time and memory of the resampling at the held-out pair cap.
func TestAttackBootstrapAtMaxPairsTime(t *testing.T) {
	if os.Getenv("VANI_ATTACK_SLOW") == "" {
		t.Skip("slow; set VANI_ATTACK_SLOW=1")
	}
	// ~2,000,000 pairs: 3 grades of ~ 365 items each give 3*365*365 ~ 400k; use
	// 800 per grade: 3*640k = 1.92M.
	rng := rand.New(rand.NewPCG(1, 2))
	per := 800
	n := 3 * per
	items := make([]Item, n)
	var by grades
	var testItems []int
	for i := range items {
		g := i / per
		it := Item{Gate: 0.5 + 0.5*rng.Float64(), Fresh: 0.5 + 0.5*rng.Float64(), Grade: g, Judged: true}
		for k := range NumParts {
			it.Present[k], it.Known[k], it.Conf[k], it.Value[k] = true, true, 1, rng.Float64()
		}
		items[i] = it
		by[g] = append(by[g], i)
		testItems = append(testItems, i)
	}
	test := by.sample(1<<30, 0)
	if len(test) > MaxHeldOutPairs {
		t.Fatalf("setup %d pairs", len(test))
	}
	base := jobinsight.DefaultWeights()
	fitted := SharesOf(base).Array()
	fitted[PartFit] *= 3
	var ms0, ms1 runtime.MemStats
	runtime.ReadMemStats(&ms0)
	start := time.Now()
	ev, err := evaluateHeldOut(items, testItems, test, blendOf(base), fitted, SharesOf(base).Array(), 1)
	if err != nil {
		t.Fatal(err)
	}
	el := time.Since(start)
	runtime.ReadMemStats(&ms1)
	t.Logf("TIMING %d pairs, %v, alloc %d MB, interval [%.4f, %.4f]", len(test), el, (ms1.TotalAlloc-ms0.TotalAlloc)>>20, ev.gainLo, ev.gainHi)
	if el > 10*time.Second {
		t.Errorf("bootstrap took %v at %d pairs", el, len(test))
	}
}

// Attack 2: held-out pairs that are all ties give accuracy 0.5 both ways, gain
// 0, not a win.
func TestAttackAllTiedPairs(t *testing.T) {
	t.Parallel()
	n := 60
	items := make([]Item, n)
	var by grades
	var testItems []int
	for i := range items {
		it := Item{Gate: 1, Fresh: 1, Grade: i % 3, Judged: true}
		for k := range NumParts {
			it.Present[k], it.Known[k], it.Conf[k], it.Value[k] = true, true, 1, 0.5
		}
		items[i] = it
		by[it.Grade] = append(by[it.Grade], i)
		testItems = append(testItems, i)
	}
	test := by.sample(1<<30, 0)
	base := jobinsight.DefaultWeights()
	h := SharesOf(base).Array()
	f := h
	f[PartFit] *= 2
	ev, err := evaluateHeldOut(items, testItems, test, blendOf(base), f, h, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !resultFinite(FitResult{FitAccuracy: ev.fit, HandAccuracy: ev.hand, GainLo: ev.gainLo, GainHi: ev.gainHi, Strata: ev.strata}) {
		t.Fatalf("non-finite %+v", ev)
	}
	if ev.fit != 0.5 || ev.hand != 0.5 || ev.gainLo != 0 || ev.gainHi != 0 {
		t.Fatalf("ties: %+v", ev)
	}
}
