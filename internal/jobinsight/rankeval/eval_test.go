package rankeval

import (
	"math"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// scenario removes signals from every posting's Inputs; the legacy rankers
// read the same masked Inputs, so both sides see equal information.
type scenario struct {
	name  string
	apply func(in *jobinsight.Inputs)
}

var scenarios = []scenario{
	{"full", func(in *jobinsight.Inputs) {}},
	{"no-semantic", func(in *jobinsight.Inputs) { in.Semantic, in.HasSemantic = 0, false }},
	{"no-devprism", func(in *jobinsight.Inputs) { in.Coverage, in.HasCoverage = 0, false }},
	{"this-machine", func(in *jobinsight.Inputs) {
		in.Coverage, in.HasCoverage = 0, false
		in.Semantic, in.HasSemantic = 0, false
		in.Target, in.TitleHits, in.BodyHits = false, 0, 0
		in.TitleSignals, in.TitleRank = false, 0
		in.Company = jobinsight.CompanyMarks{}
		in.PreferredEmployer = false
	}},
	{"no-decision-model", func(in *jobinsight.Inputs) { in.RoleFit = jobinsight.Slot{}; in.Penalties = nil }},
}

func (w *world) masked(sc scenario) []obs {
	out := make([]obs, len(w.obs))
	for i, o := range w.obs {
		o.in.Penalties = append([]jobinsight.Penalty(nil), o.in.Penalties...)
		sc.apply(&o.in)
		out[i] = o
	}
	return out
}

// prepared caches, per scenario, each world's masked observations and grades.
type prepared struct {
	worlds []*world
	obs    [][][]obs // [scenario][world]
	grades [][][]int // [utility][world]
}

func prepare(seeds int) *prepared {
	p := &prepared{}
	for s := range seeds {
		p.worlds = append(p.worlds, newWorld(uint64(s+1)))
	}
	for _, sc := range scenarios {
		var per [][]obs
		for _, w := range p.worlds {
			per = append(per, w.masked(sc))
		}
		p.obs = append(p.obs, per)
	}
	for _, u := range utilities {
		var per [][]int
		for _, w := range p.worlds {
			per = append(per, w.grades(u))
		}
		p.grades = append(p.grades, per)
	}
	return p
}

func ndcg(order []int, grades []int, k int) (float64, bool) {
	dcg := 0.0
	for i := 0; i < k && i < len(order); i++ {
		dcg += (math.Exp2(float64(grades[order[i]])) - 1) / math.Log2(float64(i)+2)
	}
	ideal := append([]int(nil), grades...)
	for i := 1; i < len(ideal); i++ {
		for j := i; j > 0 && ideal[j] > ideal[j-1]; j-- {
			ideal[j], ideal[j-1] = ideal[j-1], ideal[j]
		}
	}
	idcg := 0.0
	for i := 0; i < k && i < len(ideal); i++ {
		idcg += (math.Exp2(float64(ideal[i])) - 1) / math.Log2(float64(i)+2)
	}
	if idcg == 0 {
		return 0, false
	}
	return dcg / idcg, true
}

func firstGrade3(order []int, grades []int) (int, bool) {
	for i, id := range order {
		if grades[id] == 3 {
			return i + 1, true
		}
	}
	return 0, false
}

type stat struct{ mean, min float64 }

func summarize(xs []float64) stat {
	if len(xs) == 0 {
		return stat{math.NaN(), math.NaN()}
	}
	s := stat{min: math.Inf(1)}
	for _, x := range xs {
		s.mean += x
		s.min = math.Min(s.min, x)
	}
	s.mean /= float64(len(xs))
	return s
}

type result struct {
	n10, n25 stat
	first3   float64 // mean rank of the first grade-3 posting
	worlds   int     // worlds that had an ideal ranking
}

// evaluate ranks every world of scenario si and scores it under utility ui.
func (p *prepared) evaluate(r ranker, si, ui int) result {
	var a, b, f []float64
	for wi := range p.worlds {
		order := r.order(p.obs[si][wi])
		g := p.grades[ui][wi]
		n10, ok := ndcg(order, g, 10)
		if !ok {
			continue
		}
		n25, _ := ndcg(order, g, 25)
		a, b = append(a, n10), append(b, n25)
		if f3, ok := firstGrade3(order, g); ok {
			f = append(f, float64(f3))
		}
	}
	return result{n10: summarize(a), n25: summarize(b), first3: summarize(f).mean, worlds: len(a)}
}

const evalSeeds = 24

// TestWorldIsNotTautological guards the harness itself: the hidden-truth
// oracle must be perfect, while every observable ranker is visibly imperfect,
// so NDCG is not 1 by construction.
func TestWorldIsNotTautological(t *testing.T) {
	p := prepare(evalSeeds)
	for ui, u := range utilities {
		var perfect, sumGrade3 float64
		var graded, empty, no3 int
		for wi, w := range p.worlds {
			g := p.grades[ui][wi]
			// The oracle ranks by the hidden utility itself.
			order := make([]int, len(w.postings))
			for i := range order {
				order[i] = i
			}
			util := func(i int) float64 { return w.postings[i].adj * recency(w.postings[i].age, u.halfLife) }
			for i := 1; i < len(order); i++ {
				for j := i; j > 0 && util(order[j]) > util(order[j-1]); j-- {
					order[j], order[j-1] = order[j-1], order[j]
				}
			}
			n, ok := ndcg(order, g, 10)
			if !ok {
				empty++
				continue
			}
			perfect += n
			graded++
			had3 := false
			for _, x := range g {
				if x == 3 {
					sumGrade3++
					had3 = true
				}
			}
			if !had3 {
				no3++
			}
		}
		if graded == 0 || perfect/float64(graded) < 0.999 {
			t.Errorf("%s: oracle NDCG@10 = %.3f over %d worlds, want 1", u.name, perfect/math.Max(1, float64(graded)), graded)
		}
		t.Logf("%s: mean grade-3 postings per world %.1f of %d; worlds with no relevant posting %d, with no grade-3 %d (of %d)",
			u.name, sumGrade3/float64(len(p.worlds)), nPostings, empty, no3, len(p.worlds))
		if empty > 0 || no3 > len(p.worlds)/5 {
			t.Errorf("%s: too many degenerate worlds (%d empty, %d without grade 3)", u.name, empty, no3)
		}
		for _, lr := range legacyRankers {
			if res := p.evaluate(lr, 0, ui); res.n10.mean > 0.97 {
				t.Errorf("%s %s: NDCG@10 %.3f is near-perfect; observations leak the truth", u.name, lr.name, res.n10.mean)
			}
		}
	}
}
