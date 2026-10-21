package rankfit

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/bharathvbcr/Vani/internal/feedback"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// handItems are two postings whose match is worked by hand under the default
// shares (fit .30, coverage .20, semantic .35, keyword .03, title .10,
// employer .05; kb_match not asked, so W = 1.03), prior .4, shrink .3, every
// known part read with confidence 1:
//
//	A: fit 1.0 and semantic 0.5 known.  N = .3 + .175 = .475, D = .65
//	   match = (N + .3*.4*W) / (D + .3*W) = .5986 / .959 = .624191866...
//	B: semantic 0.2 known.              N = .07, D = .35
//	   match = .1936 / .659 = .293778452...; gate .5, fresh .25
func handItems() []Item {
	unknown := [NumParts]float64{0.4, 0.4, 0.4, 0.4, 0.4, 0, 0}
	present := [NumParts]bool{true, true, true, true, true, true, false}
	employer := [NumParts]float64{PartEmployer: 1}
	a := Item{Key: "a", Value: unknown, Present: present, Conf: employer, Gate: 1, Fresh: 1, Grade: GradeYes, Judged: true}
	a.Value[PartFit], a.Known[PartFit], a.Conf[PartFit] = 1, true, 1
	a.Value[PartSemantic], a.Known[PartSemantic], a.Conf[PartSemantic] = 0.5, true, 1
	b := Item{Key: "b", Value: unknown, Present: present, Conf: employer, Gate: 0.5, Fresh: 0.25, Grade: GradeNo, Judged: true}
	b.Value[PartSemantic], b.Known[PartSemantic], b.Conf[PartSemantic] = 0.2, true, 1
	return []Item{a, b}
}

func TestLogMatchHandComputed(t *testing.T) {
	b := blendOf(jobinsight.DefaultWeights())
	s := SharesOf(jobinsight.DefaultWeights()).Array()
	items := handItems()
	if got, want := math.Exp(b.logMatch(&items[0], &s, nil)), 0.5986/0.959; math.Abs(got-want) > 1e-15 {
		t.Errorf("match A = %.17g, want %.17g", got, want)
	}
	if got, want := math.Exp(b.logMatch(&items[1], &s, nil)), 0.1936/0.659; math.Abs(got-want) > 1e-15 {
		t.Errorf("match B = %.17g, want %.17g", got, want)
	}
}

// The pair loss at the hand weights, by hand: u_A = ln .624191867 = -.471297479,
// u_B = ln(.293778452 * .5 * .25) = -3.304370901, so the gap is 2.833073422 and
// softplus(-gap) = ln(1 + e^-2.833073422) = .057166188. One pair, two training
// items: the loss is scaled by 2/1, and every prior term is 0 at the start.
func TestPairLossHandComputed(t *testing.T) {
	items := handItems()
	base := jobinsight.DefaultWeights()
	m := newModel(items, []pair{{0, 1}}, blendOf(base), SharesOf(base).Array(), 1, 2, false)
	x := m.start()
	g := make([]float64, len(x))
	if got, want := m.objective(x, g), 0.11433237561621286; math.Abs(got-want) > 1e-12 {
		t.Fatalf("loss %.17g, want %.17g", got, want)
	}
}

func TestObjectiveGradientMatchesFiniteDifferences(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 7))
	base := jobinsight.DefaultWeights()
	var items []Item
	for i := range 40 {
		it, err := ItemFrom(string(rune('a'+i%26))+string(rune('a'+i/26)), jobinsight.ScoreWith(randomInputs(rng), base), Target{Grade: i % 3, Judged: true})
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, it)
	}
	var pairs []pair
	for i := range items {
		for j := range items {
			if items[i].Grade > items[j].Grade {
				pairs = append(pairs, pair{i, j})
			}
		}
	}
	for _, diag := range []bool{false, true} {
		m := newModel(items, pairs, blendOf(base), SharesOf(base).Array(), 0.7, float64(len(items)), diag)
		x := m.start()
		for i := range x {
			x[i] += 0.3 * (rng.Float64() - 0.5)
		}
		g := make([]float64, len(x))
		m.objective(x, g)
		for i := range x {
			h := 1e-6
			xp, xm := append([]float64(nil), x...), append([]float64(nil), x...)
			xp[i] += h
			xm[i] -= h
			scratch := make([]float64, len(x))
			fd := (m.objective(xp, scratch) - m.objective(xm, scratch)) / (2 * h)
			if math.Abs(fd-g[i]) > 1e-5*max(1, math.Abs(fd)) {
				t.Errorf("diag %v: d/dx[%d] analytic %.9g, finite difference %.9g", diag, i, g[i], fd)
			}
		}
	}
}

