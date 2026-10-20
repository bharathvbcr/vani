package rankeval

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// Red-team world variants: each post-processes the harness's generated world
// (never editing it) so that reality differs from the harness in one plausible
// way. Run with VANI_RANK_REDTEAM=1.

// rtWorld is one variant world: the Inputs rankers see, and the hidden truth
// (true company-adjusted relevance and true age) the grades come from.
type rtWorld struct {
	in  []jobinsight.Inputs
	adj []float64
	age []int
}

type rtVariant struct {
	name  string
	hs    []float64 // hidden recency half-lives; NDCG is averaged over them
	build func(w *world, r *rand.Rand) rtWorld
}

// baseRT copies the harness world.
func baseRT(w *world) rtWorld {
	rw := rtWorld{}
	for i, o := range w.obs {
		in := o.in
		in.Penalties = append([]jobinsight.Penalty(nil), in.Penalties...)
		rw.in = append(rw.in, in)
		rw.adj = append(rw.adj, w.postings[i].adj)
		rw.age = append(rw.age, w.postings[i].age)
	}
	return rw
}

// observedAge is the age a board shows for a posting of true age a.
func observedAge(w *world, p *posting, a int) int {
	co := w.companies[p.company]
	switch {
	case p.dated:
		return a
	case a <= co.crawlAge:
		return a
	}
	return -1
}

func rtGrades(adj []float64, age []int, h float64) []int {
	g := make([]int, len(adj))
	for i := range adj {
		v := adj[i] * recency(age[i], h)
		switch {
		case v >= 0.45:
			g[i] = 3
		case v >= 0.30:
			g[i] = 2
		case v >= 0.15:
			g[i] = 1
		}
	}
	return g
}

// cloneAt appends a copy of posting i with true age a to rw.
func cloneAt(w *world, rw *rtWorld, i, a int, adjScale float64) {
	in := rw.in[i]
	in.Penalties = append([]jobinsight.Penalty(nil), in.Penalties...)
	setAge(&in, observedAge(w, &w.postings[i], a))
	rw.in = append(rw.in, in)
	rw.adj = append(rw.adj, rw.adj[i]*adjScale)
	rw.age = append(rw.age, a)
}

var defaultHs = []float64{7, 30}

var rtVariants = []rtVariant{
	{"control", defaultHs, func(w *world, r *rand.Rand) rtWorld { return baseRT(w) }},
	{"a-overconfident", defaultHs, func(w *world, r *rand.Rand) rtWorld {
		rw := baseRT(w)
		for i := range rw.in {
			s := &rw.in[i].RoleFit
			if !s.Answered {
				continue
			}
			if s.HasP {
				s.P = 0.85 + 0.14*r.Float64()
			}
			if r.Float64() >= 0.6 { // right only 60% of the time
				for {
					v := roleFits[r.IntN(len(roleFits))]
					if v != w.postings[i].trueFit {
						s.Value = v
						break
					}
				}
			} else {
				s.Value = w.postings[i].trueFit
			}
		}
		return rw
	}},
	{"b1-stale-market", defaultHs, func(w *world, r *rand.Rand) rtWorld {
		rw := baseRT(w)
		for i := range rw.in {
			a := 60 + r.IntN(121)
			if r.Float64() < 0.08 {
				a = r.IntN(15)
			}
			rw.age[i] = a
			setAge(&rw.in[i], observedAge(w, &w.postings[i], a))
		}
		return rw
	}},
	{"b2-fresh-flood", defaultHs, func(w *world, r *rand.Rand) rtWorld {
		rw := baseRT(w)
		n := len(rw.in)
		var mediocre []int
		for i := 0; i < n; i++ {
			if rw.adj[i] < 0.35 {
				mediocre = append(mediocre, i)
			}
		}
		for range 600 { // hundreds posted today, mostly mediocre
			src := mediocre[r.IntN(len(mediocre))]
			if r.Float64() < 0.1 {
				src = r.IntN(n)
			}
			cloneAt(w, &rw, src, 0, 1)
		}
		return rw
	}},
	{"c-semantic-biased", defaultHs, func(w *world, r *rand.Rand) rtWorld {
		rw := baseRT(w)
		for i := range rw.in {
			p, co := &w.postings[i], w.companies[w.postings[i].company]
			if co.boilerplate > 0 || p.wall { // long generic postings read as similar to everything
				rw.in[i].Semantic = clampf(0.8+r.NormFloat64()*0.08, 0, 1)
				rw.in[i].HasSemantic = true
			}
			if r.Float64() < 0.5 {
				rw.in[i].Semantic, rw.in[i].HasSemantic = 0, false
			}
		}
		return rw
	}},
	{"d-coverage-vs-seniority", defaultHs, func(w *world, r *rand.Rand) rtWorld {
		rw := baseRT(w)
		for i := range rw.in {
			if rw.in[i].HasCoverage {
				rw.in[i].Coverage = clampf(0.6*rw.in[i].Coverage+25*float64(w.postings[i].seniority)+r.NormFloat64()*4, 0, 100)
			}
		}
		return rw
	}},
	{"e-undated-60pct", defaultHs, func(w *world, r *rand.Rand) rtWorld {
		rw := baseRT(w)
		cut := append([]float64(nil), rw.adj...)
		sort.Float64s(cut)
		top := cut[len(cut)*8/10]
		for i := range rw.in {
			pu := 0.55
			if rw.adj[i] >= top {
				pu = 0.8 // the best matches are the undated ones
			}
			if r.Float64() < pu {
				setAge(&rw.in[i], -1)
			} else {
				setAge(&rw.in[i], rw.age[i])
			}
		}
		return rw
	}},
	{"f1-recency-3d", []float64{3}, func(w *world, r *rand.Rand) rtWorld { return baseRT(w) }},
	{"f2-recency-90d", []float64{90}, func(w *world, r *rand.Rand) rtWorld { return baseRT(w) }},
	{"g1-heavy-marks", defaultHs, func(w *world, r *rand.Rand) rtWorld {
		rw := baseRT(w)
		for i := range rw.in {
			m := rw.in[i].Company
			rw.in[i].Company = jobinsight.CompanyMarks{Liked: m.Liked * 10, Hidden: m.Hidden * 10}
		}
		return rw
	}},
	{"g2-no-marks", defaultHs, func(w *world, r *rand.Rand) rtWorld {
		rw := baseRT(w)
		for i := range rw.in {
			rw.in[i].Company = jobinsight.CompanyMarks{}
		}
		return rw
	}},
	{"g3-giant-company", defaultHs, func(w *world, r *rand.Rand) rtWorld {
		rw := baseRT(w)
		n := len(rw.in)
		// One company posts 2,000 roles drawn from the general population,
		// with generic boilerplate bodies; the owner has marked it neither way.
		for range 2000 {
			src := r.IntN(n)
			cloneAt(w, &rw, src, r.IntN(maxAge+1), 1)
			k := len(rw.in) - 1
			rw.in[k].Company = jobinsight.CompanyMarks{}
			rw.in[k].PreferredEmployer = false
			rw.in[k].BodyHits += 8
		}
		return rw
	}},
}

