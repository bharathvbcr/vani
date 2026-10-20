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
	if math.Abs(only.RelevanceKnown-0.30) > 1e-9 {
		t.Errorf("fit-only RelevanceKnown %v, want 0.30 (the fit weight; an unpreferred employer is not evidence)", only.RelevanceKnown)
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

// The prior still decides the order: Score and Match are what they were, and a
// fit-only posting still scores below the same posting with everything known.
func TestRelevanceLeavesScoreAndMatchOnThePrior(t *testing.T) {
	in := Inputs{RoleFit: sl("strong_fit", 0.95), AgeKnown: true, AgeDays: 2}
	r := Score(in)
	// match = 0.30*fit + prior(0.40) x (0.20+0.25+0.10+0.10) + 0.05*0
	fit := 0.95*0.85 + 0.05*0.40
	wantMatch := (0.30*fit + 0.40*0.65) / 1.0
	if math.Abs(r.Match-wantMatch) > 1e-9 {
		t.Errorf("Match %v, want %v (the prior must still fill unknown parts)", r.Match, wantMatch)
	}
	if r.Relevance <= round1(100*r.Match*r.Gate*r.Company) {
		t.Errorf("Relevance %v should lift above the prior-filled %v", r.Relevance, round1(100*r.Match*r.Gate*r.Company))
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
