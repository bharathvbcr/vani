package main

import (
	"errors"

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
	w, used, warn, _ = rankWeightsChecked(cfg)
	return w, used, warn
}

// rankWeightsChecked is rankWeights that also says whether fitted shares are in
// use after being checked against the hand weights on the file's own held-out
// result (sharesChecked), as opposed to used on the file's word alone.
func rankWeightsChecked(cfg config.Config) (w *jobinsight.Weights, used, warn string, sharesChecked bool) {
	s := readRankWeights(cfg)
	return s.w, s.used, s.warn, s.sharesChecked
}

// rankPart says whether one part of the rank weights file is in use. Reason
// says why it is not, or notes what was read past while it is.
type rankPart struct {
	Part   string `json:"part"`
	InUse  bool   `json:"in_use"`
	Reason string `json:"reason,omitempty"`
}

// rankWeightsRead is what readRankWeights found: the weights (nil for the hand
// weights), what rankWeights reports of them, and which parts of the file are
// in use.
type rankWeightsRead struct {
	w             *jobinsight.Weights
	used, warn    string
	sharesChecked bool
	parts         []rankPart
}

// readRankWeights is the one reading of the config's rank_weights_file that
// rankWeights, rankWeightsChecked and status all answer from.
func readRankWeights(cfg config.Config) rankWeightsRead {
	if cfg.RankWeightsFile == "" {
		return rankWeightsRead{used: rankWeightsHand}
	}
	f, err := rankfit.Load(cfg.RankWeightsFile)
	if err != nil {
		part := "file"
		var pe *rankfit.PartError
		if errors.As(err, &pe) {
			part = pe.Part
		}
		return rankWeightsRead{used: rankWeightsHand,
			warn:  "rank_weights_file could not be used, so the hand weights rank: " + oneLine(err.Error(), 300),
			parts: []rankPart{{Part: part, Reason: oneLine(err.Error(), 300)}}}
	}
	// The date is the file's last fit of any part (a platt-only or weights-only
	// fit refreshes it; --spare-seniority does not), not the age of the shares.
	r := rankWeightsRead{used: "fitted " + f.FittedAt.UTC().Format("2006-01-02")}
	if f.Shares != nil {
		if reason := sharesGate(f); reason != "" {
			// The shares' held-out result does not pass the write gate (or there
			// is none): they do not rank. The calibration is a separate fit and
			// stays.
			r.warn = reason
			r.parts = append(r.parts, rankPart{Part: "shares", Reason: reason})
			f.Shares = nil
			switch {
			case len(f.Calibration) > 0:
				r.used += " (calibration only; shares refused)"
			case len(f.SpareSeniorityMismatch) > 0:
				r.used += " (shares refused)"
			default:
				r.used = rankWeightsHand
				return r
			}
		} else {
			r.sharesChecked = true
			r.parts = append(r.parts, rankPart{Part: "shares", InUse: true})
		}
	}
	if len(f.Calibration) > 0 {
		r.parts = append(r.parts, rankPart{Part: "calibration", InUse: true})
	}
	if len(f.SpareSeniorityMismatch) > 0 {
		r.parts = append(r.parts, rankPart{Part: "spare_seniority_mismatch", InUse: true})
	}
	if f.Report != nil {
		// The report is provenance; the gate above reads it, ranking does not.
		p := rankPart{Part: "report", InUse: true}
		if f.ReportIgnored != "" {
			p.Reason = "read past what this binary does not know of it: " + oneLine(f.ReportIgnored, 200)
		}
		r.parts = append(r.parts, p)
	}
	fitted := f.Apply(jobinsight.DefaultWeights())
	r.w = &fitted
	return r
}

// rankWeightsFacet is status's answer to "which weights rank, and can I trust
// them": Used as `vani jobs` reports it ("hand", "fitted <date>", or "fitted
// <date> (calibration only; shares refused)"), Warning the refusal or the
// failure (a file that could not be used, or shares whose held-out result does
// not pass the gate or is missing), SharesChecked whether fitted shares in use
// passed that gate, and Parts which parts of the file are in use and why not.
type rankWeightsFacet struct {
	facet
	Used          string     `json:"used"`
	Warning       string     `json:"warning,omitempty"`
	SharesChecked bool       `json:"shares_checked"`
	Parts         []rankPart `json:"parts,omitempty"`
}

// rankWeightsFacetOf reads the config's rank_weights_file as the ranking does;
// nil when none is configured. It is not ok while there is a warning.
func rankWeightsFacetOf(cfg config.Config) *rankWeightsFacet {
	if cfg.RankWeightsFile == "" {
		return nil
	}
	r := readRankWeights(cfg)
	f := &rankWeightsFacet{facet: facet{OK: r.warn == ""}, Used: r.used, Warning: r.warn, SharesChecked: r.sharesChecked, Parts: r.parts}
	if r.warn != "" {
		f.Reason = r.warn
	}
	return f
}

// rankWeightsLines are the status lines for f: the weights in use, and why not
// the fitted ones when they were not.
func rankWeightsLines(f *rankWeightsFacet) []string {
	if f == nil {
		return nil
	}
	out := []string{"rank weights: " + f.Used}
	if f.Warning != "" {
		out = append(out, "  "+f.Warning)
	}
	for _, p := range f.Parts {
		if p.Part == "report" && p.Reason != "" {
			out = append(out, "  report: "+p.Reason)
		}
	}
	return out
}

// sharesGate is the read side of the write gate: it says why a file's fitted
// shares must not rank, or "" when they may. They may only when the held-out
// result the file stores for them passes rankfit.FitResult.Beats, the rule a fit
// had to pass to be written, so a file that could not have been written by the
// gate is not trusted on read. A file that stores no result (a calibration-only
// fit drops it, since the calibration it was measured under changed) has shares
// that were never checked, and they stay unused with the same message.
func sharesGate(f rankfit.File) string {
	const lead = "rank_weights_file: shares present but their held-out result does not pass the gate, so the hand shares rank: "
	if f.Report == nil || f.Report.Weights == nil {
		return lead + "the file records no held-out result for them (a weight fit records one; a calibration-only fit drops it)"
	}
	if reason := f.Report.Weights.NoGainReason(); reason != "" {
		return lead + reason
	}
	return ""
}
