package rankeval

import (
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

const eps = 1e-9

// good is a typical, fully observed posting: a partial fit read by Jev, middling
// coverage, some keyword and semantic evidence, 10 days old.
func good() jobinsight.Inputs {
	return jobinsight.Inputs{
		RoleFit:      jobinsight.Slot{Value: "partial_fit", Answered: true, P: 0.7, HasP: true},
		Coverage:     60,
		HasCoverage:  true,
		TitleSignals: true,
		TitleRank:    1,
		Target:       true,
		TitleHits:    1,
		BodyHits:     3,
		Semantic:     0.6,
		HasSemantic:  true,
		AgeKnown:     true,
		AgeDays:      10,
	}
}

func fit(v string, p float64) jobinsight.Slot {
	return jobinsight.Slot{Value: v, Answered: true, P: p, HasP: true}
}

func finite01(v, hi float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= hi }

// invariantViolations returns every hard invariant the ranker breaks under w.
// An empty slice means none. The invariants are properties any sane weights
// must keep; the sweep discards variants that break one.
func invariantViolations(w jobinsight.Weights) []string {
	var v []string
	bad := func(format string, a ...any) { v = append(v, fmt.Sprintf(format, a...)) }
	s := func(in jobinsight.Inputs) float64 { return jobinsight.ScoreWith(in, w).Score }

	// A known age never scores below an unknown one, however old. This is the
	// nothing-known case (no SeenKnown). Where InputsFor knows when Vani first
	// saw an undated posting, Score deliberately scores it as that old plus
	// UnknownAgeDays, so a known age older than that does lose to it
	// (jobinsight/unknownage_test.go).
	for _, age := range []int{0, 1, 10, 60, 180, 1000} {
		k, u := good(), good()
		k.AgeDays, u.AgeKnown, u.AgeDays = age, false, -1
		if s(k) < s(u)-eps {
			bad("known age %d scores %.4f below unknown age %.4f", age, s(k), s(u))
		}
	}
	// Newer never scores below older.
	prev := math.Inf(1)
	for age := 0; age <= 500; age++ {
		in := good()
		in.AgeDays = age
		cur := s(in)
		if cur > prev+eps {
			bad("age %d scores %.6f above age %d scores %.6f", age, cur, age-1, prev)
			break
		}
		prev = cur
	}
	// An undated posting of an employer that closes by application cap is never
	// aged by when Vani first saw it, never scores below an ordinary undated
	// posting seen as long ago, and never above the same posting dated today.
	{
		today := good()
		today.AgeDays = 0
		for _, seen := range []int{0, 1, 10, 60, 400} {
			neutral, plain := good(), good()
			neutral.AgeKnown, neutral.AgeDays, neutral.SeenKnown, neutral.SeenDays, neutral.AgeNeutral = false, -1, true, seen, true
			plain.AgeKnown, plain.AgeDays, plain.SeenKnown, plain.SeenDays = false, -1, true, seen
			if s(neutral) < s(plain)-eps {
				bad("age-neutral undated posting seen %d days ago scores %.4f below an ordinary one %.4f", seen, s(neutral), s(plain))
			}
			if s(neutral) > s(today)+eps {
				bad("age-neutral undated posting seen %d days ago scores %.4f above one dated today %.4f", seen, s(neutral), s(today))
			}
			base := good()
			base.AgeKnown, base.AgeDays, base.SeenKnown, base.SeenDays, base.AgeNeutral = false, -1, true, 0, true
			if d := s(neutral) - s(base); d > eps || d < -eps {
				bad("age-neutral undated posting seen %d days ago scores %.4f, not the flat %.4f", seen, s(neutral), s(base))
			}
		}
	}
	// A language model's read of the work orders by how close it says the work is
	// (no option scores above a closer one) and never gates.
	{
		prev := math.Inf(1)
		for _, opt := range jobinsight.KBMatchOrder {
			in := good()
			in.KBMatch = jobinsight.Slot{Value: opt, Answered: true}
			cur := s(in)
			if cur > prev+eps {
				bad("kb_match %q scores %.4f above a closer option's %.4f", opt, cur, prev)
			}
			prev = cur
			if g := jobinsight.ScoreWith(in, w).Gate; g != jobinsight.ScoreWith(good(), w).Gate {
				bad("kb_match %q moved the gate to %.4f", opt, g)
			}
		}
	}
	// A strong fit with no coverage beats a domain mismatch with high coverage.
	{
		a, b := good(), good()
		a.RoleFit, a.HasCoverage, a.Coverage = fit("strong_fit", 0.8), false, 0
		b.RoleFit, b.Coverage = fit("mismatch_domain", 0.8), 90
		if !(s(a) > s(b)) {
			bad("strong_fit without coverage (%.3f) does not beat mismatch_domain with coverage 90 (%.3f)", s(a), s(b))
		}
	}
	// An unknown fit is below a known partial fit.
	{
		a, b := good(), good()
		a.RoleFit = jobinsight.Slot{}
		b.RoleFit = fit("partial_fit", 0.7)
		if !(s(a) < s(b)) {
			bad("unknown fit (%.3f) is not below partial_fit (%.3f)", s(a), s(b))
		}
	}
	// A more confident kept-by drop rule never raises the score, and the
	// extremes differ.
	{
		prev := math.Inf(1)
		first, last := 0.0, 0.0
		for i, p := range []float64{0, 0.2, 0.4, 0.6, 0.8, 0.95, 1} {
			in := good()
			in.Penalties = []jobinsight.Penalty{{Slot: jobinsight.Slot{Value: "x", Answered: true, P: p, HasP: true}}}
			cur := s(in)
			if cur > prev+eps {
				bad("penalty confidence %.2f scores %.4f above a less confident penalty %.4f", p, cur, prev)
			}
			if i == 0 {
				first = cur
			}
			last, prev = cur, cur
		}
		if !(last < first) {
			bad("penalty confidence 1 (%.4f) does not score below confidence 0 (%.4f)", last, first)
		}
		none := s(good())
		if first > none+eps {
			bad("a penalty of confidence 0 raises the score (%.4f > %.4f)", first, none)
		}
	}
	// Hidden marks never raise the score; liked marks never lower it.
	{
		base := s(good())
		prevH, prevL := base, base
		for _, n := range []int{1, 2, 3, 5, 10, 100, 10000} {
			h, l := good(), good()
			h.Company.Hidden, l.Company.Liked = n, n
			sh, sl := s(h), s(l)
			if sh > prevH+eps {
				bad("%d hidden marks raise the score (%.4f > %.4f)", n, sh, prevH)
			}
			if sl < prevL-eps {
				bad("%d liked marks lower the score (%.4f < %.4f)", n, sl, prevL)
			}
			prevH, prevL = sh, sl
		}
	}
	// The company factor is bounded even with 10,000 marks.
	for _, m := range []jobinsight.CompanyMarks{{Liked: 10000}, {Hidden: 10000}, {Liked: 10000, Hidden: 10000}, {Liked: 1 << 40}} {
		in := good()
		in.Company = m
		r := jobinsight.ScoreWith(in, w)
		lo, hi := 1-w.CompanyDown-eps, 1+w.CompanySpan+eps
		if !(r.Company >= lo && r.Company <= hi) || math.IsNaN(r.Company) {
			bad("company factor %.4f with marks %+v outside [1-%.2f, 1+%.2f]", r.Company, m, w.CompanyDown, w.CompanySpan)
		}
		if !finite01(r.Score, 100) {
			bad("score %v with marks %+v is not in [0,100]", r.Score, m)
		}
	}
	// Scores are finite and in [0,100] for hostile inputs.
	for _, f := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, -1e300, 1e300, 105, 0} {
		in := good()
		in.Coverage, in.Semantic = f, f
		in.RoleFit.P = f
		in.HalfLifeDays = f
		in.Penalties = []jobinsight.Penalty{{Slot: jobinsight.Slot{Value: "x", Answered: true, P: f, HasP: true}}}
		in.TitleRank = -1 << 40
		in.TitleHits, in.BodyHits = -1<<62, math.MaxInt
		in.AgeDays = math.MinInt
		in.Company = jobinsight.CompanyMarks{Liked: -5, Hidden: math.MaxInt}
		r := jobinsight.ScoreWith(in, w)
		if !finite01(r.Score, 100) || !finite01(r.Relevance, 100) {
			bad("hostile value %v: score %v relevance %v not in [0,100]", f, r.Score, r.Relevance)
		}
		for name, x := range map[string]float64{"match": r.Match, "gate": r.Gate, "fresh": r.Fresh} {
			if !finite01(x, 1) {
				bad("hostile value %v: %s = %v not in [0,1]", f, name, x)
			}
		}
		if math.IsNaN(r.Company) || math.IsInf(r.Company, 0) || r.Company < 0 {
			bad("hostile value %v: company factor %v", f, r.Company)
		}
		in.AgeDays = math.MaxInt
		in.TitleRank = 1 << 40
		in.TitleHits = math.MaxInt
		if r := jobinsight.ScoreWith(in, w); !finite01(r.Score, 100) {
			bad("hostile value %v with huge counts: score %v", f, r.Score)
		}
	}
	// Removing the target summary preserves the relative order of postings
	// that differ only in fit.
	for _, p := range []float64{0.5, 0.7, 0.9} {
		for _, a := range roleFits {
			for _, b := range roleFits {
				x, y := good(), good()
				x.RoleFit, y.RoleFit = fit(a, p), fit(b, p)
				with := sign(s(x) - s(y))
				x.Target, x.TitleHits, x.BodyHits = false, 0, 0
				y.Target, y.TitleHits, y.BodyHits = false, 0, 0
				without := sign(s(x) - s(y))
				if with != without {
					bad("%s vs %s at p=%.1f: order %d with a target summary, %d without", a, b, p, with, without)
				}
			}
		}
	}
	// Deterministic, and does not mutate its input.
	{
		in := good()
		in.Penalties = []jobinsight.Penalty{{Slot: fit("x", 0.6)}}
		cp := in
		cp.Penalties = append([]jobinsight.Penalty(nil), in.Penalties...)
		first := jobinsight.ScoreWith(in, w)
		for range 5 {
			if again := jobinsight.ScoreWith(in, w); !reflect.DeepEqual(first, again) {
				bad("non-deterministic: %+v then %+v", first, again)
				break
			}
		}
		if !reflect.DeepEqual(in, cp) {
			bad("ScoreWith mutated its input")
		}
	}
	return v
}

func sign(x float64) int {
	switch {
	case x > eps:
		return 1
	case x < -eps:
		return -1
	}
	return 0
}

func TestDefaultWeightsKeepInvariants(t *testing.T) {
	if v := invariantViolations(jobinsight.DefaultWeights()); len(v) != 0 {
		for _, x := range v {
			t.Errorf("invariant violated: %s", x)
		}
	}
}

// TestInvariantCheckerDetects proves invariantViolations is not vacuous: the
// zero Weights (the old stub)
// must be reported.
func TestInvariantCheckerDetects(t *testing.T) {
	if v := invariantViolations(jobinsight.Weights{}); len(v) == 0 {
		t.Error("zero Weights violate no invariant; the checker is vacuous")
	} else {
		t.Logf("zero Weights: %d violations, first: %s", len(v), v[0])
	}
}
