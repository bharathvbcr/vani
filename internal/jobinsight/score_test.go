package jobinsight

import (
	"math"
	"reflect"
	"testing"
)

func sl(value string, p float64) Slot { return Slot{Value: value, Answered: true, P: p, HasP: true} }

// baseIn is a posting with everything known at a middling level, so a test
// changes one thing at a time.
func baseIn() Inputs {
	return Inputs{
		RoleFit: sl("partial_fit", 0.8), Coverage: 60, HasCoverage: true,
		Semantic: 0.5, HasSemantic: true, Target: true, TitleHits: 1, BodyHits: 2,
		TitleSignals: true, TitleRank: 1, AgeKnown: true, AgeDays: 3,
	}
}

func inRange(t *testing.T, name string, r Result) {
	t.Helper()
	for k, v := range map[string]float64{"Score": r.Score, "Relevance": r.Relevance, "Match": r.Match, "Gate": r.Gate, "Fresh": r.Fresh, "Company": r.Company} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatalf("%s: %s is %v", name, k, v)
		}
	}
	if r.Score < 0 || r.Score > 100 || r.Relevance < 0 || r.Relevance > 100 {
		t.Fatalf("%s: score %v relevance %v out of range", name, r.Score, r.Relevance)
	}
	for _, f := range r.Factors {
		if math.IsNaN(f.Value) || math.IsNaN(f.Weight) || f.Value < 0 || f.Value > 1 {
			t.Fatalf("%s: factor %+v", name, f)
		}
	}
}

func TestScoreZeroInputs(t *testing.T) {
	r := Score(Inputs{})
	inRange(t, "zero", r)
	if r.Basis != BasisNone {
		t.Errorf("basis %q", r.Basis)
	}
	if r.Score <= 0 {
		t.Errorf("an unknown posting must still score above zero, got %v", r.Score)
	}
}

// The zero value of Inputs is "nothing is known": an unset age must not read
// as posted today, and a known negative age (clock skew) must not read as
// unknown.
func TestScoreAgeKnownZeroValue(t *testing.T) {
	zero := Score(Inputs{})
	unknown := Score(Inputs{AgeDays: -1})
	today := Score(Inputs{AgeKnown: true, AgeDays: 0})
	skewed := Score(Inputs{AgeKnown: true, AgeDays: -3})
	if zero.Fresh != unknown.Fresh || zero.Score != unknown.Score {
		t.Errorf("zero Inputs %+v differs from unknown age %+v", zero.Fresh, unknown.Fresh)
	}
	if zero.Score >= today.Score {
		t.Errorf("zero Inputs %v is not below a same-day posting %v", zero.Score, today.Score)
	}
	if skewed.Fresh != today.Fresh || skewed.Score != today.Score {
		t.Errorf("known age -3 scores %v (fresh %v), want the same-day %v (fresh %v)", skewed.Score, skewed.Fresh, today.Score, today.Fresh)
	}
}

