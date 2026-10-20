package pipeline

import (
	"math"
	"reflect"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

func TestDropCandidatesMatchEvaluate(t *testing.T) {
	pol := DropPolicy{
		OpenRouter: "d1-ultra",
		Rules: []DropRule{
			{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinOpenRouterProb: 0.9},
			{Slot: "seniority", Values: []string{"too_senior"}, MinOpenRouterProb: 0.8},
		},
	}
	jev := func(v string, p float64, backend string) decide.SlotReading {
		return decide.SlotReading{Reading: decide.Answered, Value: v, Jev: &decide.JevCalib{Probability: p}, Detail: "backend=" + backend}
	}
	for name, tc := range map[string]struct {
		slots map[string]decide.SlotReading
		want  []string
	}{
		"both clear":         {map[string]decide.SlotReading{"role_fit": jev("mismatch_domain", 0.95, "d1-ultra"), "seniority": jev("too_senior", 0.85, "d1-ultra")}, []string{"role_fit", "seniority"}},
		"exactly at p":       {map[string]decide.SlotReading{"role_fit": jev("mismatch_domain", 0.9, "d1-ultra")}, []string{"role_fit"}},
		"below p":            {map[string]decide.SlotReading{"role_fit": jev("mismatch_domain", 0.89, "d1-ultra")}, nil},
		"NaN p":              {map[string]decide.SlotReading{"role_fit": jev("mismatch_domain", math.NaN(), "d1-ultra")}, nil},
		"other value":        {map[string]decide.SlotReading{"role_fit": jev("strong_fit", 0.99, "d1-ultra")}, nil},
		"other backend":      {map[string]decide.SlotReading{"role_fit": jev("mismatch_domain", 0.99, "llm")}, nil},
		"unavailable":        {map[string]decide.SlotReading{"role_fit": {Reading: decide.Unavailable, Value: "mismatch_domain"}}, nil},
		"missing":            {map[string]decide.SlotReading{}, nil},
		"one of two clears":  {map[string]decide.SlotReading{"role_fit": jev("mismatch_domain", 0.5, "d1-ultra"), "seniority": jev("too_senior", 0.9, "d1-ultra")}, []string{"seniority"}},
		"no jev calibration": {map[string]decide.SlotReading{"role_fit": {Reading: decide.Answered, Value: "mismatch_domain", Detail: "backend=d1-ultra"}}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			a := decide.Answer{Backend: "d1-ultra", Slots: tc.slots}
			got := pol.DropCandidates(a)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DropCandidates = %v, want %v", got, tc.want)
			}
			// A candidate exists exactly when Evaluate drops.
			status, _ := pol.Evaluate(a)
			if (len(got) > 0) != (status == posting.Dropped) {
				t.Fatalf("candidates %v but Evaluate said %s", got, status)
			}
		})
	}
}
