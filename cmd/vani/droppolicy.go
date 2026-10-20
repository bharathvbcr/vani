package main

import (
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/pipeline"
)

// dropPolicy is the configured drop rules, the exclusions, and the title
// reader that applies the rules before any backend is asked. The run and `vani prefilter` share it.
func dropPolicy(cfg config.Config, openRouter string) pipeline.DropPolicy {
	rules := make([]pipeline.DropRule, 0, len(cfg.DropRules))
	for _, r := range cfg.DropRules {
		rules = append(rules, pipeline.DropRule{Slot: r.Slot, Values: r.Values, MinOpenRouterProb: r.MinOpenRouterProb, MinLappiScore: r.MinLappiScore})
	}
	p := pipeline.DropPolicy{Rules: rules, OpenRouter: openRouter}
	if x := cfg.Exclude.Clone(); !x.Empty() {
		p.Exclude = x.Match
	}
	if s := cfg.TitleSignals; len(s.Read) > 0 {
		p.ReadTitle = func(title string) []pipeline.TitleReading {
			reads := s.Reads(title)
			out := make([]pipeline.TitleReading, len(reads))
			for i, r := range reads {
				out[i] = pipeline.TitleReading{Slot: r.Slot, Value: r.Value, Matched: r.Matched}
			}
			return out
		}
	}
	return p
}
