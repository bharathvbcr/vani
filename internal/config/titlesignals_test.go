package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestTitleSignalsRank(t *testing.T) {
	s := TitleSignals{
		Include: []string{"Inference", "GPU", "Performance", "Agent", "Machine Learning Engineer", "LLM", "llm"},
		Exclude: []string{"Sales", "Recruit"},
	}
	for title, want := range map[string]int{
		"Senior Software Engineer, LLM Inference":  2,
		"llm   INFERENCE engineer":                 2, // case and whitespace runs fold; "LLM" and "llm" count once
		"Machine  Learning\tEngineer, GPU":         2,
		"Machine Learning Engineering Manager":     0, // "Engineer" is not a whole word in "Engineering"
		"Agents Platform Engineer":                 0, // "Agent" does not match "Agents"
		"GPU-Kernel Engineer":                      1, // punctuation is a boundary
		"GPUs and TPUs":                            0,
		"Inference Sales Engineer":                 -1, // an exclusion outranks every include
		"Recruiter, AI Inference":                  1,  // "Recruit" does not match "Recruiter"
		"Technical Recruit Coordinator, Inference": -1,
		"":             0,
		"Café LLM":     1,
		"ÉLLM":         0, // a letter, even a non-ASCII one, is no boundary
		"Data Analyst": 0,
		"People Specialist, Performance Management":  1, // known false positive: ordering only, never a drop
		"Security Risk & Compliance, Agent Security": 1, // known false positive: ordering only, never a drop
	} {
		if got := s.Rank(title); got != want {
			t.Errorf("Rank(%q) = %d, want %d", title, got, want)
		}
	}
	if got := (TitleSignals{}).Rank("LLM Inference"); got != 0 {
		t.Errorf("no signals: %d", got)
	}
	// A repeated occurrence after a non-match still matches.
	if got := (TitleSignals{Include: []string{"ml"}}).Rank("html ml"); got != 1 {
		t.Errorf("second occurrence: %d", got)
	}
}

func TestTitleSignalsValidate(t *testing.T) {
	long := strings.Repeat("x", MaxTitleSignalLen+1)
	many := make([]string, MaxTitleSignals+1)
	for i := range many {
		many[i] = "x"
	}
	for name, tc := range map[string]struct {
		s    TitleSignals
		want string
	}{
		"empty entry":   {TitleSignals{Include: []string{" "}}, "title_signals.include[0] is empty"},
		"long entry":    {TitleSignals{Exclude: []string{long}}, "title_signals.exclude[0] is over"},
		"too many":      {TitleSignals{Include: many}, "at most 64"},
		"invalid utf-8": {TitleSignals{Include: []string{"\xff"}}, "not valid UTF-8"},
	} {
		var got []string
		tc.s.validate(func(f string, a ...any) { got = append(got, fmt.Sprintf(f, a...)) })
		if len(got) != 1 || !strings.Contains(got[0], tc.want) {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	var none []string
	TitleSignals{Include: []string{"LLM"}, Exclude: []string{"Sales"}}.validate(func(f string, a ...any) { none = append(none, f) })
	if len(none) != 0 {
		t.Errorf("valid signals refused: %q", none)
	}
}
