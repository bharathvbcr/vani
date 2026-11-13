package config

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestLivenessDefaultsAndBounds(t *testing.T) {
	var l Liveness
	if l.Share() != DefaultMassShare || l.MinOpen() != DefaultMassMinOpen || l.Hold() != 14*24*time.Hour {
		t.Fatalf("defaults wrong: %v %d %s", l.Share(), l.MinOpen(), l.Hold())
	}
	zero, neg, nan, big, ok := 0.0, -0.1, math.NaN(), 1.01, 1.0
	for _, tc := range []struct {
		name string
		l    Liveness
		want string
	}{
		{"zero share", Liveness{MassShare: &zero}, "mass_share"},
		{"negative share", Liveness{MassShare: &neg}, "mass_share"},
		{"NaN share", Liveness{MassShare: &nan}, "mass_share"},
		{"share over one", Liveness{MassShare: &big}, "mass_share"},
		{"negative floor", Liveness{MassMinOpen: -1}, "mass_min_open"},
		{"huge floor", Liveness{MassMinOpen: MaxMassMinOpen + 1}, "mass_min_open"},
		{"hold under the ordinary hold", Liveness{MassHoldHours: MinMassHoldHours - 1}, "mass_hold_hours"},
		{"negative hold", Liveness{MassHoldHours: -5}, "mass_hold_hours"},
		{"hold over a year", Liveness{MassHoldHours: MaxMassHoldHours + 1}, "mass_hold_hours"},
	} {
		var got []string
		tc.l.validate(func(format string, a ...any) { got = append(got, format) })
		if len(got) != 1 || !strings.Contains(got[0], tc.want) {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
	var got []string
	Liveness{MassShare: &ok, MassMinOpen: 1, MassHoldHours: MinMassHoldHours}.validate(func(format string, a ...any) { got = append(got, format) })
	if len(got) != 0 {
		t.Fatalf("the edges of the ranges are valid: %v", got)
	}
}

func TestDefaultConfigValidatesWithLiveness(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Liveness.MassHoldHours = 3
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "liveness.mass_hold_hours") {
		t.Fatalf("Validate did not reach liveness: %v", err)
	}
}
