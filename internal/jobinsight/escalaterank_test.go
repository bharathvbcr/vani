package jobinsight

import (
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
)

// escAnn is a record's annotations for one slot the primary (Liquid D) read at p
// and a shadow reading was recorded beside: shadow value sv, its reading and the
// backend that gave it.
func escAnn(slot, primary string, p float64, sv, sreading, sbackend string) map[string]string {
	pre := ledger.AnnTriagePrefix + slot
	a := map[string]string{
		pre:                                       primary,
		pre + ledger.AnnTriageSuffixReading:       "answered",
		pre + ledger.AnnTriageSuffixBackend:       "d1-ultra",
		pre + ledger.AnnTriageSuffixP:             strconv.FormatFloat(p, 'f', 4, 64),
		pre + ledger.AnnTriageSuffixShadow:        sv,
		pre + ledger.AnnTriageSuffixShadowReading: sreading,
		pre + ledger.AnnTriageSuffixShadowBackend: sbackend,
	}
	if sv != "" && sv != primary {
		a[pre+ledger.AnnTriageSuffixDisagree] = "true"
	}
	return a
}

func escCtx(belowP float64) RankContext {
	return RankContext{Now: inT0, EscalateBelowP: belowP}
}

func escRec(ann map[string]string) ledger.Record { return rec("gh:a:1", "Acme", time.Hour, ann) }

// Where the primary was under the threshold and the escalation answered with a
// listed option, the fit the ranking reads is the escalation's: its value, backend
// "escalate", and the confidence of an answered reading with no p (AnsweredConf).
func TestRankingTakesTheEscalationsFitUnderTheThreshold(t *testing.T) {
	r := escRec(escAnn("role_fit", "mismatch_domain", 0.30, "strong_fit", "answered", ShadowEscalate))
	got := InputsFor(r, escCtx(0.5)).RoleFit
	want := Slot{Value: "strong_fit", Answered: true, Backend: ShadowEscalate, ShadowBackend: ShadowEscalate}
	if got != want {
		t.Fatalf("ranking slot %+v, want %+v", got, want)
	}
	res := escCtx(0.5).ScoreOf(r)
	f := factorOf(t, res, "fit")
	if f.Backend != ShadowEscalate || f.Reading != "strong_fit" || !f.Known || f.Conf != DefaultWeights().AnsweredConf || f.Value != fitBase["strong_fit"] {
		t.Fatalf("fit factor %+v does not say it rests on the escalation's strong_fit", f)
	}
	if g := factorOf(t, res, "gate"); g.Value != 1 {
		t.Fatalf("the primary's doubtful mismatch still gates: %+v", g)
	}
	before := escCtx(0).ScoreOf(r)
	if res.Score <= before.Score {
		t.Fatalf("the escalated fit did not lift the order: %.2f against the primary's %.2f", res.Score, before.Score)
	}
	if f := factorOf(t, before, "fit"); f.Backend != "" || f.Reading != "" {
		t.Fatalf("a primary fit names a backend: %+v", f)
	}
}

// A primary at or above the threshold is not touched, whatever a shadow says.
func TestRankingKeepsAConfidentPrimary(t *testing.T) {
	for _, p := range []float64{0.5, 0.51, 0.95, 1} {
		r := escRec(escAnn("role_fit", "mismatch_domain", p, "strong_fit", "answered", ShadowEscalate))
		got := InputsFor(r, escCtx(0.5)).RoleFit
		if got.Value != "mismatch_domain" || got.Backend != "d1-ultra" || !got.HasP || got.P != p {
			t.Errorf("p=%v: ranking slot %+v, want the primary's reading", p, got)
		}
	}
}