// rtPrepared caches each variant's per-seed observations and grades.
type rtPrepared struct {
	obs    [][][]obs   // [variant][seed]
	grades [][][][]int // [variant][seed][h]
}

func prepareRT(seeds int) *rtPrepared {
	p := &rtPrepared{}
	worlds := make([]*world, seeds)
	for s := range worlds {
		worlds[s] = newWorld(uint64(s + 1))
	}
	for vi, v := range rtVariants {
		var vo [][]obs
		var vg [][][]int
		for s, w := range worlds {
			r := rand.New(rand.NewPCG(uint64(s+1)*7919, uint64(vi+1)*104729))
			rw := v.build(w, r)
			os := make([]obs, len(rw.in))
			for i := range rw.in {
				os[i] = obs{id: i, in: rw.in[i]}
			}
			var gs [][]int
			for _, h := range v.hs {
				gs = append(gs, rtGrades(rw.adj, rw.age, h))
			}
			vo, vg = append(vo, os), append(vg, gs)
		}
		p.obs, p.grades = append(p.obs, vo), append(p.grades, vg)
	}
	return p
}

// rtNDCG10 is ndcg(order, g, 10) with the ideal computed from grade counts
// (grades are 0-3), so large postings sets stay cheap.
func rtNDCG10(order, g []int) (float64, bool) {
	var cnt [4]int
	for _, x := range g {
		cnt[x]++
	}
	idcg, pos := 0.0, 0
	for gr := 3; gr >= 1 && pos < 10; gr-- {
		for ; cnt[gr] > 0 && pos < 10; cnt[gr]-- {
			idcg += (math.Exp2(float64(gr)) - 1) / math.Log2(float64(pos)+2)
			pos++
		}
	}
	if idcg == 0 {
		return 0, false
	}
	dcg := 0.0
	for i := 0; i < 10 && i < len(order); i++ {
		dcg += (math.Exp2(float64(g[order[i]])) - 1) / math.Log2(float64(i)+2)
	}
	return dcg / idcg, true
}

// score is the mean NDCG@10 of w on variant vi (over seeds and half-lives).
func (p *rtPrepared) score(w jobinsight.Weights, vi int) float64 {
	var sum float64
	var n int
	for s := range p.obs[vi] {
		order := scoreRankerFunc(p.obs[vi][s], func(in jobinsight.Inputs) float64 { return jobinsight.ScoreWith(in, w).Score })
		for _, g := range p.grades[vi][s] {
			if v, ok := rtNDCG10(order, g); ok {
				sum += v
				n++
			}
		}
	}
	if n == 0 {
		return math.NaN()
	}
	return sum / float64(n)
}

