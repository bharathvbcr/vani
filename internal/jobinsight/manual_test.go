package jobinsight

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
)

// The ranking's one slot owner (SlotFor) takes the owner's current manual
// reading before the escalation's and the primary's.

func manualOf(slots map[string]string) func(ledger.Record, string) (string, bool) {
	return func(_ ledger.Record, slot string) (string, bool) {
		v, ok := slots[slot]
		return v, ok
	}
}

func TestManualReadingOutranksEscalationAndPrimary(t *testing.T) {
	// The primary read mismatch_domain at p 0.3, and the escalation tier (under the
	// threshold) read adjacent: the manual strong_fit still wins.
	r := escRec(escAnn("role_fit", "mismatch_domain", 0.30, "adjacent", "answered", ShadowEscalate))
	c := escCtx(0.5)
	if got := c.SlotFor(r, "role_fit"); got.Backend != ShadowEscalate || got.Value != "adjacent" {
		t.Fatalf("without a manual reading: %+v", got)
	}
	c.Manual = manualOf(map[string]string{"role_fit": "strong_fit"})
	got := c.SlotFor(r, "role_fit")
	want := Slot{Value: "strong_fit", Answered: true, Backend: BackendManual}
	if got != want {
		t.Fatalf("slot %+v, want %+v (no p, no shadow, not disagreeing)", got, want)
	}
	// The manual reading wins over a confident primary too, with escalation off.
	r = escRec(escAnn("role_fit", "mismatch_domain", 0.99, "", "", ""))
	c = RankContext{Now: inT0, Manual: manualOf(map[string]string{"role_fit": "strong_fit"})}
	if got := c.SlotFor(r, "role_fit"); got != want {
		t.Fatalf("over a confident primary: %+v", got)
	}
	// And a posting with no triage reading at all.
	if got := c.SlotFor(escRec(nil), "role_fit"); got != want {
		t.Fatalf("over no reading: %+v", got)
	}
}

// Only the slots the manual reading names are taken; the rest fall through.
func TestManualReadingLeavesOtherSlotsToTheirOwners(t *testing.T) {
	r := escRec(escAnn("sponsorship", "no_sponsorship", 0.9, "", "", ""))
	c := RankContext{Now: inT0, Manual: manualOf(map[string]string{"role_fit": "strong_fit"})}
	if got := c.SlotFor(r, "sponsorship"); got.Value != "no_sponsorship" || got.Backend != "d1-ultra" || !got.HasP {
		t.Fatalf("a slot the manual reading does not name: %+v", got)
	}
}

// An unusable manual reading is no reading: not found, or not a listed option
// for the slot.
func TestUnusableManualReadingFallsBackToThePrimary(t *testing.T) {
	r := escRec(escAnn("role_fit", "partial_fit", 0.8, "", "", ""))
	for name, m := range map[string]func(ledger.Record, string) (string, bool){
		"not found":      func(ledger.Record, string) (string, bool) { return "", false },
		"unlisted":       manualOf(map[string]string{"role_fit": "excellent_fit"}),
		"another slot's": manualOf(map[string]string{"role_fit": "remote"}),
		"empty":          manualOf(map[string]string{"role_fit": ""}),
		"padded":         manualOf(map[string]string{"role_fit": " strong_fit"}),
		"none":           manualOf(map[string]string{"role_fit": "none"}),
	} {
		c := RankContext{Now: inT0, Manual: m}
		if got := c.SlotFor(r, "role_fit"); got.Value != "partial_fit" || got.Backend != "d1-ultra" {
			t.Errorf("%s: %+v, want the primary's reading", name, got)
		}
	}
}

// The reading has the confidence of an answered reading with no p
// (AnsweredConf), the fit factor says it rests on the manual reading, and it
// orders. A Calibration named for the backend replaces AnsweredConf.
func TestManualReadingScoresAndSaysSo(t *testing.T) {
	r := escRec(escAnn("role_fit", "adjacent", 0.9, "", "", ""))
	base := RankContext{Now: inT0}
	c := base
	c.Manual = manualOf(map[string]string{"role_fit": "strong_fit"})
	res, was := c.ScoreOf(r), base.ScoreOf(r)
	f := factorOf(t, res, "fit")
	if f.Backend != BackendManual || f.Reading != "strong_fit" || !f.Known || f.Conf != DefaultWeights().AnsweredConf || f.Value != fitBase["strong_fit"] {
		t.Fatalf("fit factor %+v", f)
	}
	if res.Score <= was.Score || res.Relevance <= was.Relevance {
		t.Fatalf("the manual strong_fit did not lift the order: %.2f/%.2f against %.2f/%.2f", res.Score, res.Relevance, was.Score, was.Relevance)
	}
	if f := factorOf(t, was, "fit"); f.Backend != "" || f.Reading != "" {
		t.Fatalf("a primary fit names a backend: %+v", f)
	}

	w := DefaultWeights()
	w.Calibration = map[string]Calibration{BackendManual: {A: 0, B: 2, UsesP: false}}
	c.Weights = &w
	if f := factorOf(t, c.ScoreOf(r), "fit"); f.Conf <= DefaultWeights().AnsweredConf || f.Backend != BackendManual {
		t.Fatalf("a calibration for the manual backend was not used: %+v", f)
	}
}

// A manual mismatch gates like any confident mismatch, and the rank penalties read
// the manual reading of their slot: the reading orders everything it names.
func TestManualReadingFeedsGateAndPenalties(t *testing.T) {
	r := escRec(escAnn("role_fit", "strong_fit", 0.9, "", "", ""))
	base := RankContext{Now: inT0}
	c := base
	c.Manual = manualOf(map[string]string{"role_fit": "mismatch_domain"})
	if g := factorOf(t, c.ScoreOf(r), "gate"); g.Value >= factorOf(t, base.ScoreOf(r), "gate").Value {
		t.Fatalf("a manual mismatch did not gate: %+v", g)
	}

	r = escRec(escAnn("sponsorship", "sponsors_visa", 0.9, "", "", ""))
	base = RankContext{Now: inT0, DropRules: []DropRule{{Slot: "sponsorship", Values: []string{"no_sponsorship"}}}}
	c = base
	c.Manual = manualOf(map[string]string{"sponsorship": "no_sponsorship"})
	if got := InputsFor(r, base).Penalties; len(got) != 0 {
		t.Fatalf("penalty without the manual reading: %+v", got)
	}
	got := InputsFor(r, c).Penalties
	if len(got) != 1 || got[0].Slot.Backend != BackendManual || got[0].Slot.Value != "no_sponsorship" {
		t.Fatalf("penalties %+v do not read the manual reading", got)
	}
	if c.ScoreOf(r).Score >= base.ScoreOf(r).Score {
		t.Fatal("the manual no_sponsorship did not lower the order")
	}
}

// A nil lookup is the ranking as it was: the scores are identical.
func TestNilManualIsTheRankingAsItWas(t *testing.T) {
	r := escRec(escAnn("role_fit", "partial_fit", 0.3, "strong_fit", "answered", ShadowEscalate))
	a, b := escCtx(0.5), escCtx(0.5)
	b.Manual = func(ledger.Record, string) (string, bool) { return "", false }
	ra, rb := a.ScoreOf(r), b.ScoreOf(r)
	if ra.Score != rb.Score || ra.Relevance != rb.Relevance || len(ra.Factors) != len(rb.Factors) {
		t.Fatalf("%+v vs %+v", ra, rb)
	}
}
