package rankfit

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// silverItems builds nYes yes, nNo no and nMaybe maybe judged items. Each item's
// title part comes from title(i, grade) and its auditor score from audit(i, grade).
func silverItems(nYes, nNo, nMaybe int, title, audit func(i, grade int) float64) ([]Item, map[string]float64) {
	var items []Item
	scores := map[string]float64{}
	add := func(prefix string, n, grade int) {
		for i := range n {
			var v [NumParts]float64
			v[PartTitle] = title(i, grade)
			it := aucItem(fmt.Sprintf("%s%d", prefix, i), grade, "decile:5", v)
			items = append(items, it)
			scores[it.Key] = audit(i, grade)
		}
	}
	add("y", nYes, GradeYes)
	add("n", nNo, GradeNo)
	add("m", nMaybe, GradeMaybe)
	return items, scores
}

func TestScoreAUCIsTheMannWhitneyWithABootstrapInterval(t *testing.T) {
	a, lo, hi, ok := ScoreAUC([]float64{0.9, 0.8, 0.7}, []float64{0.1, 0.2, 0.3}, 300, 1)
	if !ok || a != 1 || lo != 1 || hi != 1 {
		t.Errorf("perfect separation: %v %v %v %v", a, lo, hi, ok)
	}
	a, _, _, _ = ScoreAUC([]float64{0.5, 0.5}, []float64{0.5, 0.5}, 100, 1)
	if a != 0.5 {
		t.Errorf("all ties: %v, want 0.5", a)
	}
	a, _, _, _ = ScoreAUC([]float64{0.1}, []float64{0.9}, 100, 1)
	if a != 0 {
		t.Errorf("reversed: %v", a)
	}
	l1, h1 := func() (float64, float64) {
		_, l, h, _ := ScoreAUC([]float64{1, 2, 3, 4}, []float64{2.5, 3.5, 0.5}, 400, 9)
		return l, h
	}()
	l2, h2 := func() (float64, float64) {
		_, l, h, _ := ScoreAUC([]float64{1, 2, 3, 4}, []float64{2.5, 3.5, 0.5}, 400, 9)
		return l, h
	}()
	if l1 != l2 || h1 != h2 || !(l1 < h1) {
		t.Errorf("the interval is not deterministic or has no width: %v %v / %v %v", l1, h1, l2, h2)
	}
	for _, c := range [][2][]float64{{nil, {1}}, {{1}, nil}, {nil, nil}} {
		if _, _, _, ok := ScoreAUC(c[0], c[1], 10, 1); ok {
			t.Error("an AUC over an empty class")
		}
	}
	if _, _, _, ok := ScoreAUC(make([]float64, MaxPartAUCItems+1), []float64{1}, 10, 1); ok {
		t.Error("an oversized class was computed")
	}
}

func TestSilverSaysInsufficientBelowTheCountsAndShowsNoAUC(t *testing.T) {
	perfect := func(i, g int) float64 { return float64(g) }
	for _, c := range []struct{ yes, no int }{{9, 60}, {30, 29}, {0, 0}, {3, 3}} {
		items, scores := silverItems(c.yes, c.no, 0, func(int, int) float64 { return 0.5 }, perfect)
		r := BuildSilverAUC(items, scores, jobinsight.DefaultWeights(), 200, 1)
		if r.Status != SilverInsufficient || r.Auditor != nil || r.Title != nil {
			t.Errorf("%d yes %d no: status %q with AUCs %+v %+v", c.yes, c.no, r.Status, r.Auditor, r.Title)
		}
		md := r.Markdown()
		if !strings.Contains(md, "insufficient") || strings.Contains(md, "AUC (95%)") || strings.Contains(md, "go:") {
			t.Errorf("%d yes %d no: markdown shows a verdict or a table:\n%s", c.yes, c.no, md)
		}
	}
}

func TestSilverCountsOnlyScoredPostingsTowardTheThreshold(t *testing.T) {
	items, scores := silverItems(10, 30, 5, func(int, int) float64 { return 0.5 }, func(i, g int) float64 { return float64(g) + 0.01*float64(i) })
	// Two yes postings have no auditor score (never compared): 8 scored yes is under 10.
	delete(scores, "y0")
	delete(scores, "y1")
	r := BuildSilverAUC(items, scores, jobinsight.DefaultWeights(), 200, 1)
	if r.Status != SilverInsufficient || r.Pos != 8 || r.Unscored != 2 || r.Neg != 30 {
		t.Errorf("status %q pos %d neg %d unscored %d", r.Status, r.Pos, r.Neg, r.Unscored)
	}
}

