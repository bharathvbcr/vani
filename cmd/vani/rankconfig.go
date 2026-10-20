package main

import (
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// rankRules is what the ranking reads from the config, for `vani jobs` and
// the digest alike, so the two cannot disagree: drop rules (a posting they
// kept matched one under its threshold) and rank penalties both demote in
// proportion to the reading's confidence, and the title signals' Rank when
// include or exclude signals are set. Rule values can reveal the owner's work
// authorization: they only ever reach jobinsight, never a log or output.
func rankRules(cfg config.Config) (rules []jobinsight.DropRule, titleRank func(string) int) {
	for _, r := range cfg.DropRules {
		rules = append(rules, jobinsight.DropRule{Slot: r.Slot, Values: r.Values})
	}
	for _, r := range cfg.RankPenalties {
		rules = append(rules, jobinsight.DropRule{Slot: r.Slot, Values: r.Values})
	}
	if s := cfg.TitleSignals; len(s.Include) > 0 || len(s.Exclude) > 0 {
		titleRank = s.Rank
	}
	return rules, titleRank
}
