package jobinsight

import (
	"math"
	"testing"
)

// A confident strong fit with nothing else known must not be shown as ~50:
// relevance is from what is known.
func TestRelevanceFromKnownPartsOnly(t *testing.T) {
	// Shrinkage toward the prior (relevanceShrink) keeps a fit-only reading a
	// little short of a fully evidenced one, but far above the ~50 the
	// prior-filled match would show.
	only := Score(Inputs{RoleFit: sl("strong_fit", 0.95)})
	if only.Relevance < 70 {
		t.Errorf("fit-only strong fit shows %v, want >= 70", only.Relevance)
	}
	dw := DefaultWeights()
	total := dw.Fit + dw.Coverage + dw.Semantic + dw.Keyword + dw.Title + dw.Employer
	if want := dw.Fit / total; math.Abs(only.RelevanceKnown-want) > 1e-9 {
		t.Errorf("fit-only RelevanceKnown %v, want %v (the fit share; an unpreferred employer is not evidence)", only.RelevanceKnown, want)
	}

	full := Score(Inputs{
		RoleFit: sl("strong_fit", 0.95), Coverage: 90, HasCoverage: true,
		Semantic: 0.9, HasSemantic: true, Target: true, TitleHits: 2, BodyHits: 6,
		TitleSignals: true, TitleRank: 2, PreferredEmployer: true,
	})
	if full.Relevance < 80 {
		t.Errorf("everything known and matching shows %v, want >= 80", full.Relevance)
	}
	if math.Abs(full.RelevanceKnown-1) > 1e-9 {
		t.Errorf("full RelevanceKnown %v, want 1", full.RelevanceKnown)
	}
}

// Under KnownMatch (the default) the order rests on the known parts shrunk
// toward the prior; with it off, every unknown part takes the prior inside
// the match, as before. Either way an unknown posting ranks below a known
// good one.
func TestMatchModes(t *testing.T) {
	in := Inputs{RoleFit: Slot{Value: "strong_fit", Answered: true, P: 0.95, HasP: true}, AgeKnown: true, AgeDays: 2}
	w := DefaultWeights()
	fitVal, rest, _ := w.fitValue("strong_fit")
	fit := 0.95*fitVal + 0.05*rest // ordinal: the rest leans on the neighbours
	known := Score(in)
	// Shares are the weights over their sum; only fit is known, and the shrink
	// pulls it toward the prior by MatchShrink.
	sum := w.Fit + w.Coverage + w.Semantic + w.Keyword + w.Title + w.Employer
	fitShare := w.Fit / sum
	if want := (fitShare*fit + w.MatchShrink*w.UnknownPrior) / (fitShare + w.MatchShrink); math.Abs(known.Match-want) > 1e-9 {
		t.Errorf("KnownMatch: Match %v, want %v", known.Match, want)
	}
	w.KnownMatch = false
	diluted := ScoreWith(in, w)
	// fit share x fit + prior x (coverage + semantic + keyword + title shares) + employer share x 0
	if want := fitShare*fit + w.UnknownPrior*(w.Coverage+w.Semantic+w.Keyword+w.Title)/sum; math.Abs(diluted.Match-want) > 1e-9 {
		t.Errorf("prior-filled: Match %v, want %v", diluted.Match, want)
	}
	if !(known.Score > diluted.Score) {
		t.Errorf("a confident strong fit should rank higher without dilution: %v vs %v", known.Score, diluted.Score)
	}
	unknown := Score(Inputs{AgeKnown: true, AgeDays: 2})
	partial := Score(Inputs{RoleFit: Slot{Value: "partial_fit", Answered: true}, AgeKnown: true, AgeDays: 2})
	if !(unknown.Score < partial.Score) {
		t.Errorf("unknown %v must rank below a known partial fit %v", unknown.Score, partial.Score)
	}
}

// Read as ordinal, role_fit stays monotone: at any confidence a better option
// is worth at least as much as a worse one, more confidence in a good option
// never lowers the fit and more confidence in a bad one never raises it.
func TestOrdinalFitMonotone(t *testing.T) {
	fitOf := func(v string, p float64) float64 {
		return Score(Inputs{RoleFit: Slot{Value: v, Answered: true, P: p, HasP: true}}).Factors[0].Value
	}
	for _, p := range []float64{0.05, 0.2, 0.44, 0.7, 1} {
		for i := 1; i < len(fitOrder); i++ {
			if better, worse := fitOf(fitOrder[i-1], p), fitOf(fitOrder[i], p); better < worse {
				t.Errorf("p %.2f: %s %.3f below %s %.3f", p, fitOrder[i-1], better, fitOrder[i], worse)
			}
		}
	}
	for _, v := range []string{"strong_fit", "partial_fit"} {
		if fitOf(v, 0.9) < fitOf(v, 0.3) {
			t.Errorf("more confidence in %s lowered its fit", v)
		}
	}
	for _, v := range []string{"mismatch_seniority", "mismatch_domain"} {
		if fitOf(v, 0.9) > fitOf(v, 0.3) {
			t.Errorf("more confidence in %s raised its fit", v)
		}
	}
}

// With nothing known besides the employer, relevance falls back to the
// prior-filled formula and says it rests on nothing.
func TestRelevanceFallsBackWhenNothingIsKnown(t *testing.T) {
	for name, in := range map[string]Inputs{"zero": {}, "preferred employer only": {PreferredEmployer: true}} {
		r := Score(in)
		if want := round1(100 * r.Match * r.Gate * r.Company); r.Relevance != want {
			t.Errorf("%s: Relevance %v, want the prior-filled %v", name, r.Relevance, want)
		}
		if r.RelevanceKnown != 0 && !in.PreferredEmployer {
			t.Errorf("%s: RelevanceKnown %v, want 0", name, r.RelevanceKnown)
		}
		inRange(t, name, r)
	}
}

// One thin known part must not read as a confident zero: a posting whose only
// known part is zero target-keyword overlap shows above a confident mismatch.
func TestRelevanceThinKnownPartDoesNotSinkBelowMismatch(t *testing.T) {
	thin := Score(Inputs{Target: true})
	mismatch := Score(Inputs{RoleFit: sl("mismatch_domain", 0.9)})
	if !(thin.Relevance > mismatch.Relevance) {
		t.Errorf("keyword-only zero %v shows at or below a confident mismatch %v", thin.Relevance, mismatch.Relevance)
	}
}

// A known zero counts: a confident mismatch with a known title exclusion is
// not rescued by renormalizing.
func TestRelevanceKnownZeroAndGateStillApply(t *testing.T) {
	r := Score(Inputs{RoleFit: sl("mismatch_domain", 0.9), TitleSignals: true, TitleRank: -1})
	if r.Relevance > 5 {
		t.Errorf("confident mismatch, excluded title shows %v", r.Relevance)
	}
	g := Score(Inputs{RoleFit: sl("strong_fit", 0.95), Unsure: true})
	f := Score(Inputs{RoleFit: sl("strong_fit", 0.95)})
	if !(g.Relevance < f.Relevance) {
		t.Errorf("gate must still lower relevance: %v vs %v", g.Relevance, f.Relevance)
	}
}
