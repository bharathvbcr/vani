package jobinsight

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
)

// A language model's read of whether a posting's work is the work in the owner's
// knowledge base is a seventh additive part. Only some postings are judged, so an
// unjudged one must score exactly as it did before the part existed, and a judged
// one only orders and explains (Rule 7): the part never gates.

func kbSlot(v string) Slot { return Slot{Value: v, Answered: true} }

func judgeBase() Inputs {
	return Inputs{
		RoleFit: sl("partial_fit", 0.7), Semantic: 0.7, HasSemantic: true,
		Target: true, TitleHits: 1, BodyHits: 2, AgeKnown: true, AgeDays: 4,
	}
}

func TestKBMatchUnknownChangesNothing(t *testing.T) {
	off := DefaultWeights()
	off.KBMatch = 0
	for name, in := range map[string]Inputs{
		"zero": {}, "base": judgeBase(), "fit only": {RoleFit: sl("strong_fit", 0.9)},
		"unsure":         func() Inputs { i := judgeBase(); i.Unsure = true; return i }(),
		"unanswered":     func() Inputs { i := judgeBase(); i.KBMatch = Slot{Value: "core"}; return i }(),
		"unknown option": func() Inputs { i := judgeBase(); i.KBMatch = kbSlot("bogus"); return i }(),
		"empty option":   func() Inputs { i := judgeBase(); i.KBMatch = kbSlot(""); return i }(),
	} {
		got, want := Score(in), ScoreWith(in, off)
		if got.Score != want.Score || got.Relevance != want.Relevance || got.RelevanceKnown != want.RelevanceKnown ||
			got.Match != want.Match || got.Basis != want.Basis {
			t.Errorf("%s: an unjudged posting moved: %+v vs %+v", name, got, want)
		}
		if !reflect.DeepEqual(got.Factors, want.Factors) || len(got.Factors) != 9 {
			t.Errorf("%s: an unjudged posting's factor list changed: %+v", name, got.Factors)
		}
	}
}

func TestKBMatchOrdersByHowCloseTheWorkIs(t *testing.T) {
	prev := math.Inf(1)
	for _, v := range KBMatchOrder {
		in := judgeBase()
		in.KBMatch = kbSlot(v)
		r := Score(in)
		if !(r.Score < prev) {
			t.Errorf("%s scores %.2f, not below the closer option's %.2f", v, r.Score, prev)
		}
		prev = r.Score
		if f := factorOf(t, r, "kb_match"); !f.Known || f.Weight <= 0 || f.Value < 0 || f.Value > 1 {
			t.Errorf("%s: kb_match factor %+v", v, f)
		}
	}
	unjudged := Score(judgeBase())
	core, unrelated := judgeBase(), judgeBase()
	core.KBMatch, unrelated.KBMatch = kbSlot("core"), kbSlot("unrelated")
	if !(Score(core).Score > unjudged.Score && unjudged.Score > Score(unrelated).Score) {
		t.Errorf("core %.2f, unjudged %.2f, unrelated %.2f: want core > unjudged > unrelated",
			Score(core).Score, unjudged.Score, Score(unrelated).Score)
	}
}

func TestKBMatchOnlyOrdersNeverGates(t *testing.T) {
	in := judgeBase()
	in.KBMatch = kbSlot("unrelated")
	r := Score(in)
	if r.Gate != 1 || r.Company != 1 {
		t.Errorf("an unrelated reading moved the gate to %.2f and company to %.2f", r.Gate, r.Company)
	}
	if r.Score <= 0 {
		t.Errorf("an unrelated reading must lower the score, not erase it: %.2f", r.Score)
	}
}

func TestKBMatchConfidenceBlendsTowardThePrior(t *testing.T) {
	w := DefaultWeights()
	sure, unsure := judgeBase(), judgeBase()
	sure.KBMatch = Slot{Value: "core", Answered: true, P: 1, HasP: true}
	unsure.KBMatch = Slot{Value: "core", Answered: true, P: 0.2, HasP: true}
	a, b := ScoreWith(sure, w), ScoreWith(unsure, w)
	if !(a.Score > b.Score) {
		t.Errorf("a sure core %.2f must beat an unsure one %.2f", a.Score, b.Score)
	}
	if v := factorOf(t, a, "kb_match").Value; math.Abs(v-1) > 1e-9 {
		t.Errorf("a sure core reads %.3f, want 1", v)
	}
}

func TestInputsForCarriesKBMatchOnlyWhenAsked(t *testing.T) {
	r := rec("gh:a:1", "Acme", time.Hour, nil)
	c := NewRankContext([]ledger.Record{r}, nil, inT0)
	if got := InputsFor(r, c).KBMatch; got != (Slot{}) {
		t.Errorf("no lookup configured, yet KBMatch is %+v", got)
	}
	c.KBMatch = func(ledger.Record) (Slot, bool) { return kbSlot("core"), false }
	if got := InputsFor(r, c).KBMatch; got != (Slot{}) {
		t.Errorf("a lookup that found nothing must leave it unknown, got %+v", got)
	}
	c.KBMatch = func(x ledger.Record) (Slot, bool) {
		if x.Key != r.Key {
			t.Errorf("asked about %s", x.Key)
		}
		return kbSlot("partial"), true
	}
	if got := InputsFor(r, c).KBMatch; got.Value != "partial" || !got.Answered {
		t.Errorf("KBMatch %+v, want the stored partial", got)
	}
}

func TestKBMatchHostileWeights(t *testing.T) {
	for name, v := range map[string]float64{"nan": math.NaN(), "inf": math.Inf(1), "negative": -2, "zero": 0} {
		w := DefaultWeights()
		w.KBMatch = v
		in := judgeBase()
		in.KBMatch = kbSlot("core")
		r := ScoreWith(in, w)
		if math.IsNaN(r.Score) || r.Score < 0 || r.Score > 100 {
			t.Errorf("%s: score %v", name, r.Score)
		}
		for _, f := range r.Factors {
			if f.Name == "kb_match" && f.Weight != 0 && !(v > 0 && !math.IsInf(v, 0)) {
				t.Errorf("%s: a weight that is not positive still carries %.3f", name, f.Weight)
			}
		}
	}
	if !reflect.DeepEqual(Score(judgeBase()).Factors[0].Name, "fit") {
		t.Errorf("the factor order changed at its head")
	}
}