// randomInputs draws a posting across the cases ScoreWith distinguishes:
// each part known or not, kb_match asked or not, a preferred employer, an
// excluded title, mismatches, penalties, unsure, any age.
func randomInputs(rng *rand.Rand) jobinsight.Inputs {
	fits := []string{"strong_fit", "partial_fit", "adjacent", "mismatch_seniority", "mismatch_domain", "", "bogus"}
	slot := func(values []string) jobinsight.Slot {
		s := jobinsight.Slot{Value: values[rng.IntN(len(values))], Answered: rng.IntN(4) > 0, Backend: []string{"", "d1-ultra", "claude"}[rng.IntN(3)]}
		if s.Answered && rng.IntN(2) == 0 {
			s.P, s.HasP = rng.Float64(), true
		}
		s.Disagree = rng.IntN(5) == 0
		return s
	}
	in := jobinsight.Inputs{
		RoleFit:           slot(fits),
		Coverage:          100 * rng.Float64(),
		HasCoverage:       rng.IntN(3) > 0,
		PreferredEmployer: rng.IntN(4) == 0,
		TitleSignals:      rng.IntN(2) == 0,
		TitleRank:         rng.IntN(6) - 1,
		Target:            rng.IntN(2) == 0,
		TitleHits:         rng.IntN(4),
		BodyHits:          rng.IntN(8),
		Semantic:          rng.Float64(),
		HasSemantic:       rng.IntN(3) > 0,
		AgeKnown:          rng.IntN(2) == 0,
		AgeDays:           rng.IntN(60) - 2,
		SeenKnown:         rng.IntN(2) == 0,
		SeenDays:          rng.IntN(30),
		AgeNeutral:        rng.IntN(5) == 0,
		Unsure:            rng.IntN(6) == 0,
		Company:           jobinsight.CompanyMarks{Liked: rng.IntN(3), Hidden: rng.IntN(3)},
	}
	if rng.IntN(3) == 0 {
		in.KBMatch = slot([]string{"core", "partial", "adjacent", "unrelated", "nope"})
	}
	for range rng.IntN(3) {
		in.Penalties = append(in.Penalties, jobinsight.Penalty{Slot: slot([]string{"x"})})
	}
	return in
}

// The fit's match must be ScoreWith's, under any shares, or the shares it
// learns are for a different ranker. Items are built under the base weights
// and re-scored under other shares; ScoreWith is asked under the same ones.
func TestLogMatchIsScoreWithsMatch(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	bases := []jobinsight.Weights{jobinsight.DefaultWeights()}
	off := jobinsight.DefaultWeights()
	off.KnownMatch = false
	bases = append(bases, off)
	cal := jobinsight.DefaultWeights()
	cal.Calibration = map[string]jobinsight.Calibration{"d1-ultra": {A: 0.8, B: -0.2, UsesP: true}, "claude": {B: 1.1}}
	bases = append(bases, cal)
	for bi, base := range bases {
		for n := range 3000 {
			in := randomInputs(rng)
			it, err := ItemFrom("k", jobinsight.ScoreWith(in, base), Target{})
			if err != nil {
				t.Fatal(err)
			}
			w := base
			if n%2 == 1 { // other shares, some 0
				for _, p := range []*float64{&w.Fit, &w.Coverage, &w.Semantic, &w.Keyword, &w.Title, &w.Employer, &w.KBMatch} {
					*p = rng.Float64() * float64(rng.IntN(3))
				}
			}
			r := jobinsight.ScoreWith(in, w)
			s := SharesOf(w).Array()
			b := blendOf(w)
			got := math.Exp(b.logMatch(&it, &s, nil))
			if want := max(r.Match, logFloor); math.Abs(got-want) > 1e-12 {
				t.Fatalf("base %d case %d: match %.17g, ScoreWith %.17g (inputs %+v, shares %v)", bi, n, got, want, in, s)
			}
			if u, want := math.Exp(utility(&it, b, &s, 1, 1)), max(r.Match, logFloor)*max(r.Gate, logFloor)*max(r.Fresh, logFloor); math.Abs(u-want) > 1e-12 {
				t.Fatalf("base %d case %d: utility %.17g, ScoreWith match*gate*fresh %.17g", bi, n, u, want)
			}
		}
	}
}

