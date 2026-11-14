package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/rankfit"
)

// The weight shadow: what other additive weights or match shrink would do to
// the order of the owner's judged open postings, without changing the ranking.
// It measures the age-free ranked score (match x gate x company, Score less
// freshness), which is what orders the list apart from age; the displayed
// Relevance uses a fixed shrink (jobinsight's relevanceShrink), so a
// MatchShrink variant cannot move it. Each variant is compared with the live
// weights on the same postings by a paired bootstrap of the AUC difference, at
// a family-wise level over every variant and label mode.
//
// The refuting result is stated before any data: a variant counts as better
// only if its paired interval is above 0 in every label mode that can be
// computed, and nothing is promoted on fewer than minWeightShadowLabels judged
// open postings, whatever the interval says. A variant whose interval contains
// 0 is no evidence for a change, however its point estimate looks.

// minWeightShadowLabels is the fewest judged open postings a weight change may
// be argued from (the weight fit's own floor is far below what an AUC
// difference of a few points needs).
const minWeightShadowLabels = 150

// weightShadowResamples is the paired bootstrap's size.
const weightShadowResamples = 4000

// weightVariants are the alternatives the shadow measures. Each starts from the
// live weights.
var weightVariants = []struct {
	name string
	set  func(*jobinsight.Weights)
}{
	{"V1: fit weight 0", func(w *jobinsight.Weights) { w.Fit = 0 }},
	{"V2: coverage weight 0", func(w *jobinsight.Weights) { w.Coverage = 0 }},
	{"V3: fit 0.20, coverage 0.15, semantic 0.30, keyword 0.10, title 0.20, employer 0.05", func(w *jobinsight.Weights) {
		w.Fit, w.Coverage, w.Semantic, w.Keyword, w.Title, w.Employer = 0.20, 0.15, 0.30, 0.10, 0.20, 0.05
	}},
	{"V4: match shrink 0.10", func(w *jobinsight.Weights) { w.KnownMatch, w.MatchShrink = true, 0.10 }},
}

// Verdicts of a weight variant.
const (
	verdictBetter = "better"
	// verdictBetterBelowFloor is better on fewer judged open postings than
	// minWeightShadowLabels: not evidence for a change.
	verdictBetterBelowFloor = "better_below_floor"
	verdictWorse            = "worse"
	verdictWithinNoise      = "within_noise"
	verdictInsufficient     = "insufficient"
)

type weightShadow struct {
	Weights string `json:"weights"`
	Pool    int    `json:"pool"`
	// Judged counts the judged postings still open, the ones compared;
	// MinLabels is the floor below which no verdict may change a weight.
	Judged    int             `json:"judged"`
	MinLabels int             `json:"min_labels"`
	Alpha     float64         `json:"alpha"` // two-sided, family-wise over Variants x modes
	Resamples int             `json:"resamples"`
	Variants  []weightVariant `json:"variants"`
	// Coverage is how much of the match each part carries over the open pool.
	Coverage *partCoverage `json:"coverage,omitempty"`
	Error    string        `json:"error,omitempty"`
}

type weightVariant struct {
	shadowVariant
	Paired  []pairedAUC `json:"paired"`
	Verdict string      `json:"verdict"`
}

type pairedAUC struct {
	Mode eval.LabelMode `json:"mode"`
	OK   bool           `json:"ok"`
	Diff float64        `json:"diff"` // variant AUC less the live one
	Lo   float64        `json:"lo"`
	Hi   float64        `json:"hi"`
}

// ageFreeScore is the ranked score without freshness, 0-100.
func ageFreeScore(r jobinsight.Result) float64 { return 100 * r.Match * r.Gate * r.Company }