func TestSilverGoNeedsBothTheBoundAndTheTitleBeat(t *testing.T) {
	w := jobinsight.DefaultWeights()
	// The auditor separates the classes well; the title part is a coin flip.
	items, scores := silverItems(12, 40, 6, func(i, g int) float64 { return float64((i*7+g*3)%10) / 10 }, func(i, g int) float64 { return float64(g)*2 + float64(i%5)/10 })
	r := BuildSilverAUC(items, scores, w, 500, 1)
	if r.Status != SilverGo || r.Auditor == nil || r.Title == nil {
		t.Fatalf("status %q reason %q", r.Status, r.Reason)
	}
	if r.Pos != 12 || r.Neg != 40 {
		t.Errorf("maybes were counted: %d yes %d no", r.Pos, r.Neg)
	}
	if !(r.Auditor.Lo > 0.5) || !(r.Auditor.AUC > r.Title.AUC) {
		t.Errorf("go without the bound or the beat: %+v %+v", r.Auditor, r.Title)
	}

	// The title part is just as good: the auditor does not beat it, so no go.
	items, scores = silverItems(12, 40, 0, func(i, g int) float64 { return float64(g)*0.4 + float64(i%5)/50 }, func(i, g int) float64 { return float64(g)*0.4 + float64(i%5)/50 })
	r = BuildSilverAUC(items, scores, w, 500, 1)
	if r.Status != SilverNoGo || !strings.Contains(r.Reason, "does not beat the title part") {
		t.Errorf("a tie with the title part: status %q reason %q", r.Status, r.Reason)
	}

	// A noisy auditor: its lower bound is not above 0.5, whatever the title does.
	items, scores = silverItems(10, 30, 0, func(int, int) float64 { return 0.5 }, func(i, g int) float64 { return float64((i*37 + g*11) % 17) })
	r = BuildSilverAUC(items, scores, w, 500, 1)
	if r.Status != SilverNoGo || r.Auditor == nil || r.Auditor.Lo > 0.5 {
		t.Errorf("noise: status %q auditor %+v", r.Status, r.Auditor)
	}
	if !strings.Contains(r.Markdown(), "verdict: no_go") {
		t.Errorf("markdown lacks the verdict:\n%s", r.Markdown())
	}
}

func TestSilverComparesTheTitlePartOnTheSamePostingsOnly(t *testing.T) {
	// 12 yes and 40 no are scored. 20 more no postings carry a title value that
	// would drag the title part's AUC to 0 if they were counted; they have no
	// auditor score, so the title part must be computed without them.
	items, scores := silverItems(12, 40, 0, func(i, g int) float64 { return float64(g)*0.5 + float64(i%5)/100 }, func(i, g int) float64 { return float64(g) + float64(i%5)/10 })
	for i := range 20 {
		var v [NumParts]float64
		v[PartTitle] = 1
		items = append(items, aucItem(fmt.Sprintf("z%d", i), GradeNo, "decile:5", v))
	}
	r := BuildSilverAUC(items, scores, jobinsight.DefaultWeights(), 300, 1)
	if r.Unscored != 20 || r.Title == nil || math.Abs(r.Title.AUC-1) > 1e-9 {
		t.Errorf("unscored %d, title %+v: the title part was computed over postings the auditor never scored", r.Unscored, r.Title)
	}
}

func TestSilverIgnoresUnjudgedItems(t *testing.T) {
	items, scores := silverItems(10, 30, 0, func(int, int) float64 { return 0.5 }, func(i, g int) float64 { return float64(g) + float64(i)/100 })
	items = append(items, Item{Key: "mark", Grade: GradeYes}) // a mark, not an audit judgment
	scores["mark"] = 5
	r := BuildSilverAUC(items, scores, jobinsight.DefaultWeights(), 200, 1)
	if r.Pos != 10 || r.Unscored != 0 {
		t.Errorf("a mark counted: pos %d unscored %d", r.Pos, r.Unscored)
	}
}

func TestSilverTooManyIsNotComputed(t *testing.T) {
	items, scores := silverItems(MaxPartAUCItems+1, 30, 0, func(int, int) float64 { return 0.5 }, func(i, g int) float64 { return float64(g) })
	r := BuildSilverAUC(items, scores, jobinsight.DefaultWeights(), 10, 1)
	if r.Status != SilverTooMany || r.Auditor != nil {
		t.Errorf("status %q", r.Status)
	}
}