func TestLogMatchGradientMatchesFiniteDifferences(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	base := jobinsight.DefaultWeights()
	b := blendOf(base)
	for n := range 500 {
		it, err := ItemFrom("k", jobinsight.ScoreWith(randomInputs(rng), base), Target{})
		if err != nil {
			t.Fatal(err)
		}
		var s, g [NumParts]float64
		for i := range s {
			s[i] = 0.05 + rng.Float64()
		}
		b.logMatch(&it, &s, &g)
		for i := range s {
			h := 1e-7
			sp, sm := s, s
			sp[i] += h
			sm[i] -= h
			fd := (b.logMatch(&it, &sp, nil) - b.logMatch(&it, &sm, nil)) / (2 * h)
			if math.Abs(fd-g[i]) > 1e-5*max(1, math.Abs(fd)) {
				t.Fatalf("case %d share %d: analytic %.9g, finite difference %.9g", n, i, g[i], fd)
			}
		}
	}
}

func TestItemFromRefusesMalformedResults(t *testing.T) {
	good := jobinsight.ScoreWith(jobinsight.Inputs{}, jobinsight.DefaultWeights())
	if _, err := ItemFrom("k", good, Target{}); err != nil {
		t.Fatalf("a real result: %v", err)
	}
	for name, mutate := range map[string]func(r *jobinsight.Result){
		"NaN factor":     func(r *jobinsight.Result) { r.Factors[1].Value = math.NaN() },
		"factor above 1": func(r *jobinsight.Result) { r.Factors[2].Value = 1.5 },
		"no factors":     func(r *jobinsight.Result) { r.Factors = nil },
		"no gate": func(r *jobinsight.Result) {
			var keep []jobinsight.Factor
			for _, f := range r.Factors {
				if f.Name != "gate" {
					keep = append(keep, f)
				}
			}
			r.Factors = keep
		},
		"no semantic": func(r *jobinsight.Result) {
			var keep []jobinsight.Factor
			for _, f := range r.Factors {
				if f.Name != "semantic" {
					keep = append(keep, f)
				}
			}
			r.Factors = keep
		},
	} {
		r := jobinsight.ScoreWith(jobinsight.Inputs{}, jobinsight.DefaultWeights())
		mutate(&r)
		if _, err := ItemFrom("k", r, Target{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// What feedback records (feedback.ScoresOf) rebuilds the Item ScoreWith ranked,
// except for the one case the record cannot carry: an unknown role fit whose
// option was set took the ordinal value, not the prior.
func TestItemFromScoresMatchesItemFrom(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	base := jobinsight.DefaultWeights()
	prior := blendOf(base).prior
	edge := 0
	for n := range 3000 {
		r := jobinsight.ScoreWith(randomInputs(rng), base)
		want, err := ItemFrom("k", r, Target{Grade: GradeMaybe, Judged: true})
		if err != nil {
			t.Fatal(err)
		}
		got, err := ItemFromScores("k", feedback.ScoresOf(r), base, Target{Grade: GradeMaybe, Judged: true})
		if err != nil {
			t.Fatal(err)
		}
		if !want.Known[PartFit] && want.Value[PartFit] != prior {
			edge++
			if got.Value[PartFit] != prior {
				t.Fatalf("case %d: unrecorded fit read as %v, want the prior", n, got.Value[PartFit])
			}
			want.Value[PartFit] = prior
		}
		if got != want {
			t.Fatalf("case %d:\n got %+v\nwant %+v", n, got, want)
		}
	}
	if edge == 0 {
		t.Error("the ordinal edge never came up: the test does not cover it")
	}
	for name, s := range map[string]feedback.Scores{
		"no gate":     {Factors: map[string]float64{"fresh": 1, "employer": 0}},
		"no fresh":    {Factors: map[string]float64{"gate": 1, "employer": 0}},
		"part over 1": {Factors: map[string]float64{"gate": 1, "fresh": 1, "coverage": 1.5}},
		"NaN part":    {Factors: map[string]float64{"gate": 1, "fresh": 1, "semantic": math.NaN()}},
		"conf over 1": {Factors: map[string]float64{"gate": 1, "fresh": 1, "semantic": 0.5, "semantic" + feedback.ConfSuffix: 1.5}},
		"NaN conf":    {Factors: map[string]float64{"gate": 1, "fresh": 1, "semantic": 0.5, "semantic" + feedback.ConfSuffix: math.NaN()}},
		"nil factors": {},
	} {
		if _, err := ItemFromScores("k", s, base, Target{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A line recorded before confidences were kept trusts each known part fully.
	old, err := ItemFromScores("k", feedback.Scores{Factors: map[string]float64{"gate": 1, "fresh": 1, "semantic": 0.5}}, base, Target{})
	if err != nil {
		t.Fatal(err)
	}
	if old.Conf[PartSemantic] != 1 || old.Conf[PartCoverage] != 0 || old.Conf[PartEmployer] != 1 {
		t.Errorf("legacy line confidences %v", old.Conf)
	}
}