// buildWeightShadow scores the open pool under the live weights and each
// variant. A source that cannot be read is an error, never an empty table. s
// and err are newShadowSetup's, shared with the seniority shadow.
func buildWeightShadow(done map[posting.Key]eval.Entry, s shadowSetup, err error) weightShadow {
	out := weightShadow{MinLabels: minWeightShadowLabels, Resamples: weightShadowResamples, Variants: []weightVariant{}}
	out.Weights = s.used
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Pool = openCount(s.recs)
	cov := partCoverageOf(s.inputs, s.recs, s.base)
	out.Coverage = &cov
	judged := openJudged(done, s.recs)
	out.Judged = len(judged)
	out.Alpha = 0.05 / float64(len(weightVariants)*len(shadowModes))
	base := s.scores(s.base, ageFreeScore)
	for vi, v := range weightVariants {
		w := s.base
		v.set(&w)
		rel := s.scores(w, ageFreeScore)
		wv := weightVariant{shadowVariant: compareArms(v.name, s.recs, judged, s.eng, base, rel)}
		for mi, md := range shadowModes {
			wv.Paired = append(wv.Paired, pairedFor(md, s.recs, judged, s.eng, base, rel, out.Alpha, uint64(1+vi*len(shadowModes)+mi)))
		}
		wv.Verdict = withFloor(verdictOf(wv.Paired), out.Judged)
		out.Variants = append(out.Variants, wv)
	}
	return out
}

// openJudged is done restricted to postings still open (a dropped posting is
// not in the order a weight change would move) and drawn at random within
// their stratum: a pick selected on a reading (the disagreement and
// yes-neighbour cells, which done already lacks) or on its title (the
// engineering group) was chosen on what some variants reweight.
func openJudged(done map[posting.Key]eval.Entry, recs []ledger.Record) map[posting.Key]eval.Entry {
	out := make(map[posting.Key]eval.Entry)
	for _, r := range recs {
		if e, ok := done[r.Key]; ok && open(r.Status) && !eval.SelectedOnReading(e.Source) && !eval.EngineeringStratum(e.Source) {
			if _, judged := eval.ModeYesOrMaybe.Remap(e.Labels[eval.WouldApply], false); judged {
				out[r.Key] = e
			}
		}
	}
	return out
}

// pairedFor is the paired AUC difference of rel over base under mode md.
func pairedFor(md eval.LabelMode, recs []ledger.Record, done map[posting.Key]eval.Entry, eng func(posting.Key) bool, base, rel []float64, alpha float64, seed uint64) pairedAUC {
	var pa, pb, na, nb []float64
	for i, r := range recs {
		e, ok := done[r.Key]
		if !ok {
			continue
		}
		nv, keep := md.Remap(e.Labels[eval.WouldApply], eng(r.Key))
		if !keep {
			continue
		}
		if nv == "yes" || nv == "maybe" {
			pa, pb = append(pa, base[i]), append(pb, rel[i])
		} else {
			na, nb = append(na, base[i]), append(nb, rel[i])
		}
	}
	p := pairedAUC{Mode: md}
	p.Diff, p.Lo, p.Hi, p.OK = rankfit.PairedAUCDiff(pa, pb, na, nb, alpha, weightShadowResamples, seed)
	return p
}

// verdictOf is better when every computable mode's interval is above 0 (and
// the first mode, yes-or-maybe, is computable), worse when any is below 0,
// otherwise within noise.
func verdictOf(ps []pairedAUC) string {
	if len(ps) == 0 || !ps[0].OK {
		return verdictInsufficient
	}
	better := true
	for _, p := range ps {
		if !p.OK {
			continue
		}
		if p.Hi < 0 {
			return verdictWorse
		}
		if !(p.Lo > 0) {
			better = false
		}
	}
	if better {
		return verdictBetter
	}
	return verdictWithinNoise
}

// withFloor is v, except that better on fewer than minWeightShadowLabels
// judged postings says so in the verdict itself.
func withFloor(v string, judged int) string {
	if v == verdictBetter && judged < minWeightShadowLabels {
		return verdictBetterBelowFloor
	}
	return v
}

// additiveParts are the Score parts that share the match, in Factor order.
var additiveParts = []string{"fit", "coverage", "semantic", "keyword", "title", "employer", "kb_match"}

// maxDistinctPartValues bounds the distinct values counted per part.
const maxDistinctPartValues = 50