type rtCand struct {
	label string
	w     jobinsight.Weights
	per   []float64
	mean  float64
	min   float64
}

func (c *rtCand) fill(p *rtPrepared) {
	c.per = make([]float64, len(rtVariants))
	c.min = math.Inf(1)
	for vi := range rtVariants {
		c.per[vi] = p.score(c.w, vi)
		c.mean += c.per[vi]
		c.min = math.Min(c.min, c.per[vi])
	}
	c.mean /= float64(len(rtVariants))
}

func wlabel(w jobinsight.Weights) string {
	return fmt.Sprintf("hl=%g ff=%.2f fit=%.1f sem=%.2f ac=%.1f up=%.1f", w.HalfLifeDays, w.FreshFloor, w.Fit, w.Semantic, w.AnsweredConf, w.UnknownPrior)
}

const rtSeeds = 16

// TestRedTeamVariants evaluates DefaultWeights, the in-sample sweep winner and a
// grid across the adversarial variants. Gated so the normal suite stays fast.
func TestRedTeamVariants(t *testing.T) {
	if os.Getenv("VANI_RANK_REDTEAM") != "1" {
		t.Skip("set VANI_RANK_REDTEAM=1 to run the red-team variants")
	}
	start := time.Now()
	p := prepareRT(rtSeeds)

	def := jobinsight.DefaultWeights()
	sweep := def
	sweep.HalfLifeDays, sweep.FreshFloor, sweep.Fit = 21, 0.25, 0.4

	var cands []*rtCand
	discarded := 0
	for _, hl := range []float64{5, 7, 10, 14, 21} {
		for _, ff := range []float64{0.15, 0.25, 0.35} {
			for _, ft := range []float64{0.3, 0.4, 0.5} {
				for _, sem := range []float64{0.15, 0.25} {
					for _, ac := range []float64{0.5, 0.7} {
						for _, up := range []float64{0.3, 0.4} {
							w := def
							w.HalfLifeDays, w.FreshFloor, w.Fit, w.Semantic, w.AnsweredConf, w.UnknownPrior = hl, ff, ft, sem, ac, up
							if len(invariantViolations(w)) != 0 {
								discarded++
								continue
							}
							c := &rtCand{label: wlabel(w), w: w}
							c.fill(p)
							cands = append(cands, c)
						}
					}
				}
			}
		}
	}
	dc, sc := &rtCand{label: "DEFAULT " + wlabel(def), w: def}, &rtCand{label: "SWEEP-WINNER " + wlabel(sweep), w: sweep}
	dc.fill(p)
	sc.fill(p)

	var hdr strings.Builder
	for _, v := range rtVariants {
		fmt.Fprintf(&hdr, " %s", v.name)
	}
	t.Logf("variants:%s", hdr.String())
	row := func(c *rtCand) string {
		var b strings.Builder
		for _, x := range c.per {
			fmt.Fprintf(&b, " %.3f", x)
		}
		return fmt.Sprintf("mean %.4f min %.4f |%s | %s", c.mean, c.min, b.String(), c.label)
	}
	t.Logf("%d candidates (%d discarded by invariants), %d seeds, %s", len(cands), discarded, rtSeeds, time.Since(start).Round(time.Millisecond))
	t.Log(row(dc))
	t.Log(row(sc))

	byMean := append([]*rtCand(nil), cands...)
	sort.SliceStable(byMean, func(i, j int) bool { return byMean[i].mean > byMean[j].mean })
	byMin := append([]*rtCand(nil), cands...)
	sort.SliceStable(byMin, func(i, j int) bool { return byMin[i].min > byMin[j].min })
	t.Log("top 6 by MEAN:")
	for _, c := range byMean[:min(6, len(byMean))] {
		t.Log("  " + row(c))
	}
	t.Log("top 6 by WORST-CASE (min):")
	for _, c := range byMin[:min(6, len(byMin))] {
		t.Log("  " + row(c))
	}
	// Robust pick: best worst-case, ties broken by mean.
	best := byMin[0]
	t.Logf("ROBUST PICK: %s", row(best))
	t.Logf("regret of DefaultWeights vs robust pick: mean %.4f, worst-case %.4f", best.mean-dc.mean, best.min-dc.min)
	var worstReg float64
	worstName := ""
	for vi, v := range rtVariants {
		if d := best.per[vi] - dc.per[vi]; d > worstReg {
			worstReg, worstName = d, v.name
		}
	}
	t.Logf("largest per-variant regret of DefaultWeights: %.4f on %s", worstReg, worstName)
	if el := time.Since(start); el > 3*time.Minute {
		t.Errorf("red-team run took %s, want under 3m", el)
	}
}
