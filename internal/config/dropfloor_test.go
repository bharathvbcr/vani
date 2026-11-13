package config

import (
	"math"
	"strings"
	"testing"
)

// The owner's policy: a model reading drops a posting only at p >= 0.9; zero
// means the rule never drops on that backend. Lappi's score is the top-two
// probability margin, so a margin of m puts the top label at p >= (1+m)/2:
// the same 0.9 floor is a margin of 0.8.
func TestDropRuleThresholdFloor(t *testing.T) {
	rule := func(p, l float64) DropRule {
		return DropRule{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinOpenRouterProb: p, MinLappiScore: l}
	}
	for name, tc := range map[string]struct {
		r    DropRule
		want string // "" valid
	}{
		"owner live values":       {rule(0.9, 0), ""},
		"openrouter at the floor": {rule(0.9, 0), ""},
		"openrouter certain":      {rule(1, 0), ""},
		"openrouter never":        {rule(0, 0.8), ""},
		"openrouter below floor":  {rule(0.5, 0), "drop_rules[0]: min_openrouter_prob 0.5 must be 0 (never) or within [0.9, 1]"},
		"openrouter just below":   {rule(0.899, 0), "min_openrouter_prob 0.899 must be 0 (never) or within [0.9, 1]"},
		"openrouter tiny":         {rule(0.0001, 0), "min_openrouter_prob"},
		"openrouter over one":     {rule(1.1, 0), "min_openrouter_prob"},
		"openrouter nan":          {rule(math.NaN(), 0), "min_openrouter_prob"},
		"lappi at the floor":      {rule(0.9, 0.8), ""},
		"lappi below floor":       {rule(0.9, 0.5), "drop_rules[0]: min_lappi_score 0.5 must be 0 (never) or within [0.8, 1]"},
		"lappi over one":          {rule(0.9, 1.5), "min_lappi_score"},
		"lappi negative":          {rule(0.9, -0.1), "min_lappi_score"},
		"lappi nan":               {rule(0.9, math.NaN()), "min_lappi_score"},
	} {
		t.Run(name, func(t *testing.T) {
			c := valid()
			c.DropRules = []DropRule{tc.r}
			err := c.Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("want valid, got %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}
