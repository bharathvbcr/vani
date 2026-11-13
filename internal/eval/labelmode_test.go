package eval

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestTitleIsEngineeringOnTheOwnersJudgedTitles(t *testing.T) {
	// The owner's judged titles (2026-11-12), and what the report should call them.
	for title, want := range map[string]bool{
		"Member of Technical Staff (Software Engineer, Backend Platform)": true,
		"Software Engineer - AIM Virtualization":                          true,
		"Systems Engineer":                                                true,
		"Staff + Senior Software Engineer, Inference Infrastructure":      true,
		"Staff Software Engineer (Core Experiences)":                      true,
		"Senior Software Engineer, Site Reliability Engineer":             true,
		"Research Engineer, Cybersecurity RL (Reinforcement Learning)":    true,
		"Solutions Architect - APAC":                                      false,
		"GTM Engineer":                                                    false,
		"People Research Scientist":                                       false,
		"Enterprise Account Executive - Industries Generalist":            false,
		"Lead Salesforce Developer":                                       false,
		"Head of Finance":                                                 false,
		"Engineering Manager, Scheduler and Fleet Efficiency":             false,
		"Manager, Software Engineering (R5966)":                           false,
		"Sr. Mechanical Engineer, Satellites (Starlink)":                  false,
		"Sr. Staff Electrical Engineer - BlueSky (R5838)":                 false,
		"Intern - ASIC Validation":                                        false,
		"Sr. Customer Support - Account Support Spec.":                    false,
		"":                               false,
		strings.Repeat("engineer ", 200): true, // bounded, still read
		"Engineer\x00Sales":              false,
	} {
		if got := TitleIsEngineering(title); got != want {
			t.Errorf("%q: %v, want %v", title, got, want)
		}
	}
}

func TestLabelModesRemapAndFilter(t *testing.T) {
	labels := map[posting.Key]Entry{
		"y":    judged("yes", "decile:9"),
		"m":    judged("maybe", "decile:5"),
		"n":    judged("no", "decile:2"),
		"s":    judged("skip", "decile:1"),
		"slot": {Labels: map[string]string{"role_fit": "strong_fit"}},
	}
	eng := func(k posting.Key) bool { return k == "y" || k == "n" }
	got := func(m LabelMode) map[string]string {
		out := map[string]string{}
		for k, e := range ViewLabels(labels, m, eng) {
			out[string(k)] = e.Labels[WouldApply]
		}
		return out
	}
	cases := map[LabelMode]map[string]string{
		ModeYesOrMaybe:     {"y": "yes", "m": "maybe", "n": "no", "slot": ""},
		ModeYesOnly:        {"y": "yes", "m": "no", "n": "no", "slot": ""},
		ModeMaybesExcluded: {"y": "yes", "n": "no", "slot": ""},
		ModeEngineering:    {"y": "yes", "n": "no", "slot": ""},
	}
	for m, want := range cases {
		g := got(m)
		if len(g) != len(want) {
			t.Errorf("%s: %v, want %v", m, g, want)
			continue
		}
		for k, v := range want {
			if g[k] != v {
				t.Errorf("%s %s: %q, want %q", m, k, g[k], v)
			}
		}
	}
	// The input is never modified.
	if labels["m"].Labels[WouldApply] != "maybe" {
		t.Error("ViewLabels modified its input")
	}
	// An unknown mode keeps no would_apply label.
	if g := ViewLabels(labels, LabelMode("bogus"), eng); len(g) != 1 {
		t.Errorf("unknown mode: %v", g)
	}
}