func TestScoreOrderings(t *testing.T) {
	score := func(in Inputs) float64 { return Score(in).Score }

	t.Run("known strong fit without coverage outranks a known domain mismatch with coverage", func(t *testing.T) {
		a := Inputs{RoleFit: sl("strong_fit", 0.9), AgeKnown: true, AgeDays: 2}
		b := Inputs{RoleFit: sl("mismatch_domain", 0.9), Coverage: 90, HasCoverage: true, AgeKnown: true, AgeDays: 2}
		if score(a) <= score(b) {
			t.Errorf("strong %v <= mismatch %v", score(a), score(b))
		}
	})

	t.Run("unknown fit never beats a known partial fit or better", func(t *testing.T) {
		for _, v := range []string{"partial_fit", "strong_fit"} {
			known, unknown := baseIn(), baseIn()
			known.RoleFit = sl(v, 0.9)
			unknown.RoleFit = Slot{}
			if score(unknown) > score(known) {
				t.Errorf("unknown %v > %s %v", score(unknown), v, score(known))
			}
		}
	})

	t.Run("newer is no lower than older", func(t *testing.T) {
		prev := math.Inf(1)
		for age := 0; age < 400; age += 5 {
			in := baseIn()
			in.AgeDays = age
			s := score(in)
			if s > prev {
				t.Fatalf("age %d scores %v above a newer posting's %v", age, s, prev)
			}
			prev = s
		}
	})

	t.Run("known age is no lower than unknown age", func(t *testing.T) {
		known, unknown := baseIn(), baseIn()
		known.AgeDays, unknown.AgeKnown, unknown.AgeDays = 400, false, -1
		if score(known) < score(unknown) {
			t.Errorf("known %v < unknown %v", score(known), score(unknown))
		}
	})

	t.Run("a surer penalty lowers the score more", func(t *testing.T) {
		prev := score(baseIn())
		for _, p := range []float64{0.2, 0.5, 0.9} {
			in := baseIn()
			in.Penalties = []Penalty{{Slot: sl("x", p)}}
			s := score(in)
			if s >= prev {
				t.Fatalf("penalty p=%v scores %v, not below %v", p, s, prev)
			}
			prev = s
		}
	})

	t.Run("hidden marks never raise, liked marks never lower", func(t *testing.T) {
		prevH, prevL := score(baseIn()), score(baseIn())
		for n := 1; n <= 40; n++ {
			h, l := baseIn(), baseIn()
			h.Company.Hidden, l.Company.Liked = n, n
			if s := score(h); s > prevH {
				t.Fatalf("%d hidden raised %v to %v", n, prevH, s)
			} else {
				prevH = s
			}
			if s := score(l); s < prevL {
				t.Fatalf("%d liked lowered %v to %v", n, prevL, s)
			} else {
				prevL = s
			}
		}
		// From any starting counts, not only from zero: a liked mark never
		// lowers the factor and a hidden one never raises it (no cliff at a
		// minimum mark count).
		at := func(l, h int) float64 {
			in := baseIn()
			in.Company = CompanyMarks{Liked: l, Hidden: h}
			return Score(in).Company
		}
		for l := 0; l <= 12; l++ {
			for h := 0; h <= 12; h++ {
				if at(l+1, h) < at(l, h) {
					t.Fatalf("liked %d->%d with %d hidden lowered the factor %v -> %v", l, l+1, h, at(l, h), at(l+1, h))
				}
				if at(l, h+1) > at(l, h) {
					t.Fatalf("hidden %d->%d with %d liked raised the factor %v -> %v", h, h+1, l, at(l, h), at(l, h+1))
				}
			}
		}
	})

	t.Run("a company with 10000 marks stays bounded", func(t *testing.T) {
		for _, m := range []CompanyMarks{{10000, 0}, {0, 10000}, {5000, 5000}, {math.MaxInt, math.MaxInt}} {
			in := baseIn()
			in.Company = m
			c := Score(in).Company
			if c < CompanyMin-1e-12 || c > CompanyMax+1e-12 {
				t.Errorf("%+v gives company %v", m, c)
			}
		}
	})

	t.Run("disagreement lowers fit confidence", func(t *testing.T) {
		a, b := baseIn(), baseIn()
		b.RoleFit.Disagree = true
		if score(b) >= score(a) {
			t.Errorf("disagree %v >= agree %v", score(b), score(a))
		}
		a.RoleFit, b.RoleFit = Slot{Value: "strong_fit", Answered: true}, Slot{Value: "strong_fit", Answered: true, Disagree: true}
		if score(b) >= score(a) {
			t.Errorf("disagree without p %v >= agree %v", score(b), score(a))
		}
	})

	t.Run("unsure lowers the score", func(t *testing.T) {
		a, b := baseIn(), baseIn()
		b.Unsure = true
		if score(b) >= score(a) {
			t.Errorf("unsure %v >= %v", score(b), score(a))
		}
	})

	t.Run("an excluded title lowers it", func(t *testing.T) {
		a, b := baseIn(), baseIn()
		b.TitleRank = -1
		if score(b) >= score(a) {
			t.Errorf("excluded %v >= %v", score(b), score(a))
		}
		if g := Score(b).Gate; g != DefaultWeights().ExcludedTitle {
			t.Errorf("gate %v", g)
		}
	})

	t.Run("a fit value outside the vocabulary is unknown", func(t *testing.T) {
		in := baseIn()
		in.RoleFit = sl("nonsense", 1)
		r := Score(in)
		if r.Factors[0].Known {
			t.Error("fit known for an unknown option")
		}
	})

	t.Run("a preferred employer helps", func(t *testing.T) {
		a, b := baseIn(), baseIn()
		b.PreferredEmployer = true
		if score(b) <= score(a) {
			t.Errorf("%v <= %v", score(b), score(a))
		}
	})
}

func TestScoreHalfLifeFallback(t *testing.T) {
	in := baseIn()
	in.AgeKnown, in.AgeDays = true, 14
	want := Score(in)
	for _, h := range []float64{0, -1, 400, math.NaN(), math.Inf(1)} {
		in.HalfLifeDays = h
		if got := Score(in); got.Score != want.Score || got.Fresh != want.Fresh {
			t.Errorf("half-life %v: %+v, want the default's %+v", h, got.Fresh, want.Fresh)
		}
	}
	in.HalfLifeDays = 100
	if Score(in).Fresh <= want.Fresh {
		t.Error("a longer half-life should age slower")
	}
}

