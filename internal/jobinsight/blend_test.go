package jobinsight

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
)

// sameBits reports whether a and b are the same float64, bit for bit.
func sameBits(a, b float64) bool { return math.Float64bits(a) == math.Float64bits(b) }

// diffResult names the first field where got and want differ by even one bit.
func diffResult(got, want Result) string {
	for _, f := range []struct {
		name string
		a, b float64
	}{{"Score", got.Score, want.Score}, {"Relevance", got.Relevance, want.Relevance},
		{"RelevanceKnown", got.RelevanceKnown, want.RelevanceKnown}, {"Match", got.Match, want.Match},
		{"Gate", got.Gate, want.Gate}, {"Fresh", got.Fresh, want.Fresh}, {"Company", got.Company, want.Company}} {
		if !sameBits(f.a, f.b) {
			return fmt.Sprintf("%s %v (%#x) != %v (%#x)", f.name, f.a, math.Float64bits(f.a), f.b, math.Float64bits(f.b))
		}
	}
	if got.Basis != want.Basis {
		return fmt.Sprintf("Basis %q != %q", got.Basis, want.Basis)
	}
	if len(got.Factors) != len(want.Factors) {
		return fmt.Sprintf("%d factors != %d", len(got.Factors), len(want.Factors))
	}
	for i, g := range got.Factors {
		w := want.Factors[i]
		if g.Name != w.Name || !sameBits(g.Value, w.Value) || !sameBits(g.Weight, w.Weight) || g.Known != w.Known ||
			!sameBits(g.Conf, w.Conf) || g.Note != w.Note || g.Backend != w.Backend || g.Reading != w.Reading {
			return fmt.Sprintf("factor %d: %+v != %+v", i, g, w)
		}
	}
	return ""
}

var (
	fitOptions = []string{"", "strong_fit", "partial_fit", "adjacent", "mismatch_seniority", "mismatch_domain", "weird"}
	kbOptions  = []string{"", "core", "partial", "adjacent", "unrelated", "other"}
	backends   = []string{"", "d1-ultra", ShadowEscalate, BackendManual, "lappi"}
	oddFloats  = []float64{0, -0, 1, -1, 0.5, 100, 150, -20, 1e-300, 1e300, math.NaN(), math.Inf(1), math.Inf(-1)}
)

func randFloat(r *rand.Rand, lo, hi float64) float64 {
	if r.IntN(12) == 0 {
		return oddFloats[r.IntN(len(oddFloats))]
	}
	return lo + r.Float64()*(hi-lo)
}

func randSlot(r *rand.Rand, opts []string) Slot {
	return Slot{Value: opts[r.IntN(len(opts))], Answered: r.IntN(3) > 0, P: randFloat(r, 0, 1), HasP: r.IntN(2) == 0,
		Disagree: r.IntN(5) == 0, ShadowBackend: backends[r.IntN(len(backends))], Backend: backends[r.IntN(len(backends))]}
}

func randInputs(r *rand.Rand) Inputs {
	in := Inputs{
		RoleFit: randSlot(r, fitOptions), Coverage: randFloat(r, 0, 100), HasCoverage: r.IntN(2) == 0,
		PreferredEmployer: r.IntN(4) == 0, TitleSignals: r.IntN(2) == 0, TitleRank: r.IntN(6) - 1,
		Target: r.IntN(2) == 0, TitleHits: r.IntN(8) - 1, BodyHits: r.IntN(20) - 1,
		Semantic: randFloat(r, 0, 1), HasSemantic: r.IntN(2) == 0,
		AgeKnown: r.IntN(2) == 0, AgeDays: r.IntN(400) - 5, SeenKnown: r.IntN(2) == 0, SeenDays: r.IntN(400) - 5,
		AgeNeutral: r.IntN(4) == 0, KBMatch: randSlot(r, kbOptions), Unsure: r.IntN(6) == 0,
		Company:      CompanyMarks{Liked: r.IntN(12) - 1, Hidden: r.IntN(5)},
		HalfLifeDays: []float64{0, 7, 10, 30, -1, 1e9, math.NaN()}[r.IntN(7)],
	}
	for range r.IntN(4) {
		in.Penalties = append(in.Penalties, Penalty{Slot: randSlot(r, fitOptions)})
	}
	return in
}