// partCoverage is, over the open pool under the live weights, how often each
// additive part is evidence and how much of the match it carries.
type partCoverage struct {
	Pool  int           `json:"pool"`
	Parts []partCovered `json:"parts"`
	// RelevanceKnown is the share of the additive weight the displayed
	// relevance rests on: its median and 10th percentile over the pool.
	RelevanceKnownMedian float64 `json:"relevance_known_median"`
	RelevanceKnownP10    float64 `json:"relevance_known_p10"`
}

type partCovered struct {
	Name string `json:"name"`
	// Weight is the part's nominal share (mean over the pool).
	Weight float64 `json:"weight"`
	// Known counts postings where the part is evidence: an unpreferred
	// employer, which says nothing, is not.
	Known int `json:"known"`
	// Carried is the mean of weight x confidence over the pool, zero where
	// unknown: the share of the match the part actually decides.
	Carried float64 `json:"carried"`
	// Gated counts known readings with no confidence in the match: a confident
	// mismatch fit, which the gate penalises instead. MeanConf is the mean
	// confidence of the other known readings, so a low Carried reads as one or
	// the other.
	Gated    int     `json:"gated,omitempty"`
	MeanConf float64 `json:"mean_conf"`
	// Distinct counts the part's distinct values where known, up to
	// maxDistinctPartValues (DistinctCapped then).
	Distinct       int  `json:"distinct"`
	DistinctCapped bool `json:"distinct_capped,omitempty"`
	// Constant is true when the part is known somewhere and takes one value
	// everywhere it is: it cannot order anything.
	Constant bool `json:"constant,omitempty"`
}

// partCoverageOf reads ScoreWith's factors for every open posting.
func partCoverageOf(inputs []jobinsight.Inputs, recs []ledger.Record, w jobinsight.Weights) partCoverage {
	type agg struct {
		known, gated        int
		weight, carry, conf float64
		vals                map[float64]bool
		capped              bool
	}
	parts := make(map[string]*agg, len(additiveParts))
	for _, n := range additiveParts {
		parts[n] = &agg{vals: map[float64]bool{}}
	}
	var relKnown []float64
	for i, r := range recs {
		if !open(r.Status) {
			continue
		}
		res := jobinsight.ScoreWith(inputs[i], w)
		relKnown = append(relKnown, res.RelevanceKnown)
		for _, f := range res.Factors {
			a, ok := parts[f.Name]
			if !ok {
				continue
			}
			a.weight += f.Weight
			known := f.Known && (f.Name != "employer" || f.Value > 0)
			if !known {
				continue
			}
			a.known++
			a.carry += f.Weight * f.Conf
			if f.Conf == 0 {
				a.gated++
			} else {
				a.conf += f.Conf
			}
			if len(a.vals) < maxDistinctPartValues {
				a.vals[f.Value] = true
			} else if !a.vals[f.Value] {
				a.capped = true
			}
		}
	}
	out := partCoverage{Pool: len(relKnown), Parts: make([]partCovered, 0, len(additiveParts))}
	for _, n := range additiveParts {
		a := parts[n]
		p := partCovered{Name: n, Known: a.known, Gated: a.gated, Distinct: len(a.vals), DistinctCapped: a.capped}
		if confident := a.known - a.gated; confident > 0 {
			p.MeanConf = a.conf / float64(confident)
		}
		if out.Pool > 0 {
			p.Weight, p.Carried = a.weight/float64(out.Pool), a.carry/float64(out.Pool)
		}
		p.Constant = a.known > 0 && len(a.vals) == 1
		out.Parts = append(out.Parts, p)
	}
	if len(relKnown) > 0 {
		slices.Sort(relKnown)
		out.RelevanceKnownMedian = relKnown[len(relKnown)/2]
		out.RelevanceKnownP10 = relKnown[len(relKnown)/10]
	}
	return out
}

