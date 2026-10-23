package main

import (
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/rankfit"
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

// rankWeightsHand is what rankWeights reports when Score uses the hand weights.
const rankWeightsHand = "hand"

// rankWeights reads the config's rank_weights_file for every ranking caller,
// so `vani jobs`, the digest and kbmatch's candidate order rank by the same
// weights. w is nil for the hand weights. used says which weights are in use
// ("hand", or "fitted <date>"), so it is never silent; warn is non-empty when
// a file was configured but could not be used, and the hand weights stand in.
func rankWeights(cfg config.Config) (w *jobinsight.Weights, used, warn string) {
	if cfg.RankWeightsFile == "" {
		return nil, rankWeightsHand, ""
	}
	f, err := rankfit.Load(cfg.RankWeightsFile)
	if err != nil {
		return nil, rankWeightsHand, "rank_weights_file could not be used, so the hand weights rank: " + oneLine(err.Error(), 300)
	}
	fitted := f.Apply(jobinsight.DefaultWeights())
	return &fitted, "fitted " + f.FittedAt.UTC().Format("2006-01-02"), ""
}
