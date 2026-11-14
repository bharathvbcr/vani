package main

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/rankfit"
)

// The seniority-gate shadow: what Weights.SpareSeniorityMismatch would do to
// the owner's judged postings, without changing the ranking. Each variant
// spares a list of seniority readings; the report gives, per label mode, the
// AUC of the age-free Relevance under the live weights and under the variant,
// and every judged posting the variant moves with its percentile in the open
// pool before and after. Promotion is the owner's (`vani rankfit
// --spare-seniority`), or the weight fit's gate; this only measures.

// spareVariants are the levels each shadow variant spares.
var spareVariants = []struct {
	name   string
	levels []string
}{
	{"spare mid and senior", []string{"mid", "senior"}},
	{"spare mid, senior and staff_plus", []string{"mid", "senior", "staff_plus"}},
}

// shadowModes are the label modes the shadow reports AUCs under.
var shadowModes = []eval.LabelMode{eval.ModeYesOrMaybe, eval.ModeMaybesExcluded, eval.ModeEngineering}

// maxShadowMoves bounds the moved postings listed per variant.
const maxShadowMoves = 40

type seniorityShadow struct {
	// Weights names the weights the variants start from ("hand" or "fitted <date>").
	Weights  string          `json:"weights"`
	Pool     int             `json:"pool"` // open postings the percentiles are over
	Variants []shadowVariant `json:"variants"`
	Error    string          `json:"error,omitempty"`
}

type shadowVariant struct {
	Name   string   `json:"name"`
	Spared []string `json:"spared"`
	// Changed counts judged postings whose Relevance the variant changes;
	// PoolChanged the open postings.
	Changed     int           `json:"changed"`
	PoolChanged int           `json:"pool_changed"`
	AUC         []shadowAUC   `json:"auc"`
	Moved       []shadowMoved `json:"moved"`
	MovedTotal  int           `json:"moved_total"`
	// Wanted is every judged posting answered yes, moved or not.
	Wanted []shadowMoved `json:"wanted"`
}

type shadowAUC struct {
	Mode    eval.LabelMode `json:"mode"`
	Pos     int            `json:"pos"`
	Neg     int            `json:"neg"`
	OK      bool           `json:"ok"`
	Base    float64        `json:"base"`
	BaseLo  float64        `json:"base_lo"`
	BaseHi  float64        `json:"base_hi"`
	Spare   float64        `json:"spare"`
	SpareLo float64        `json:"spare_lo"`
	SpareHi float64        `json:"spare_hi"`
	// BaseP and SpareP are each AUC's one-sided chance p (rankfit.AUCPValue),
	// set with OK.
	BaseP  float64 `json:"base_p"`
	SpareP float64 `json:"spare_p"`
}

type shadowMoved struct {
	Key     posting.Key `json:"key"`
	Title   string      `json:"title"`
	Verdict string      `json:"verdict"`
	// Before and After are the posting's Relevance percentile (0-100, higher is
	// better) among the open postings.
	Before float64 `json:"before"`
	After  float64 `json:"after"`
}

// open reports whether a posting is in the pool the ranking orders.
func open(st posting.Status) bool {
	return st != posting.Dropped && st != posting.Dead && st != posting.Duplicate
}

// buildSeniorityShadow scores the open pool and the judged postings under the
// live weights and each variant. A source that cannot be read is an error.
func (e *env) buildSeniorityShadow(done map[posting.Key]eval.Entry, snap ledger.Snapshot) seniorityShadow {
	var out seniorityShadow
	cfg, err := e.loadConfig()
	if err != nil {
		out.Error = "config: " + err.Error()
		return out
	}
	live, used, warn := rankWeights(cfg)
	out.Weights = used
	if warn != "" {
		out.Weights += " (" + warn + ")"
	}
	base := jobinsight.DefaultWeights()
	if live != nil {
		base = *live
	}
	me, err := marks.Read(marks.Dir(e.stateDir))
	if err != nil {
		out.Error = "read marks: " + err.Error()
		return out
	}
	in, err := e.newInsight(jobinsight.DefaultScoreHalfLifeDays, snap, me)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	recs := uniqueRecords(snap)
	inputs := make([]jobinsight.Inputs, len(recs))
	for i, r := range recs {
		inputs[i] = jobinsight.InputsFor(r, in.rc)
	}
	relevance := func(w jobinsight.Weights) []float64 {
		v := make([]float64, len(recs))
		for i := range recs {
			v[i] = jobinsight.ScoreWith(inputs[i], w).Relevance
		}
		return v
	}
	baseRel := relevance(base)
	out.Pool = openCount(recs)
	eng := engineeringOf(snap)
	for _, v := range spareVariants {
		w := base
		w.SpareSeniorityMismatch = slices.Clone(v.levels)
		sv := compareArms(v.name, recs, done, eng, baseRel, relevance(w))
		sv.Spared = v.levels
		out.Variants = append(out.Variants, sv)
	}
	return out
}

// uniqueRecords is one record per key, the first, as the other reports read
// the ledger.
func uniqueRecords(snap ledger.Snapshot) []ledger.Record {
	seen := make(map[posting.Key]bool, len(snap.Records))
	recs := make([]ledger.Record, 0, len(snap.Records))
	for _, r := range snap.Records {
		if !seen[r.Key] {
			seen[r.Key] = true
			recs = append(recs, r)
		}
	}
	return recs
}

// openCount counts the open postings of recs (the percentiles' pool).
func openCount(recs []ledger.Record) int {
	n := 0
	for _, r := range recs {
		if open(r.Status) {
			n++
		}
	}
	return n
}

