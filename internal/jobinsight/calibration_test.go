package jobinsight

import (
	"math"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func fitConf(t *testing.T, w Weights, s Slot) float64 {
	t.Helper()
	return w.conf(s)
}

// A backend's calibration replaces AnsweredConf (no p) and the raw p (on p);
// a backend it does not name, an invalid calibration, or a p fit read without
// p keeps the hand rule.
func TestCalibrationReplacesConfidence(t *testing.T) {
	w := DefaultWeights()
	w.Calibration = map[string]Calibration{
		"claude":   {B: math.Log(0.9 / 0.1)},          // intercept: .9
		"d1-ultra": {A: 0.5, B: 0, UsesP: true},       // halves the logit
		"runaway":  {A: 1, B: math.NaN()},             // never used
		"too big":  {A: MaxCalibrationCoef + 1, B: 0}, // never used
	}
	near := func(name string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 1e-12 {
			t.Errorf("%s: conf %.15f, want %.15f", name, got, want)
		}
	}
	near("claude, answered, no p", fitConf(t, w, Slot{Value: "strong_fit", Answered: true, Backend: "claude"}), 0.9)
	near("claude, with p: intercept still", fitConf(t, w, Slot{Value: "strong_fit", Answered: true, HasP: true, P: 0.2, Backend: "claude"}), 0.9)
	// logit(.8) = ln 4; half of it is ln 2, so sigmoid gives 2/3.
	near("d1-ultra p .8", fitConf(t, w, Slot{Value: "strong_fit", Answered: true, HasP: true, P: 0.8, Backend: "d1-ultra"}), 2.0/3)
	near("d1-ultra without p: hand rule", fitConf(t, w, Slot{Value: "strong_fit", Answered: true, Backend: "d1-ultra"}), w.AnsweredConf)
	near("uncalibrated backend: raw p", fitConf(t, w, Slot{Value: "strong_fit", Answered: true, HasP: true, P: 0.44, Backend: "lappi"}), 0.44)
	near("runaway calibration: raw p", fitConf(t, w, Slot{Value: "strong_fit", Answered: true, HasP: true, P: 0.44, Backend: "runaway"}), 0.44)
	near("oversized calibration: hand rule", fitConf(t, w, Slot{Value: "strong_fit", Answered: true, Backend: "too big"}), w.AnsweredConf)
	near("no backend recorded", fitConf(t, w, Slot{Value: "strong_fit", Answered: true}), w.AnsweredConf)
	near("not answered", fitConf(t, w, Slot{Value: "strong_fit", Backend: "claude"}), 0)
	// p of exactly 0 and 1 stay finite.
	for _, p := range []float64{0, 1} {
		if c := fitConf(t, w, Slot{Value: "strong_fit", Answered: true, HasP: true, P: p, Backend: "d1-ultra"}); !(c > 0 && c < 1) {
			t.Errorf("p %v: conf %v", p, c)
		}
	}
	// Disagreement still lowers a calibrated reading.
	near("claude disagreed", fitConf(t, w, Slot{Value: "strong_fit", Answered: true, Backend: "claude", Disagree: true, ShadowBackend: "local"}), 0.9*w.DisagreeConf)

	// Through Score: the calibrated backend's strong fit outranks the same
	// reading under the 0.70 rule.
	in := Inputs{RoleFit: Slot{Value: "strong_fit", Answered: true, Backend: "claude"}, AgeKnown: true}
	if hand, cal := Score(in), ScoreWith(in, w); !(cal.Match > hand.Match) {
		t.Errorf("calibrated match %v not above hand %v", cal.Match, hand.Match)
	}
}

func TestSlotOfReadsTheBackend(t *testing.T) {
	s := SlotOf(map[string]string{
		"triage.role_fit": "adjacent", "triage.role_fit.reading": "answered", "triage.role_fit.backend": " d1-ultra ",
	}, "role_fit")
	if s.Backend != "d1-ultra" {
		t.Errorf("backend %q", s.Backend)
	}
}

// ScoreOf ranks by the context's weights, and by the hand weights without.
func TestScoreOfUsesContextWeights(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	r := ledger.Record{Key: "gh:a:1", FirstSeen: now, Posting: posting.Posting{Key: "gh:a:1", Company: "A", PostedAt: now},
		Annotations: map[string]string{ledger.AnnCoverage: "90", "triage.role_fit": "mismatch_domain", "triage.role_fit.reading": "answered"}}
	c := NewRankContext([]ledger.Record{r}, nil, now)
	if got, want := c.ScoreOf(r), Score(InputsFor(r, c)); got.Score != want.Score {
		t.Fatalf("no weights: %v, want the hand %v", got.Score, want.Score)
	}
	w := DefaultWeights()
	w.Fit, w.Coverage = 0.01, 1
	c.Weights = &w
	if got, hand := c.ScoreOf(r), Score(InputsFor(r, c)); !(got.Score > hand.Score) {
		t.Errorf("coverage-led weights scored a high-coverage mismatch %v, hand %v", got.Score, hand.Score)
	}
}
