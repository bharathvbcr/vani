package clusterstudy

import (
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
)

func near[T, U ~float64](a T, b U) bool { return math.Abs(float64(a)-float64(b)) < 1e-12 }

func TestWithinVarianceByHand(t *testing.T) {
	// cell 0: one yes, one no (variance .25 each posting); cell 1: two yes (0).
	w, err := WithinVariance([]int{0, 0, 1, 1}, []bool{true, false, true, true})
	if err != nil {
		t.Fatal(err)
	}
	if w.N != 4 || w.Cells != 2 || w.Positives != 3 {
		t.Errorf("%+v", w)
	}
	if !near(w.Total, 0.1875) || !near(w.Plugin, 0.125) || !near(w.Pooled, 0.25) {
		t.Errorf("total %v plugin %v pooled %v, want .1875 .125 .25", w.Total, w.Plugin, w.Pooled)
	}
}

func TestWithinVarianceLimits(t *testing.T) {
	target := []bool{true, false, true, false, false, true}
	// One cell: the stratification explains nothing, so the within variance is the total.
	one, _ := WithinVariance([]int{7, 7, 7, 7, 7, 7}, target)
	if !near(one.Plugin, one.Total) {
		t.Errorf("one cell: plugin %v, total %v", one.Plugin, one.Total)
	}
	// Cells that sort the target perfectly leave nothing inside.
	perfect, _ := WithinVariance([]int{1, 0, 1, 0, 0, 1}, target)
	if perfect.Plugin != 0 || perfect.Pooled != 0 {
		t.Errorf("perfect: %+v", perfect)
	}
	// Cell numbering is only a name.
	renamed, _ := WithinVariance([]int{-5, 99, -5, 99, 99, -5}, target)
	if renamed != perfect {
		t.Errorf("renamed cells: %+v vs %+v", renamed, perfect)
	}
	// Every posting alone: no stratum has two, so the corrected figure does not exist.
	single, _ := WithinVariance([]int{0, 1, 2, 3, 4, 5}, target)
	if single.Plugin != 0 || !math.IsNaN(float64(single.Pooled)) {
		t.Errorf("all singletons: plugin %v pooled %v", single.Plugin, single.Pooled)
	}
	// A target that never varies has no variance to reduce.
	flat, _ := WithinVariance([]int{0, 0, 1}, []bool{true, true, true})
	if flat.Plugin != 0 || flat.Total != 0 {
		t.Errorf("constant target: %+v", flat)
	}
	if !math.IsNaN(Reduction(float64(flat.Plugin), float64(flat.Plugin))) {
		t.Error("a reduction of a zero variance should be NaN, not 0 or 1")
	}
}

func TestWithinVarianceRefusesWhatItCannotMeasure(t *testing.T) {
	if _, err := WithinVariance(nil, nil); !errors.Is(err, ErrInput) {
		t.Errorf("empty: %v", err)
	}
	if _, err := WithinVariance([]int{0, 1}, []bool{true}); !errors.Is(err, ErrInput) {
		t.Errorf("mismatch: %v", err)
	}
}

func TestReductionIsOneMinusTheRatio(t *testing.T) {
	if !near(Reduction(0.2, 0.15), 0.25) {
		t.Error("Reduction(.2,.15)")
	}
	if Reduction(0.2, 0.3) >= 0 {
		t.Error("a worse stratification must read as a negative reduction")
	}
	if !math.IsNaN(Reduction(0, 0.1)) || !math.IsNaN(Reduction(0.1, math.NaN())) {
		t.Error("an undefined reduction must be NaN")
	}
}

// synthetic builds postings whose target depends on a hidden group, so a
// stratification equal to the group is far better than an unrelated one.
func synthetic(n, groups int, seed uint64) (group, unrelated []int, target []bool) {
	rng := rand.New(rand.NewPCG(seed, 1))
	for i := 0; i < n; i++ {
		g := rng.IntN(groups)
		group = append(group, g)
		unrelated = append(unrelated, rng.IntN(groups))
		target = append(target, rng.Float64() < 0.05+0.9*float64(g)/float64(groups-1))
	}
	return
}

func TestCompareSeparatesASignalFromNoise(t *testing.T) {
	group, unrelated, target := synthetic(2000, 8, 1)
	good, err := Compare(unrelated, group, target, 300, 7)
	if err != nil {
		t.Fatal(err)
	}
	if good.Reduction < 0.3 || good.Bootstrap.Lo < 0.2 {
		t.Errorf("the group should explain the target: %+v", good)
	}
	if good.NullAtOrAbove > 0.01 || good.Null.Hi >= good.Reduction {
		t.Errorf("a real reduction must sit outside the permutation null: %+v", good)
	}
	// Two stratifications that are both unrelated to the target reduce nothing.
	_, other, _ := synthetic(2000, 8, 2)
	bad, err := Compare(unrelated, other, target, 300, 7)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(bad.Reduction)) > 0.1 || bad.Bootstrap.Lo > 0.1 {
		t.Errorf("unrelated stratifications: %+v", bad)
	}
	if bad.Null.Lo > bad.Reduction || bad.Reduction > bad.Null.Hi {
		t.Errorf("noise should fall inside the null band: reduction %v null %+v", bad.Reduction, bad.Null)
	}
}

func TestCompareIsReproducibleAndBounded(t *testing.T) {
	group, unrelated, target := synthetic(300, 5, 3)
	a, _ := Compare(unrelated, group, target, 50, 11)
	b, _ := Compare(unrelated, group, target, 50, 11)
	if a != b {
		t.Error("the same seed must give the same answer")
	}
	for _, reps := range []int{0, -1, MaxResamples + 1} {
		if _, err := Compare(unrelated, group, target, reps, 1); !errors.Is(err, ErrInput) {
			t.Errorf("reps %d: %v", reps, err)
		}
	}
	if _, err := Compare(unrelated, group[:10], target, 5, 1); !errors.Is(err, ErrInput) {
		t.Errorf("length mismatch: %v", err)
	}
	if _, err := Compare(nil, nil, nil, 5, 1); !errors.Is(err, ErrInput) {
		t.Errorf("empty: %v", err)
	}
}

func TestNumCarriesAnUndefinedFigureAsNull(t *testing.T) {
	w, _ := WithinVariance([]int{0, 1, 2}, []bool{true, false, true}) // every posting alone: no pooled figure
	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("a within-variance with an undefined figure must still encode: %v", err)
	}
	if !strings.Contains(string(raw), `"pooled":null`) {
		t.Errorf("undefined pooled variance encoded as %s", raw)
	}
	var back Within
	if err := json.Unmarshal(raw, &back); err != nil || !math.IsNaN(float64(back.Pooled)) || back.Cells != 3 {
		t.Errorf("round trip: %+v, %v", back, err)
	}
	raw, _ = json.Marshal([]Num{0.25, Num(math.Inf(1)), Num(math.NaN())})
	if string(raw) != "[0.25,null,null]" {
		t.Errorf("encoded %s", raw)
	}
	var n Num
	if err := json.Unmarshal([]byte(`"x"`), &n); err == nil {
		t.Error("a string decoded as a number")
	}
}

func TestCompareOfAConstantTargetIsUndefinedNotZero(t *testing.T) {
	c, err := Compare([]int{0, 0, 1, 1}, []int{0, 1, 0, 1}, []bool{false, false, false, false}, 20, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !math.IsNaN(float64(c.Reduction)) || !math.IsNaN(float64(c.Bootstrap.Lo)) {
		t.Errorf("no variance to reduce, got %+v", c)
	}
}