// Anything short of an answered, listed escalation reading leaves the primary's.
func TestRankingIgnoresAnUnusableEscalation(t *testing.T) {
	good := func() map[string]string {
		return escAnn("role_fit", "mismatch_domain", 0.3, "strong_fit", "answered", ShadowEscalate)
	}
	pre := ledger.AnnTriagePrefix + "role_fit"
	for name, mutate := range map[string]func(map[string]string){
		"unavailable":         func(a map[string]string) { a[pre+ledger.AnnTriageSuffixShadowReading] = "unavailable" },
		"failed":              func(a map[string]string) { a[pre+ledger.AnnTriageSuffixShadowReading] = "failed" },
		"reading missing":     func(a map[string]string) { delete(a, pre+ledger.AnnTriageSuffixShadowReading) },
		"unlisted value":      func(a map[string]string) { a[pre+ledger.AnnTriageSuffixShadow] = "excellent_fit" },
		"another slots value": func(a map[string]string) { a[pre+ledger.AnnTriageSuffixShadow] = "remote" },
		"empty value":         func(a map[string]string) { a[pre+ledger.AnnTriageSuffixShadow] = "" },
		"padded garbage":      func(a map[string]string) { a[pre+ledger.AnnTriageSuffixShadow] = " strong_fit; drop" },
		"value missing":       func(a map[string]string) { delete(a, pre+ledger.AnnTriageSuffixShadow) },
		"another shadow":      func(a map[string]string) { a[pre+ledger.AnnTriageSuffixShadowBackend] = "lappi" },
		"no shadow backend":   func(a map[string]string) { delete(a, pre+ledger.AnnTriageSuffixShadowBackend) },
		"primary has no p":    func(a map[string]string) { delete(a, pre+ledger.AnnTriageSuffixP) },
		"primary p is NaN":    func(a map[string]string) { a[pre+ledger.AnnTriageSuffixP] = "NaN" },
		"primary p garbage":   func(a map[string]string) { a[pre+ledger.AnnTriageSuffixP] = "low" },
		"primary unanswered":  func(a map[string]string) { a[pre+ledger.AnnTriageSuffixReading] = "unavailable" },
	} {
		t.Run(name, func(t *testing.T) {
			a := good()
			mutate(a)
			got, want := InputsFor(escRec(a), escCtx(0.5)).RoleFit, InputsFor(escRec(a), escCtx(0)).RoleFit
			if got != want || got.Backend == ShadowEscalate {
				t.Fatalf("ranking slot %+v, want the primary's %+v", got, want)
			}
		})
	}
	// A threshold that is not a probability switches the promotion off.
	for _, b := range []float64{0, -0.5, math.NaN(), math.Inf(-1)} {
		if got := InputsFor(escRec(good()), escCtx(b)).RoleFit; got.Value != "mismatch_domain" {
			t.Errorf("below_p %v: ranking slot %+v, want the primary's", b, got)
		}
	}
}

// A confident mismatch from the escalation tier still gates (Mismatch x its
// confidence); a mismatch from an unsure primary that the tier overturns no
// longer does.
func TestEscalatedMismatchGatesAndOverturnedOneDoesNot(t *testing.T) {
	w := DefaultWeights()
	confirmed := escRec(escAnn("role_fit", "strong_fit", 0.3, "mismatch_domain", "answered", ShadowEscalate))
	res := escCtx(0.5).ScoreWith(confirmed, w)
	wantGate := 1 - w.Mismatch*w.AnsweredConf
	if g := factorOf(t, res, "gate"); math.Abs(g.Value-wantGate) > 1e-9 {
		t.Fatalf("gate %v, want %v for a mismatch the escalation read", g.Value, wantGate)
	}
	if g := factorOf(t, escCtx(0).ScoreWith(confirmed, w), "gate"); g.Value != 1 {
		t.Fatalf("control: the primary's strong_fit gates %v", g.Value)
	}

	overturned := escRec(escAnn("role_fit", "mismatch_domain", 0.3, "strong_fit", "answered", ShadowEscalate))
	if g := factorOf(t, escCtx(0.5).ScoreWith(overturned, w), "gate"); g.Value != 1 {
		t.Fatalf("an overturned doubtful mismatch still gates: %v", g.Value)
	}
	if g := factorOf(t, escCtx(0).ScoreWith(overturned, w), "gate"); g.Value >= 1 {
		t.Fatalf("control: the primary's mismatch should gate today, got %v", g.Value)
	}
}

