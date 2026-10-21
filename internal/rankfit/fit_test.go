package rankfit

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// world draws n postings whose labels follow plantedShares, not the hand
// weights: coverage carries the blend. judged of them carry an audit
// judgement; the rest are marks.
var plantedShares = [NumParts]float64{0.05, 0.60, 0.05, 0.03, 0.10, 0.05, 0.12}

func world(seed uint64, n, judged int) []Item {
	rng := rand.New(rand.NewPCG(seed, 99))
	b := blendOf(jobinsight.DefaultWeights())
	items := make([]Item, n)
	u := make([]float64, n)
	for i := range items {
		it := Item{Key: fmt.Sprintf("gh:w:%05d", i), Gate: 0.3 + 0.7*rng.Float64(), Fresh: 0.25 + 0.75*rng.Float64(), Judged: i < judged}
		for k := range NumParts {
			it.Present[k] = k != PartKBMatch || rng.IntN(3) == 0
			it.Known[k] = it.Present[k] && rng.IntN(5) > 0
			it.Value[k] = rng.Float64()
			if it.Known[k] {
				it.Conf[k] = 1
			} else {
				it.Value[k] = b.prior
			}
		}
		it.Value[PartEmployer] = float64(rng.IntN(5) / 4)
		it.Known[PartEmployer] = it.Value[PartEmployer] == 1
		it.Conf[PartEmployer] = 1 // ScoreWith always weighs the employer part
		items[i] = it
		s := plantedShares
		u[i] = utility(&items[i], b, &s, 1, 1) + 0.15*math.Log(1/rng.Float64()-1) // logistic noise
	}
	sorted := append([]float64(nil), u...)
	slicesSort(sorted)
	low, high := sorted[n/3], sorted[2*n/3]
	for i := range items {
		switch {
		case u[i] > high:
			items[i].Grade = GradeYes
		case u[i] > low:
			items[i].Grade = GradeMaybe
		}
	}
	return items
}

func slicesSort(v []float64) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

func share(s Shares, k int) float64 {
	a := s.Array()
	sum := 0.0
	for _, v := range a {
		sum += v
	}
	return a[k] / sum
}

// Labels drawn from coverage-heavy shares: the fit must move toward coverage
// and order the held-out judged pairs better than the hand weights do. This is
// Phase 5's exit test, asserted, on a world where it is true by construction.
func TestFitRecoversPlantedShares(t *testing.T) {
	items := world(1, 1200, 400)
	res, err := Fit(items, jobinsight.DefaultWeights(), Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("held-out pairs %d: fitted %.4f, hand %.4f; coverage share %.3f -> %.3f; semantic %.3f -> %.3f; tau %.2f; gate exp %.2f fresh exp %.2f; %d iterations",
		res.HeldOutPairs, res.FitAccuracy, res.HandAccuracy, share(res.Hand, PartCoverage), share(res.Shares, PartCoverage),
		share(res.Hand, PartSemantic), share(res.Shares, PartSemantic), res.Temperature, res.GateExp, res.FreshExp, res.Iterations)
	if !res.Converged || !res.DiagConverged {
		t.Errorf("did not converge (main %v in %d iterations, diagnostic %v)", res.Converged, res.Iterations, res.DiagConverged)
	}
	if !res.Beats() || res.FitAccuracy-res.HandAccuracy < 0.02 {
		t.Errorf("fitted accuracy %.4f does not clearly beat hand %.4f", res.FitAccuracy, res.HandAccuracy)
	}
	if !(share(res.Shares, PartCoverage) > share(res.Hand, PartCoverage)+0.1) || !(share(res.Shares, PartSemantic) < share(res.Hand, PartSemantic)) {
		t.Errorf("shares did not move toward the planted ones: hand %+v fitted %+v", res.Hand, res.Shares)
	}
	if res.Judged != 400 || res.HeldOutItems == 0 || res.HeldOutItems > 400 || res.TrainItems != 1200-res.HeldOutItems {
		t.Errorf("counts: judged %d, held out %d, trained %d", res.Judged, res.HeldOutItems, res.TrainItems)
	}
	// The diagnostic exponents see that gate and freshness enter as Score
	// has them (the world used exponent 1 for both).
	if math.Abs(res.GateExp-1) > 0.35 || math.Abs(res.FreshExp-1) > 0.35 {
		t.Errorf("diagnostic exponents gate %.3f fresh %.3f, want near 1", res.GateExp, res.FreshExp)
	}
}

// A world labelled by the hand weights themselves: the prior is right, so
// the fit must not wander off it.
func TestFitStaysNearHandWhenHandIsRight(t *testing.T) {
	saved := plantedShares
	plantedShares = SharesOf(jobinsight.DefaultWeights()).Array()
	defer func() { plantedShares = saved }()
	res, err := Fit(world(2, 1000, 300), jobinsight.DefaultWeights(), Options{Seed: 2})
	if err != nil {
		t.Fatal(err)
	}
	for k := range NumParts {
		if d := math.Abs(share(res.Shares, k) - share(res.Hand, k)); d > 0.08 {
			t.Errorf("%s moved %.3f from the hand share", PartNames[k], d)
		}
	}
	if res.FitAccuracy < res.HandAccuracy-0.03 {
		t.Errorf("fitted %.4f fell well below hand %.4f on hand-labelled data", res.FitAccuracy, res.HandAccuracy)
	}
}

func TestFitIsDeterministic(t *testing.T) {
	items := world(3, 600, 200)
	a, err := Fit(items, jobinsight.DefaultWeights(), Options{Seed: 9})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Fit(items, jobinsight.DefaultWeights(), Options{Seed: 9})
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("same seed, different results:\n%+v\n%+v", a, b)
	}
	// The split hangs on the keys, not the order: reversed items reach the
	// same optimum.
	rev := make([]Item, len(items))
	for i := range items {
		rev[len(items)-1-i] = items[i]
	}
	c, err := Fit(rev, jobinsight.DefaultWeights(), Options{Seed: 9})
	if err != nil {
		t.Fatal(err)
	}
	if c.HeldOutItems != a.HeldOutItems || c.HeldOutPairs != a.HeldOutPairs || c.FitAccuracy != a.FitAccuracy {
		t.Errorf("reversed order changed the split: %+v vs %+v", c, a)
	}
	for k, v := range a.Shares.Array() {
		if d := math.Abs(v - c.Shares.Array()[k]); d > 1e-6*max(1, v) {
			t.Errorf("reversed order moved %s by %g", PartNames[k], d)
		}
	}
	// Another seed holds out other keys.
	d, err := Fit(items, jobinsight.DefaultWeights(), Options{Seed: 10})
	if err != nil {
		t.Fatal(err)
	}
	if d.HeldOutItems == a.HeldOutItems && d.FitAccuracy == a.FitAccuracy && d.Shares == a.Shares {
		t.Errorf("seed 10 gave seed 9's split and fit")
	}
}