// Markdown renders the part coverage.
func (c partCoverage) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n## Score part coverage (open pool)\n\nOver the %d open postings, under the live weights: how often each part is evidence rather than the prior, and how much of the match it decides (mean of weight x confidence; a confident mismatch fit carries none, the gate takes it). A part that is known nowhere, or constant wherever known, cannot order anything.\n\n", c.Pool)
	if c.Pool == 0 {
		b.WriteString("no open postings\n")
		return b.String()
	}
	b.WriteString("| part | weight | known | gated (confident mismatch) | mean confidence, the rest | carried | distinct values | note |\n|---|---|---|---|---|---|---|---|\n")
	var carried float64
	for _, p := range c.Parts {
		carried += p.Carried
	}
	for _, p := range c.Parts {
		d := fmt.Sprint(p.Distinct)
		if p.DistinctCapped {
			d = fmt.Sprintf("%d+", p.Distinct)
		}
		note := ""
		switch {
		case p.Known == 0:
			note = "never known"
		case p.Constant:
			note = "constant: orders nothing"
		}
		share := ""
		if carried > 0 {
			share = fmt.Sprintf(" (%.0f%% of carried)", 100*p.Carried/carried)
		}
		fmt.Fprintf(&b, "| %s | %.3f | %d (%.1f%%) | %d | %.2f | %.3f%s | %s | %s |\n", p.Name, p.Weight, p.Known, 100*float64(p.Known)/float64(c.Pool), p.Gated, p.MeanConf, p.Carried, share, d, note)
	}
	fmt.Fprintf(&b, "\nShare of the additive weight the displayed relevance rests on: median %.2f, 10th percentile %.2f.\n", c.RelevanceKnownMedian, c.RelevanceKnownP10)
	return b.String()
}

// Markdown renders the weight shadow and the part coverage.
func (s weightShadow) Markdown() string {
	var b strings.Builder
	if s.Coverage != nil {
		b.WriteString(s.Coverage.Markdown())
	}
	b.WriteString("\n## Weight shadow\n\nWhat other weights would do to the order of the judged postings still open, measured on the age-free ranked score (match x gate x company: the list's order apart from freshness). Nothing here changes the ranking.\n\n")
	if s.Error != "" {
		fmt.Fprintf(&b, "not computed: %s\n", s.Error)
		return b.String()
	}
	fmt.Fprintf(&b, "Stated before the data: a variant is better only if its paired AUC difference is above 0 in every label mode that can be computed, at a two-sided level of %.4f per comparison (0.05 over %d variants x %d modes), %d paired resamples. With fewer than %d judged open postings no verdict changes a weight. An interval containing 0 is no evidence for a change. Picks drawn on a reading or on their title (the disagreement, yes-neighbour and engineering cells) are left out: they were chosen on what the variants reweight.\n\n", s.Alpha, len(s.Variants), len(shadowModes), s.Resamples, s.MinLabels)
	fmt.Fprintf(&b, "- weights the variants start from: %s\n- open postings (percentiles are over these): %d\n- judged open postings compared: %d", s.Weights, s.Pool, s.Judged)
	if s.Judged < s.MinLabels {
		fmt.Fprintf(&b, " (below %d: report only)", s.MinLabels)
	}
	b.WriteString("\n")
	for _, v := range s.Variants {
		b.WriteString(v.shadowVariant.Markdown("live", "the age-free score"))
		b.WriteString("\n| mode | AUC difference, variant less live (family-wise interval) |\n|---|---|\n")
		for _, p := range v.Paired {
			if !p.OK {
				fmt.Fprintf(&b, "| %s | insufficient |\n", p.Mode)
				continue
			}
			fmt.Fprintf(&b, "| %s | %+.3f (%+.3f to %+.3f) |\n", p.Mode, p.Diff, p.Lo, p.Hi)
		}
		switch v.Verdict {
		case verdictBetterBelowFloor:
			fmt.Fprintf(&b, "\nverdict: **better on %d judged open postings, under the floor of %d: not evidence for a change**\n", s.Judged, s.MinLabels)
		default:
			fmt.Fprintf(&b, "\nverdict: **%s**\n", strings.ReplaceAll(v.Verdict, "_", " "))
		}
	}
	return b.String()
}