// A kept-by penalty keys on the reading the ranking uses: the escalation's value
// when it took the slot, so a reading the better model overturned is not
// penalised and one it found is.
func TestPenaltyKeysOnTheRankedReading(t *testing.T) {
	rules := []DropRule{{Slot: "work_mode", Values: []string{"onsite"}}}
	w := DefaultWeights()
	c := escCtx(0.5)
	c.DropRules = rules

	overturned := escRec(escAnn("work_mode", "onsite", 0.3, "remote", "answered", ShadowEscalate))
	if in := InputsFor(overturned, c); len(in.Penalties) != 0 {
		t.Fatalf("penalised a reading the escalation overturned: %+v", in.Penalties)
	}
	if in := InputsFor(overturned, RankContext{Now: inT0, DropRules: rules}); len(in.Penalties) != 1 {
		t.Fatalf("control: the primary's onsite should be penalised today: %+v", in.Penalties)
	}

	found := escRec(escAnn("work_mode", "remote", 0.3, "onsite", "answered", ShadowEscalate))
	in := InputsFor(found, c)
	if len(in.Penalties) != 1 || in.Penalties[0].Slot.Value != "onsite" || in.Penalties[0].Slot.Backend != ShadowEscalate {
		t.Fatalf("penalties %+v, want one on the escalation's onsite", in.Penalties)
	}
	if g := factorOf(t, ScoreWith(in, w), "gate").Value; math.Abs(g-(1-w.PenaltyStrength*w.AnsweredConf)) > 1e-9 {
		t.Fatalf("gate %v, want the penalty at the escalation's confidence", g)
	}

	// Above the threshold the primary's onsite stands.
	sure := escRec(escAnn("work_mode", "onsite", 0.8, "remote", "answered", ShadowEscalate))
	if in := InputsFor(sure, c); len(in.Penalties) != 1 || in.Penalties[0].Slot.Value != "onsite" {
		t.Fatalf("a confident primary was overridden: %+v", in.Penalties)
	}
	// A mismatch penalty and the mismatch fit are one reading, penalised once.
	mm := escRec(escAnn("role_fit", "partial_fit", 0.3, "mismatch_domain", "answered", ShadowEscalate))
	c.DropRules = []DropRule{{Slot: "role_fit", Values: []string{"mismatch_domain"}}}
	res := c.ScoreWith(mm, w)
	if g := factorOf(t, res, "gate").Value; math.Abs(g-(1-w.Mismatch*w.AnsweredConf)) > 1e-9 {
		t.Fatalf("gate %v: the escalated mismatch was penalised twice", g)
	}
}

// ScoreWith is RankContext.ScoreOf over explicit weights, for the tests above.
func (c RankContext) ScoreWith(r ledger.Record, w Weights) Result {
	return ScoreWith(InputsFor(r, c), w)
}

// A future rank weights file can calibrate the escalation tier by its name.
func TestEscalationReadingIsCalibratableByName(t *testing.T) {
	r := escRec(escAnn("role_fit", "mismatch_domain", 0.3, "strong_fit", "answered", ShadowEscalate))
	w := DefaultWeights()
	w.Calibration = map[string]Calibration{ShadowEscalate: {A: 0, B: 2, UsesP: false}}
	f := factorOf(t, escCtx(0.5).ScoreWith(r, w), "fit")
	if want := 1 / (1 + math.Exp(-2)); math.Abs(f.Conf-want) > 1e-9 {
		t.Fatalf("conf %v, want the calibration's %v", f.Conf, want)
	}
}
