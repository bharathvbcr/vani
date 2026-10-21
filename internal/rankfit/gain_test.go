package rankfit

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A planted gain: the result carries the held-out n and an interval that
// brackets the point gain and clears 0.
func TestPlantedGainCarriesNAndInterval(t *testing.T) {
	t.Parallel()
	res, err := Fit(world(1, 1200, 400), jobinsight.DefaultWeights(), Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	gain := res.FitAccuracy - res.HandAccuracy
	t.Logf("n=%d gain %.4f interval [%.4f, %.4f] over %d resamples", res.HeldOutPairs, gain, res.GainLo, res.GainHi, res.Resamples)
	if !res.Beats() {
		t.Fatalf("a clear planted gain is not a win: %s", res.NoGainReason())
	}
	if res.HeldOutPairs < MinHeldOutPairs || res.Resamples != BootstrapResamples {
		t.Errorf("n %d, resamples %d", res.HeldOutPairs, res.Resamples)
	}
	if !(res.GainLo > 0) || !(res.GainLo <= gain && gain <= res.GainHi) {
		t.Errorf("interval [%.4f, %.4f] does not clear 0 and hold the gain %.4f", res.GainLo, res.GainHi, gain)
	}
}

// The gate's own boundaries, on a hand-built result.
func TestBeatsBoundaries(t *testing.T) {
	t.Parallel()
	ok := FitResult{HeldOutPairs: MinHeldOutPairs, GainLo: 0.001, GainHi: 0.05, Resamples: BootstrapResamples}
	if !ok.Beats() || ok.NoGainReason() != "" {
		t.Fatalf("baseline should win: %s", ok.NoGainReason())
	}
	for name, mutate := range map[string]func(*FitResult){
		"n one short":         func(r *FitResult) { r.HeldOutPairs = MinHeldOutPairs - 1 },
		"lower end exactly 0": func(r *FitResult) { r.GainLo = 0 },
		"lower end negative":  func(r *FitResult) { r.GainLo = -0.001 },
		"lower end NaN":       func(r *FitResult) { r.GainLo = math.NaN() },
		"never evaluated":     func(r *FitResult) { r.Resamples = 0 },
		"scored one short": func(r *FitResult) {
			r.Stratified, r.ScoredPairs = true, MinScoredStratumPairs-1
		},
	} {
		r := ok
		mutate(&r)
		if r.Beats() || r.NoGainReason() == "" {
			t.Errorf("%s: still beats", name)
		}
	}
	atFloor := ok
	atFloor.Stratified, atFloor.ScoredPairs = true, MinScoredStratumPairs
	if !atFloor.Beats() {
		t.Errorf("scored pairs at the floor: %s", atFloor.NoGainReason())
	}
	if (FitResult{}).Beats() {
		t.Error("the zero FitResult beats")
	}
}

// The strata the audit draws (scored deciles, dropped classes) are reported
// apart, and a held-out set with no scored pair cannot win, however well the
// dropped pairs go.
func TestHeldOutAccuracyIsReportedPerStratum(t *testing.T) {
	t.Parallel()
	scored := stratumWorld("sc", 31, 3000, scoredShares)
	dropped := stratumWorld("dr", 32, 3000, droppedShares)
	sample := sampleStrata(7, scored, dropped, 0.1, 1.0)
	res, err := Fit(sample, jobinsight.DefaultWeights(), Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]StratumAccuracy{}
	total := 0
	for _, s := range res.Strata {
		got[s.Stratum] = s
		total += s.Pairs
	}
	for _, name := range []string{StratumScored, StratumDropped, "mixed"} {
		if got[name].Pairs == 0 {
			t.Errorf("no %s pairs in %+v", name, res.Strata)
		}
	}
	if total != res.HeldOutPairs || !res.Stratified || res.ScoredPairs != got[StratumScored].Pairs {
		t.Errorf("strata hold %d pairs, scored %d; result %d pairs, scored %d, stratified %v", total, got[StratumScored].Pairs, res.HeldOutPairs, res.ScoredPairs, res.Stratified)
	}

	onlyDropped := stratumWorld("d1", 41, 1500, droppedShares)
	for i := range onlyDropped {
		onlyDropped[i].Source, onlyDropped[i].PSelect = "dropped:title:role_fit", 0.5
	}
	one, err := Fit(onlyDropped, jobinsight.DefaultWeights(), Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if one.ScoredPairs != 0 || len(one.Strata) != 1 || one.Strata[0].Stratum != StratumDropped {
		t.Fatalf("single stratum: %+v", one.Strata)
	}
	if one.Beats() || !strings.Contains(one.NoGainReason(), "scored-stratum") {
		t.Errorf("a held-out set of dropped postings alone won: %s", one.NoGainReason())
	}
}

// PSelect is validated where it enters: Targets and Fit refuse NaN, 0,
// negative, infinite and above 1, and take 1 and a tiny positive value.
func TestPSelectIsValidatedAtTheBoundary(t *testing.T) {
	t.Parallel()
	bad := map[string]float64{"NaN": math.NaN(), "zero": 0, "negative": -0.1, "above one": 1.0000001, "+Inf": math.Inf(1), "-Inf": math.Inf(-1)}
	for name, p := range bad {
		labels := map[posting.Key]eval.Entry{"gh:a:x": {Labels: map[string]string{eval.WouldApply: "yes"}, Source: "decile:3", PSelect: p}}
		if _, _, err := Targets(labels, nil, nil); err == nil || !strings.Contains(err.Error(), "p_select") {
			t.Errorf("Targets accepted %s: %v", name, err)
		}
		w := world(10, 400, 200)
		w[3].Source, w[3].PSelect = "decile:3", p
		if _, err := Fit(w, jobinsight.DefaultWeights(), Options{Seed: 1}); err == nil || errors.As(err, new(*NotRunError)) {
			t.Errorf("Fit accepted %s: %v", name, err)
		}
	}
	for name, p := range map[string]float64{"one": 1, "tiny": 1e-9, "half": 0.5} {
		labels := map[posting.Key]eval.Entry{"gh:a:x": {Labels: map[string]string{eval.WouldApply: "yes"}, Source: "dropped:t:role_fit", PSelect: p}}
		got, _, err := Targets(labels, nil, nil)
		if err != nil || got["gh:a:x"].PSelect != p || got["gh:a:x"].Source != "dropped:t:role_fit" {
			t.Errorf("Targets refused or lost %s: %+v, %v", name, got, err)
		}
		w := world(10, 400, 200)
		w[3].Source, w[3].PSelect = "decile:3", p
		if _, err := Fit(w, jobinsight.DefaultWeights(), Options{Seed: 1}); err != nil {
			t.Errorf("Fit refused %s: %v", name, err)
		}
	}
	// A manual label and a mark carry no probability, and weigh 1.
	labels := map[posting.Key]eval.Entry{"gh:a:m": {Labels: map[string]string{eval.WouldApply: "no"}, Source: eval.SourceManual}}
	if got, _, err := Targets(labels, nil, nil); err != nil || got["gh:a:m"].PSelect != 0 {
		t.Errorf("manual label: %+v, %v", got, err)
	}
	w := world(10, 400, 200)
	w[3].PSelect = 0.5 // a probability with no stratum
	if _, err := Fit(w, jobinsight.DefaultWeights(), Options{Seed: 1}); err == nil {
		t.Error("a p_select without a sampling stratum was accepted")
	}
	if it := (&Item{Source: "decile:1", PSelect: 0.25}); it.weight() != 4 {
		t.Errorf("weight %v, want 4", it.weight())
	}
	if it := (&Item{}); it.weight() != 1 {
		t.Errorf("unsampled weight %v, want 1", it.weight())
	}
}

// Adversarial held-out sets: all ties, nothing, and too many pairs.
func TestHeldOutAdversarialInputs(t *testing.T) {
	t.Parallel()
	base := jobinsight.DefaultWeights()

	// Every posting scores the same: both share sets tie every pair, the gain
	// is exactly 0, and it cannot win.
	tied := world(20, 600, 300)
	for i := range tied {
		tied[i].Value, tied[i].Known, tied[i].Present, tied[i].Conf = [NumParts]float64{}, [NumParts]bool{}, [NumParts]bool{}, [NumParts]float64{}
		tied[i].Gate, tied[i].Fresh = 1, 1
		for k := range NumParts {
			tied[i].Value[k], tied[i].Present[k] = 0.5, k != PartKBMatch
		}
	}
	res, err := Fit(tied, base, Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Beats() || res.FitAccuracy != 0.5 || res.HandAccuracy != 0.5 || res.GainLo != 0 || res.GainHi != 0 {
		t.Errorf("all ties: fitted %.4f hand %.4f interval [%v, %v] beats %v", res.FitAccuracy, res.HandAccuracy, res.GainLo, res.GainHi, res.Beats())
	}

	// No held-out pairs at all.
	if ev, err := evaluateHeldOut(nil, nil, nil, blendOf(base), [NumParts]float64{}, [NumParts]float64{}, 1); err != nil || ev.pairs != 0 || ev.bootstrapSamples != 0 {
		t.Errorf("empty held-out set: %+v", ev)
	}

	// Beyond MaxHeldOutPairs the fit refuses rather than score or resample a
	// sampled subset.
	big := world(21, 4000, 4000)
	if _, err := Fit(big, base, Options{Seed: 1, HoldoutPct: 90}); err == nil || !strings.Contains(err.Error(), "held-out pairs") {
		t.Errorf("oversized held-out set: %v", err)
	}
}

// The interval is a function of the seed: the same call twice agrees.
func TestGainIntervalIsDeterministic(t *testing.T) {
	t.Parallel()
	items := world(3, 600, 200)
	a, err := Fit(items, jobinsight.DefaultWeights(), Options{Seed: 9})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Fit(items, jobinsight.DefaultWeights(), Options{Seed: 9})
	if a.GainLo != b.GainLo || a.GainHi != b.GainHi {
		t.Fatalf("same seed, different interval: [%v, %v] vs [%v, %v]", a.GainLo, a.GainHi, b.GainLo, b.GainHi)
	}
	if !(a.GainLo < a.GainHi) {
		t.Errorf("degenerate interval [%v, %v]", a.GainLo, a.GainHi)
	}
}