func TestFitRefusals(t *testing.T) {
	base := jobinsight.DefaultWeights()
	short := world(4, 3000, MinJudged-1) // marks do not count toward the threshold
	allYes := world(5, 400, 200)
	for i := range allYes {
		if allYes[i].Judged {
			allYes[i].Grade = GradeYes
		}
	}
	for name, tc := range map[string]struct {
		items []Item
		need  int
	}{
		"no items":            {nil, MinJudged},
		"149 judged":          {short, MinJudged},
		"judged all yes":      {allYes, 0},
		"one judged only no":  {setAll(world(7, 200, 200), GradeNo), 0},
		"one item":            {world(8, 1, 1), MinJudged},
		"all marks, no judge": {world(9, 5000, 0), MinJudged},
	} {
		_, err := Fit(tc.items, base, Options{Seed: 1})
		var nr *NotRunError
		if !errors.As(err, &nr) {
			t.Errorf("%s: %v, want a NotRunError", name, err)
			continue
		}
		if nr.Need != tc.need || !strings.HasPrefix(err.Error(), "could not run: weight fit") {
			t.Errorf("%s: %q (need %d), want need %d", name, err, nr.Need, tc.need)
		}
	}
}

func setAll(items []Item, grade int) []Item {
	for i := range items {
		items[i].Grade = grade
	}
	return items
}

func TestFitRejectsBadItems(t *testing.T) {
	base := jobinsight.DefaultWeights()
	for name, mutate := range map[string]func([]Item){
		"duplicate key": func(w []Item) { w[5].Key = w[4].Key },
		"NaN value":     func(w []Item) { w[3].Value[PartSemantic] = math.NaN() },
		"value above 1": func(w []Item) { w[3].Value[PartCoverage] = 1.0001 },
		"negative gate": func(w []Item) { w[7].Gate = -0.1 },
		"NaN fresh":     func(w []Item) { w[8].Fresh = math.NaN() },
		"inf value":     func(w []Item) { w[9].Value[PartFit] = math.Inf(1) },
		"grade 3":       func(w []Item) { w[10].Grade = 3 },
		"grade -1":      func(w []Item) { w[11].Grade = -1 },
	} {
		w := world(10, 400, 200)
		mutate(w)
		_, err := Fit(w, base, Options{Seed: 1})
		var nr *NotRunError
		if err == nil || errors.As(err, &nr) {
			t.Errorf("%s: %v, want a data error", name, err)
		}
	}
}

func TestFitBounds(t *testing.T) {
	base := jobinsight.DefaultWeights()
	w := world(11, 400, 200)
	for name, opt := range map[string]Options{
		"holdout 95":     {HoldoutPct: 95},
		"holdout -1":     {HoldoutPct: -1},
		"max pairs -1":   {MaxPairs: -1},
		"max pairs over": {MaxPairs: MaxPairsLimit + 1},
	} {
		if _, err := Fit(w, base, opt); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Fit(make([]Item, MaxItems+1), base, Options{}); err == nil || !strings.Contains(err.Error(), "max") {
		t.Errorf("oversized item set: %v", err)
	}

	// Above the pair cap the pairs are sampled, and the report says so.
	res, err := Fit(world(12, 2000, 300), base, Options{Seed: 3, MaxPairs: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if res.TrainPairs != 5000 || res.TrainPairsTotal <= 5000 {
		t.Errorf("pairs used %d of %d, want 5000 of more", res.TrainPairs, res.TrainPairsTotal)
	}
	if !res.Beats() {
		t.Errorf("sampled fit fitted %.4f, hand %.4f", res.FitAccuracy, res.HandAccuracy)
	}
}

// The held-out split is a seeded hash of the key: about the asked share, and
// the same key always lands on the same side.
func TestHeldOutSplitIsByKey(t *testing.T) {
	in, out := 0, 0
	for i := range 10000 {
		if heldOut(fmt.Sprintf("gh:x:%d", i), 1, 30) {
			in++
		} else {
			out++
		}
	}
	if in < 2800 || in > 3200 {
		t.Errorf("30%% split held out %d of 10000", in)
	}
	if heldOut("gh:x:1", 1, 30) != heldOut("gh:x:1", 1, 30) {
		t.Error("split is not a function of the key")
	}
}
