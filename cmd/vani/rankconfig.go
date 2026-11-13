package main

import (
	"fmt"

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

// rankEscalateBelowP is the threshold under which the ranking reads the escalation
// tier's reading of a slot in place of an unsure primary's
// (jobinsight.RankContext.EscalateBelowP): the config's decide.escalate.below_p
// while decide.escalate is on, 0 (never) otherwise. Every ranking caller reads it
// here, so turning escalation off in the config returns the order to the primary's.
func rankEscalateBelowP(cfg config.Config) float64 {
	if !cfg.Decide.EscalateEnabled() {
		return 0
	}
	return cfg.Decide.EscalateBelowP()
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
	used = "fitted " + f.FittedAt.UTC().Format("2006-01-02")
	if f.Shares != nil {
		switch refusal, unchecked := sharesVerdict(f); {
		case refusal != "":
			// The file's own held-out result puts the shares below the hand
			// weights (or is not one): they do not rank. Its calibration is a
			// separate fit and stays.
			warn = "rank_weights_file's shares are refused, so the hand shares rank: " + refusal
			f.Shares = nil
			if len(f.Calibration) == 0 {
				return nil, rankWeightsHand, warn
			}
			used += " (calibration only; shares refused)"
		case unchecked != "":
			warn = unchecked // used as the file says, but never silently unchecked
		}
	}
	fitted := f.Apply(jobinsight.DefaultWeights())
	return &fitted, used, warn
}

// sharesVerdict judges a file's fitted shares by the held-out result the file
// records (rankfit.FitResult: the accuracy of the fitted shares and of the hand
// weights on the same held-out judged pairs). refusal is non-empty when the
// shares must not rank: that result puts them below the hand weights, or is not
// an accuracy over any pair. unchecked is non-empty when the file records no
// result, so the shares are used on its word alone, and says so. Both are empty
// for shares at least as good as the hand weights on their own held-out pairs.
func sharesVerdict(f rankfit.File) (refusal, unchecked string) {
	if f.Report == nil || f.Report.Weights == nil {
		return "", "rank_weights_file's shares record no held-out result (a file `vani rankfit` writes does), so they are used unchecked against the hand weights"
	}
	r := f.Report.Weights
	unit := func(v float64) bool { return v >= 0 && v <= 1 } // false for NaN
	switch {
	case r.HeldOutPairs <= 0:
		return "its recorded held-out result has no held-out pairs", ""
	case !unit(r.FitAccuracy) || !unit(r.HandAccuracy):
		return fmt.Sprintf("its recorded held-out accuracies (%v fitted, %v hand) are not an accuracy", r.FitAccuracy, r.HandAccuracy), ""
	case r.FitAccuracy < r.HandAccuracy:
		return fmt.Sprintf("on its own %d held-out pairs the fitted shares rank %.1f%% right, worse than the hand weights' %.1f%%",
			r.HeldOutPairs, 100*r.FitAccuracy, 100*r.HandAccuracy), ""
	}
	return "", ""
}