func TestScoreHostileInputs(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	cases := map[string]Inputs{
		"nan everywhere": {
			RoleFit: Slot{Value: "strong_fit", Answered: true, P: nan, HasP: true}, Coverage: nan, HasCoverage: true,
			Semantic: nan, HasSemantic: true, Target: true, TitleHits: -5, BodyHits: -5, TitleSignals: true, TitleRank: -9,
			HalfLifeDays: nan, Company: CompanyMarks{-3, -4}, Penalties: []Penalty{{Slot{P: nan, HasP: true, Answered: true}}},
		},
		"inf everywhere": {
			RoleFit: Slot{Value: "strong_fit", P: inf, HasP: true}, Coverage: inf, HasCoverage: true,
			Semantic: -inf, HasSemantic: true, Target: true, TitleHits: math.MaxInt, BodyHits: math.MaxInt,
			TitleSignals: true, TitleRank: math.MaxInt, AgeKnown: true, AgeDays: math.MaxInt, HalfLifeDays: inf,
			Penalties: []Penalty{{Slot{P: inf, HasP: true}}, {Slot{P: -inf, HasP: true}}},
		},
		"negative and huge": {
			RoleFit: sl("strong_fit", -3), Coverage: -50, HasCoverage: true, Semantic: 9, HasSemantic: true,
			AgeKnown: true, AgeDays: math.MinInt, TitleRank: 1 << 40,
		},
		"many penalties": {Penalties: make([]Penalty, 5000)},
	}
	for name, in := range cases {
		inRange(t, name, Score(in))
	}

	hostile := Weights{Fit: nan, Coverage: -1, Semantic: inf, Keyword: 0, Title: 0, Employer: 0, UnknownPrior: nan,
		AnsweredConf: 9, DisagreeConf: -2, PenaltyStrength: nan, ExcludedTitle: 9, UnsureGate: nan, FreshFloor: -3,
		HalfLifeDays: nan, CompanyMinMarks: -1, CompanySmoothing: -9, CompanySpan: nan}
	for name, in := range cases {
		inRange(t, name+" hostile weights", ScoreWith(in, hostile))
	}
	inRange(t, "zero weights", ScoreWith(baseIn(), Weights{}))
}

func TestScoreFactors(t *testing.T) {
	for _, in := range []Inputs{{}, baseIn()} {
		r := Score(in)
		names := []string{"fit", "coverage", "semantic", "keyword", "title", "employer", "gate", "fresh", "company"}
		if len(r.Factors) != len(names) {
			t.Fatalf("%d factors", len(r.Factors))
		}
		var share float64
		for i, f := range r.Factors {
			if f.Name != names[i] {
				t.Errorf("factor %d is %q, want %q", i, f.Name, names[i])
			}
			if i < 6 {
				share += f.Weight
			} else if f.Weight != 0 || !f.Known {
				t.Errorf("multiplier %+v", f)
			}
		}
		if math.Abs(share-1) > 1e-9 {
			t.Errorf("additive shares sum to %v", share)
		}
	}
	a, b := Score(baseIn()), Score(baseIn())
	a.Factors[0].Value = 99
	if b.Factors[0].Value == 99 || Score(baseIn()).Factors[0].Value == 99 {
		t.Error("Factors shares storage between calls")
	}
}

func TestScoreBasis(t *testing.T) {
	cases := []struct {
		name string
		in   Inputs
		want Basis
	}{
		{"nothing", Inputs{}, BasisNone},
		{"keywords only", Inputs{Target: true, TitleHits: 3}, BasisNone},
		{"fit", Inputs{RoleFit: sl("strong_fit", 0.9)}, BasisFit},
		{"coverage", Inputs{Coverage: 95, HasCoverage: true}, BasisRank},
		{"semantic", Inputs{Semantic: 0.99, HasSemantic: true}, BasisSemantic},
		{"strong coverage beats a weak fit", Inputs{RoleFit: sl("mismatch_domain", 0.9), Coverage: 100, HasCoverage: true}, BasisRank},
	}
	for _, c := range cases {
		if got := Score(c.in).Basis; got != c.want {
			t.Errorf("%s: basis %q, want %q", c.name, got, c.want)
		}
	}
}

func TestScoreDeterministicAndPure(t *testing.T) {
	in := baseIn()
	in.Penalties = []Penalty{{Slot: sl("x", 0.4)}}
	before := in
	a, b := Score(in), Score(in)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("two calls differ: %+v vs %+v", a, b)
	}
	if !reflect.DeepEqual(before, in) {
		t.Error("Score modified its input")
	}
}

func TestScoreWithNormalizesWeights(t *testing.T) {
	w := DefaultWeights()
	in := baseIn()
	want := ScoreWith(in, w)
	w.Fit, w.Coverage, w.Semantic, w.Keyword, w.Title, w.Employer = w.Fit*10, w.Coverage*10, w.Semantic*10, w.Keyword*10, w.Title*10, w.Employer*10
	if got := ScoreWith(in, w); math.Abs(got.Match-want.Match) > 1e-12 {
		t.Errorf("scaling every weight moved match from %v to %v", want.Match, got.Match)
	}
	if got := ScoreWith(in, Weights{}).Match; got != 0 {
		t.Errorf("all weights skipped and prior 0: match %v, want the prior", got)
	}
}

func TestScoreRelevanceIsAgeFree(t *testing.T) {
	a, b := baseIn(), baseIn()
	a.AgeDays, b.AgeDays = 0, 300
	a.AgeKnown, b.AgeKnown = true, true
	ra, rb := Score(a), Score(b)
	if ra.Relevance != rb.Relevance {
		t.Errorf("relevance moved with age: %v vs %v", ra.Relevance, rb.Relevance)
	}
	if ra.Score <= rb.Score {
		t.Errorf("score did not: %v vs %v", ra.Score, rb.Score)
	}
}
