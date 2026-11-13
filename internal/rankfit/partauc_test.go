package rankfit

import (
	"math"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// aucItem is a judged item with every part present and known at conf 1.
func aucItem(key string, grade int, src string, vals [NumParts]float64) Item {
	it := Item{Key: key, Grade: grade, Judged: true, Source: src, PSelect: 0.5, Value: vals, Gate: 1, Fresh: 1}
	for i := range NumParts {
		it.Present[i], it.Known[i], it.Conf[i] = true, true, 1
	}
	return it
}

func TestPartAUCOrdersEachPartAgainstTheOwner(t *testing.T) {
	var items []Item
	// semantic separates perfectly; fit is reversed; coverage is a tie.
	for i, g := range []int{GradeYes, GradeYes, GradeMaybe, GradeNo, GradeNo, GradeNo} {
		var v [NumParts]float64
		hit := g != GradeNo
		v[PartSemantic] = 0.1 + 0.1*float64(i)
		if hit {
			v[PartSemantic] += 1 // wanted all above every unwanted
			v[PartFit] = 0.1
		} else {
			v[PartFit] = 0.9
		}
		v[PartSemantic] = math.Min(v[PartSemantic]/2, 1)
		v[PartCoverage] = 0.5
		items = append(items, aucItem(string(rune('a'+i)), g, "decile:5", v))
	}
	items = append(items, Item{Key: "mark", Grade: GradeYes}) // a mark: not judged, left out
	r := BuildPartAUC(items, jobinsight.DefaultWeights(), "all judged", nil, 200, 7)
	if r.Pos != 3 || r.Neg != 3 || r.Status != PartAUCOK {
		t.Fatalf("report: %+v", r)
	}
	row := func(name string) PartAUCRow {
		for _, x := range r.Rows {
			if x.Part == name {
				return x
			}
		}
		t.Fatalf("no row %s", name)
		return PartAUCRow{}
	}
	if s := row("semantic"); s.AUC != 1 || !s.OK || s.Lo > 1 || s.Hi != 1 {
		t.Errorf("semantic: %+v", s)
	}
	if f := row("fit"); f.AUC != 0 || f.Hi > 0.5 {
		t.Errorf("fit should be reversed: %+v", f)
	}
	if c := row("coverage"); c.AUC != 0.5 {
		t.Errorf("coverage tie: %+v", c)
	}
	if m := row(PartMatchGate); !m.OK || m.AUC < 0 || m.AUC > 1 {
		t.Errorf("match x gate: %+v", m)
	}
	md := r.Markdown()
	for _, want := range []string{"semantic", "1.00", "0.00", "reversed", "match x gate"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	// Same seed, same interval.
	if again := BuildPartAUC(items, jobinsight.DefaultWeights(), "all judged", nil, 200, 7); again.Rows[0] != r.Rows[0] {
		t.Errorf("not deterministic: %+v vs %+v", again.Rows[0], r.Rows[0])
	}
}

// A part known only on wanted postings (the unwanted ones sit at the prior)
// separates the classes by whether it was known, not by its value: the
// all-postings AUC is high, but the row must not read as ordering taste.
func TestPartAUCDoesNotReadKnownnessAsTaste(t *testing.T) {
	prior := jobinsight.DefaultWeights().UnknownPrior
	var items []Item
	for i := range 4 {
		var v [NumParts]float64
		v[PartSemantic] = prior + 0.1 + 0.01*float64(i)
		items = append(items, aucItem("w"+string(rune('a'+i)), GradeYes, "decile:8", v))
	}
	for i := range 6 {
		var v [NumParts]float64
		v[PartSemantic] = prior
		it := aucItem("u"+string(rune('a'+i)), GradeNo, "decile:3", v)
		it.Known[PartSemantic], it.Conf[PartSemantic] = false, 0
		items = append(items, it)
	}
	r := BuildPartAUC(items, jobinsight.DefaultWeights(), "all judged", nil, 200, 3)
	var sem PartAUCRow
	for _, x := range r.Rows {
		if x.Part == "semantic" {
			sem = x
		}
	}
	if sem.AUC != 1 || sem.KnownPos != 4 || sem.KnownNeg != 0 || sem.KnownOK {
		t.Fatalf("semantic: %+v", sem)
	}
	md := r.Markdown()
	line := ""
	for _, l := range strings.Split(md, "\n") {
		if strings.HasPrefix(l, "| semantic |") {
			line = l
		}
	}
	if !strings.Contains(line, "too few known") || strings.Contains(line, "orders the owner's taste") {
		t.Errorf("semantic row reads knownness as taste: %q", line)
	}
}

func TestPartAUCRefusesTooFewOfAClass(t *testing.T) {
	items := []Item{
		aucItem("a", GradeYes, "decile:9", [NumParts]float64{}),
		aucItem("b", GradeNo, "decile:1", [NumParts]float64{}),
		aucItem("c", GradeNo, "decile:2", [NumParts]float64{}),
		aucItem("d", GradeNo, "decile:3", [NumParts]float64{}),
	}
	r := BuildPartAUC(items, jobinsight.DefaultWeights(), "all judged", nil, 200, 1)
	if r.Status != PartAUCInsufficient || len(r.Rows) != 0 || !strings.Contains(r.Markdown(), "insufficient") {
		t.Errorf("one wanted posting must not give an AUC: %+v\n%s", r, r.Markdown())
	}
	if e := BuildPartAUC(nil, jobinsight.DefaultWeights(), "all judged", nil, 200, 1); e.Status != PartAUCInsufficient {
		t.Errorf("empty: %+v", e)
	}
}

func TestPartAUCScopeKeepsOnlyItsItems(t *testing.T) {
	var items []Item
	for i := range 4 {
		items = append(items, aucItem("s"+string(rune('a'+i)), []int{GradeYes, GradeMaybe, GradeYes, GradeNo}[i], "decile:5", [NumParts]float64{}))
		items = append(items, aucItem("d"+string(rune('a'+i)), GradeNo, "dropped:model:role_fit", [NumParts]float64{}))
	}
	scored := BuildPartAUC(items, jobinsight.DefaultWeights(), "scored stratum", func(it *Item) bool { return it.stratum() == StratumScored }, 50, 1)
	if scored.Pos != 3 || scored.Neg != 1 || scored.Status != PartAUCInsufficient {
		t.Errorf("scored scope: %+v", scored)
	}
	all := BuildPartAUC(items, jobinsight.DefaultWeights(), "all judged", nil, 50, 1)
	if all.Pos != 3 || all.Neg != 5 || all.Status != PartAUCOK {
		t.Errorf("all scope: %+v", all)
	}
}

func TestPartAUCBoundsItsWork(t *testing.T) {
	items := make([]Item, 0, 2*MaxPartAUCItems+2)
	for i := range MaxPartAUCItems + 1 {
		items = append(items, aucItem("y", GradeYes, "decile:5", [NumParts]float64{}))
		_ = i
	}
	for range MaxPartAUCItems + 1 {
		items = append(items, aucItem("n", GradeNo, "decile:5", [NumParts]float64{}))
	}
	r := BuildPartAUC(items, jobinsight.DefaultWeights(), "all judged", nil, 10, 1)
	if r.Status != PartAUCTooMany || len(r.Rows) != 0 {
		t.Errorf("over the bound must refuse, not sample: %+v", r.Status)
	}
	// Resamples are clamped, never unbounded.
	small := []Item{
		aucItem("a", GradeYes, "decile:5", [NumParts]float64{}), aucItem("b", GradeYes, "decile:5", [NumParts]float64{}),
		aucItem("c", GradeYes, "decile:5", [NumParts]float64{}), aucItem("d", GradeNo, "decile:5", [NumParts]float64{}),
		aucItem("e", GradeNo, "decile:5", [NumParts]float64{}), aucItem("f", GradeNo, "decile:5", [NumParts]float64{}),
	}
	if r := BuildPartAUC(small, jobinsight.DefaultWeights(), "x", nil, 1<<30, 1); r.Resamples != MaxPartAUCResamples {
		t.Errorf("resamples %d", r.Resamples)
	}
	if r := BuildPartAUC(small, jobinsight.DefaultWeights(), "x", nil, 0, 1); r.Resamples != DefaultPartAUCResamples {
		t.Errorf("default resamples %d", r.Resamples)
	}
}