func randWeights(r *rand.Rand) Weights {
	w := DefaultWeights()
	if r.IntN(3) == 0 {
		return w
	}
	w.Fit, w.Coverage, w.Semantic = randFloat(r, 0, 1), randFloat(r, 0, 1), randFloat(r, 0, 1)
	w.Keyword, w.Title, w.Employer, w.KBMatch = randFloat(r, 0, 1), randFloat(r, 0, 1), randFloat(r, 0, 1), randFloat(r, 0, 1)
	w.UnknownPrior, w.AnsweredConf, w.DisagreeConf = randFloat(r, 0, 1), randFloat(r, 0, 1), randFloat(r, 0, 1)
	w.PenaltyStrength, w.Mismatch, w.ExcludedTitle, w.UnsureGate = randFloat(r, 0, 1), randFloat(r, 0, 1), randFloat(r, 0, 1), randFloat(r, 0, 1)
	w.FreshFloor, w.HalfLifeDays, w.UnknownAgeDays, w.NeutralFresh = randFloat(r, 0, 1), randFloat(r, 0, 60), randFloat(r, -5, 60), randFloat(r, -0.5, 1.5)
	w.CompanyMinMarks, w.CompanySmoothing, w.CompanySpan = r.IntN(4), randFloat(r, -2, 20), randFloat(r, -0.5, 1.5)
	w.KnownMatch, w.MatchShrink = r.IntN(2) == 0, randFloat(r, -0.5, 2)
	if r.IntN(3) == 0 {
		for i := range w.FitValues {
			w.FitValues[i] = randFloat(r, 0, 1)
		}
	}
	if r.IntN(3) == 0 {
		w.Calibration = map[string]Calibration{backends[r.IntN(len(backends))]: {A: randFloat(r, -3, 3), B: randFloat(r, -3, 3), UsesP: r.IntN(2) == 0}}
	}
	return w
}

// The two-stage score is the pre-split ScoreWith to the bit, field by field
// and factor by factor, over random readings and weights (the edge values
// included), and Order is Close without the factor list.
func TestBlendCloseIsScoreWithBitForBit(t *testing.T) {
	r := rand.New(rand.NewPCG(20261111, 1))
	n := 200_000
	if testing.Short() {
		n = 20_000
	}
	for i := range n {
		in, w := randInputs(r), randWeights(r)
		want := scoreWithOracle(in, w)
		if d := diffResult(ScoreWith(in, w), want); d != "" {
			t.Fatalf("case %d: ScoreWith differs from the oracle: %s\ninputs %+v\nweights %+v", i, d, in, w)
		}
		b := BlendOf(in, w)
		c := ClosingOf(in)
		if d := diffResult(b.Close(c, w), want); d != "" {
			t.Fatalf("case %d: Close differs: %s", i, d)
		}
		order, wantOrder := b.Order(c, w), want
		wantOrder.Factors = nil
		if d := diffResult(order, wantOrder); d != "" {
			t.Fatalf("case %d: Order differs: %s", i, d)
		}
		// A kept Blend closes alike under another clock and other marks as a
		// fresh score with those.
		other := in
		other.AgeDays, other.SeenDays, other.Company.Liked, other.HalfLifeDays = in.AgeDays+r.IntN(30), in.SeenDays+3, r.IntN(9), 7
		if d := diffResult(b.Close(ClosingOf(other), w), scoreWithOracle(other, w)); d != "" {
			t.Fatalf("case %d: a kept blend closed under another clock differs: %s", i, d)
		}
	}
}

// BlendOf reads none of the fields Close reads: two inputs that differ only
// there blend alike.
func TestBlendReadsNoClockOrMarks(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 7))
	for range 5000 {
		in, w := randInputs(r), randWeights(r)
		other := in
		other.AgeKnown, other.AgeDays, other.SeenKnown, other.SeenDays = !in.AgeKnown, in.AgeDays+40, !in.SeenKnown, in.SeenDays+40
		other.AgeNeutral, other.HalfLifeDays, other.Company = !in.AgeNeutral, 3, CompanyMarks{Liked: 7, Hidden: 2}
		a, b := BlendOf(in, w), BlendOf(other, w)
		if fmt.Sprintf("%#v", a) != fmt.Sprintf("%#v", b) {
			t.Fatalf("the blend read a clock or marks field:\n%#v\n%#v", a, b)
		}
	}
}