// compareArms sets the variant's relevance (rel) against the base's (baseRel),
// both indexed like recs: the open postings it moves, every judged posting it
// moves and every wanted (yes) one moved or not, with their percentile among
// the open postings, and the AUC of each arm per label mode.
func compareArms(name string, recs []ledger.Record, done map[posting.Key]eval.Entry, eng func(posting.Key) bool, baseRel, rel []float64) shadowVariant {
	sv := shadowVariant{Name: name, AUC: []shadowAUC{}, Moved: []shadowMoved{}, Wanted: []shadowMoved{}}
	var basePool, varPool []float64
	for i, r := range recs {
		if open(r.Status) {
			basePool = append(basePool, baseRel[i])
			varPool = append(varPool, rel[i])
			if rel[i] != baseRel[i] {
				sv.PoolChanged++
			}
		}
	}
	sort.Float64s(basePool)
	sort.Float64s(varPool)
	for i, r := range recs {
		e, ok := done[r.Key]
		if !ok {
			continue
		}
		verdict := e.Labels[eval.WouldApply]
		if _, judged := eval.ModeYesOrMaybe.Remap(verdict, false); !judged {
			continue
		}
		m := shadowMoved{Key: r.Key, Title: oneLine(r.Posting.Title, 120), Verdict: verdict,
			Before: percentile(basePool, baseRel[i]), After: percentile(varPool, rel[i])}
		if verdict == "yes" {
			sv.Wanted = append(sv.Wanted, m)
		}
		if rel[i] == baseRel[i] {
			continue
		}
		sv.Changed++
		sv.MovedTotal++
		if len(sv.Moved) < maxShadowMoves {
			sv.Moved = append(sv.Moved, m)
		}
	}
	for _, md := range shadowModes {
		var bp, bn, sp, sn []float64
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
				bp, sp = append(bp, baseRel[i]), append(sp, rel[i])
			} else {
				bn, sn = append(bn, baseRel[i]), append(sn, rel[i])
			}
		}
		a := shadowAUC{Mode: md, Pos: len(bp), Neg: len(bn)}
		var okB, okS bool
		a.Base, a.BaseLo, a.BaseHi, okB = rankfit.AUCWithInterval(bp, bn, rankfit.DefaultPartAUCResamples, 1)
		a.Spare, a.SpareLo, a.SpareHi, okS = rankfit.AUCWithInterval(sp, sn, rankfit.DefaultPartAUCResamples, 1)
		a.OK = okB && okS
		if a.OK {
			a.BaseP, _ = rankfit.AUCPValue(bp, bn)
			a.SpareP, _ = rankfit.AUCPValue(sp, sn)
		}
		sv.AUC = append(sv.AUC, a)
	}
	return sv
}

// percentile is the share (0-100) of sorted below v, ties counted half.
func percentile(sorted []float64, v float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	lo := sort.SearchFloat64s(sorted, v)
	hi := sort.Search(len(sorted), func(i int) bool { return sorted[i] > v })
	return 100 * (float64(lo) + float64(hi-lo)/2) / float64(len(sorted))
}

// Markdown renders the shadow.
func (s seniorityShadow) Markdown() string {
	var b strings.Builder
	b.WriteString("\n## Seniority-gate shadow\n\nWhat sparing a mismatch_seniority reading at some seniority levels would do (Weights.SpareSeniorityMismatch): the gate is dropped and the fit part is unknown for those postings. It only loosens. Nothing here changes the ranking; `vani rankfit --spare-seniority mid,senior` writes it to the rank weights file.\n\n")
	if s.Error != "" {
		fmt.Fprintf(&b, "not computed: %s\n", s.Error)
		return b.String()
	}
	fmt.Fprintf(&b, "- weights the variants start from: %s\n- open postings (percentiles are over these): %d\n", s.Weights, s.Pool)
	for _, v := range s.Variants {
		b.WriteString(v.Markdown("live"))
	}
	return b.String()
}

// Markdown renders one variant against its base arm, named base.
func (v shadowVariant) Markdown(base string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n### %s\n\n- judged postings it moves: %d; open postings it moves: %d\n\n", v.Name, v.Changed, v.PoolChanged)
	fmt.Fprintf(&b, "| mode | wanted / unwanted | AUC of Relevance, %s (95%%; exact p) | with the variant (95%%; exact p) |\n|---|---|---|---|\n", base)
	for _, a := range v.AUC {
		if !a.OK {
			fmt.Fprintf(&b, "| %s | %d / %d | insufficient | insufficient |\n", a.Mode, a.Pos, a.Neg)
			continue
		}
		fmt.Fprintf(&b, "| %s | %d / %d | %.2f (%.2f-%.2f; p %.3f) | %.2f (%.2f-%.2f; p %.3f) |\n", a.Mode, a.Pos, a.Neg, a.Base, a.BaseLo, a.BaseHi, a.BaseP, a.Spare, a.SpareLo, a.SpareHi, a.SpareP)
	}
	rows := func(head string, ms []shadowMoved) {
		if len(ms) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n| %s | answer | percentile, %s | with the variant |\n|---|---|---|---|\n", head, base)
		for _, m := range ms {
			fmt.Fprintf(&b, "| %s | %s | %.0f | %.0f |\n", strings.ReplaceAll(m.Title, "|", "/"), m.Verdict, m.Before, m.After)
		}
	}
	rows("judged posting it moves", v.Moved)
	if v.MovedTotal > len(v.Moved) {
		fmt.Fprintf(&b, "\n(%d more not listed)\n", v.MovedTotal-len(v.Moved))
	}
	rows("every posting answered yes", v.Wanted)
	return b.String()
}
