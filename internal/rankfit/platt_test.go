package rankfit

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func samples(right, wrong int, p float64, hasP bool) []PlattSample {
	var out []PlattSample
	for i := range right + wrong {
		out = append(out, PlattSample{P: p, HasP: hasP, Correct: i < right})
	}
	return out
}

// Without p the fit is the intercept alone, and its optimum is closed form:
// sigmoid(B) is the mean of Platt's targets. 75 right and 25 wrong give
// targets 76/77 and 1/27, so conf = (75*76/77 + 25/27) / 100 = .749519...
func TestPlattInterceptHandComputed(t *testing.T) {
	fit, err := FitPlatt("claude", samples(75, 25, 0, false), 0)
	if err != nil {
		t.Fatal(err)
	}
	if fit.Calibration.UsesP || fit.Calibration.A != 0 {
		t.Fatalf("calibration %+v, want intercept only", fit.Calibration)
	}
	got, ok := fit.Calibration.Conf(0, false)
	if want := (75.0*76/77 + 25.0/27) / 100; !ok || math.Abs(got-want) > 1e-9 {
		t.Fatalf("conf %.12f (ok %v), want %.12f", got, ok, want)
	}
	if fit.N != 100 || fit.Correct != 75 || fit.Unused != 0 {
		t.Errorf("counts %+v", fit)
	}
}

// Readings whose p is exactly their accuracy are already calibrated: the fit
// stays at the identity. Readings that are right less often than p says are
// pulled down.
func TestPlattOnP(t *testing.T) {
	var cal []PlattSample
	for _, p := range []float64{0.1, 0.3, 0.5, 0.7, 0.9} {
		n := 1000
		right := int(math.Round(p * float64(n)))
		cal = append(cal, samples(right, n-right, p, true)...)
	}
	fit, err := FitPlatt("d1-ultra", cal, DefaultPlattRidge)
	if err != nil {
		t.Fatal(err)
	}
	if c := fit.Calibration; !c.UsesP || math.Abs(c.A-1) > 0.02 || math.Abs(c.B) > 0.02 {
		t.Fatalf("calibrated readings fitted to %+v, want about A 1, B 0", c)
	}

	// Right half as often as p claims, on the logit scale: a = .5.
	var over []PlattSample
	for _, p := range []float64{0.2, 0.4, 0.6, 0.8, 0.95} {
		q := sigmoid(0.5 * math.Log(p/(1-p)))
		right := int(math.Round(q * 2000))
		over = append(over, samples(right, 2000-right, p, true)...)
	}
	fit, err = FitPlatt("d1-ultra", over, DefaultPlattRidge)
	if err != nil {
		t.Fatal(err)
	}
	if c := fit.Calibration; math.Abs(c.A-0.5) > 0.02 || math.Abs(c.B) > 0.02 {
		t.Fatalf("overconfident readings fitted to %+v, want about A .5, B 0", c)
	}
}

func TestPlattRefusesBelowThreshold(t *testing.T) {
	for name, s := range map[string][]PlattSample{
		"none":   nil,
		"99":     samples(60, 39, 0.9, true),
		"no p 1": samples(1, 0, 0, false),
	} {
		_, err := FitPlatt("b", s, DefaultPlattRidge)
		var nr *NotRunError
		if !errors.As(err, &nr) || nr.Need != MinPlattLabels || nr.Have != len(s) {
			t.Errorf("%s: error %v, want a NotRunError for %d of %d", name, err, len(s), MinPlattLabels)
			continue
		}
		if !strings.HasPrefix(err.Error(), "could not run: ") {
			t.Errorf("%s: %q does not read as could not run", name, err)
		}
	}
	if _, err := FitPlatt("b", samples(70, 30, 0.8, true), DefaultPlattRidge); err != nil {
		t.Errorf("exactly %d: %v", MinPlattLabels, err)
	}
}

// One class only, or every p the same: Platt's targets keep the fit finite
// and the confidence below 1, and a constant p is fitted as the intercept.
func TestPlattOneClassAndConstantP(t *testing.T) {
	for name, s := range map[string][]PlattSample{
		"all right, no p":    samples(120, 0, 0, false),
		"all wrong, no p":    samples(0, 120, 0, false),
		"all right, p .99":   samples(120, 0, 0.99, true),
		"all wrong, p 1":     samples(0, 120, 1, true),
		"all right, p 0":     samples(120, 0, 0, true),
		"mixed, constant p ": samples(90, 30, 0.6, true),
	} {
		fit, err := FitPlatt("b", s, DefaultPlattRidge)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		c, ok := fit.Calibration.Conf(s[0].P, s[0].HasP)
		if !ok || !(c > 0 && c < 1) || !fit.Calibration.Valid() {
			t.Fatalf("%s: conf %v ok %v from %+v", name, c, ok, fit.Calibration)
		}
		// A constant p cannot tell a slope from the intercept: the fit must not
		// present a ridge-chosen slope as measured.
		if fit.Calibration.UsesP || fit.Calibration.A != 0 {
			t.Errorf("%s: %+v, want the intercept alone", name, fit.Calibration)
		}
		// The smoothed precision bounds the answer away from 0 and 1.
		pos := 0
		for _, x := range s {
			if x.Correct {
				pos++
			}
		}
		if want := (float64(pos)*float64(pos+1)/float64(pos+2) + float64(len(s)-pos)/float64(len(s)-pos+2)) / float64(len(s)); math.Abs(c-want) > 0.01 {
			t.Errorf("%s: conf %.4f, smoothed precision %.4f", name, c, want)
		}
	}
}

// A NaN or out-of-range p is never fitted, on p or as an intercept sample: a
// reading that claims a p it does not have is not evidence of either kind.
// When that leaves too few, the fit refuses and counts only the usable ones.
func TestPlattInvalidP(t *testing.T) {
	var nr *NotRunError
	if _, err := FitPlatt("b", samples(80, 40, math.NaN(), true), DefaultPlattRidge); !errors.As(err, &nr) || nr.Have != 0 {
		t.Fatalf("120 NaN p: %v, want a refusal counting 0", err)
	}

	mixed := append(append(samples(100, 50, 0.7, true), samples(50, 50, 0.4, true)...), samples(10, 10, 1.5, true)...)
	fit, err := FitPlatt("b", mixed, DefaultPlattRidge)
	if err != nil {
		t.Fatal(err)
	}
	if !fit.Calibration.UsesP || fit.N != 250 || fit.Unused != 20 {
		t.Fatalf("mixed: %+v, want 250 used on p and 20 left out", fit)
	}
	if got := PlattUsable(mixed); got != 250 {
		t.Errorf("PlattUsable %d, want 250", got)
	}

	short := append(samples(60, 30, 0.7, true), samples(10, 10, -0.2, true)...)
	if _, err := FitPlatt("b", short, DefaultPlattRidge); !errors.As(err, &nr) || nr.Have != 90 {
		t.Errorf("90 valid p of 110: %v, want a refusal counting 90", err)
	}
}

func TestPlattDeterministic(t *testing.T) {
	var s []PlattSample
	for i := range 400 {
		s = append(s, PlattSample{P: float64(i%97) / 97, HasP: true, Correct: i%7 < 4})
	}
	a, err := FitPlatt("b", s, DefaultPlattRidge)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := FitPlatt("b", s, DefaultPlattRidge)
	if a != b {
		t.Fatalf("two fits differ: %+v %+v", a, b)
	}
}
