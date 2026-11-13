package eval

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// MinYieldJudged is the fewest judged labels a stratum needs before the yes
// yield report will name it as yes-rich.
const MinYieldJudged = 5

// YieldRow is one stratum's raw yes yield: of N judged labels drawn from it
// (yes, maybe or no), Yes said yes, with the 95% Wilson interval on Yes/N.
type YieldRow struct {
	Source string  `json:"source"`
	N      int     `json:"judged"`
	Yes    int     `json:"yes"`
	Maybe  int     `json:"maybe"`
	Lo     float64 `json:"lo"`
	Hi     float64 `json:"hi"`
}

// YesYield is where the owner's yeses come from, per audit stratum and
// disagreement cell. It is raw counts, never weighted by p_select and never a
// population estimate: it says which strata are worth more of the owner's
// labelling time, which is what the draw's quotas spend (stage 5 of
// build/fable-plan-2026-11-12-ranking.md: a yes-rich quota).
//
// Random is every random stratum pooled (deciles, drop classes, closed). Richest
// is the stratum with the highest Wilson lower bound among those with at least
// MinYieldJudged labels, named only when that bound is above Random's yes
// rate; Advice says so in words, or why none is named.
type YesYield struct {
	Rows    []YieldRow `json:"rows"`
	Random  YieldRow   `json:"random"`
	Richest string     `json:"richest,omitempty"`
	Advice  string     `json:"advice"`
}

// BuildYesYield counts the judged would_apply labels by their Source. A manual
// label, one from Explore or another surface, and a skip are not counted: none
// was drawn from a stratum.
func BuildYesYield(labels map[posting.Key]Entry) YesYield {
	rows := map[string]*YieldRow{}
	out := YesYield{Rows: []YieldRow{}, Random: YieldRow{Source: "random strata"}}
	for _, e := range labels {
		v, ok := e.Labels[WouldApply]
		if !ok {
			continue
		}
		if _, judged := wanted(v); !judged {
			continue
		}
		random := auditStratum(e.Source)
		if !random && !SelectedOnReading(e.Source) {
			continue
		}
		r := rows[e.Source]
		if r == nil {
			r = &YieldRow{Source: e.Source}
			rows[e.Source] = r
		}
		r.add(v)
		if random {
			out.Random.add(v)
		}
	}
	for _, src := range slices.Sorted(maps.Keys(rows)) {
		r := *rows[src]
		r.Lo, r.Hi, _ = Wilson(r.Yes, r.N)
		out.Rows = append(out.Rows, r)
	}
	out.Random.Lo, out.Random.Hi, _ = Wilson(out.Random.Yes, out.Random.N)

	best, bestLo := "", -1.0
	for _, r := range out.Rows {
		if r.N >= MinYieldJudged && r.Lo > bestLo {
			best, bestLo = r.Source, r.Lo
		}
	}
	switch {
	case out.Random.N == 0:
		out.Advice = "no data: no judged label drawn from a random stratum yet, so nothing to compare a stratum with"
	case best == "":
		out.Advice = fmt.Sprintf("none named: no stratum has %d judged labels yet", MinYieldJudged)
	case bestLo <= float64(out.Random.Yes)/float64(out.Random.N):
		out.Advice = fmt.Sprintf("none named: no stratum's yes rate is clearly above the random strata's (%d of %d)", out.Random.Yes, out.Random.N)
	default:
		out.Richest = best
		how := "give its group more of the draw's places"
		if SelectedOnReading(best) {
			how = "raise --disagree (at most twice the random quotas)"
		}
		out.Advice = fmt.Sprintf("%s is yes-rich: its 95%% lower bound %.2f is above the random strata's %d of %d; %s. The quotas are the owner's call.", best, bestLo, out.Random.Yes, out.Random.N, how)
	}
	return out
}

// Markdown renders the yes yield table.
func (y YesYield) Markdown() string {
	var b strings.Builder
	b.WriteString("\n## Yes yield per stratum\n\nWhere the owner's yeses come from, to spend labelling time where they are likeliest. Raw counts with 95% Wilson intervals on yes; nothing is weighted, and the disagreement cells were chosen on a model's reading, so this is no estimate of the pool.\n\n")
	if len(y.Rows) == 0 {
		b.WriteString("no data: no judged label drawn from a stratum yet\n")
		return b.String()
	}
	b.WriteString("| stratum | judged | yes | maybe | yes rate (95%) |\n|---|---|---|---|---|\n")
	for _, r := range append(slices.Clone(y.Rows), y.Random) {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %s |\n", r.Source, r.N, r.Yes, r.Maybe, yieldRate(r))
	}
	fmt.Fprintf(&b, "\n%s\n", y.Advice)
	return b.String()
}

// add counts one judged verdict (yes, maybe or no).
func (r *YieldRow) add(v string) {
	r.N++
	switch v {
	case "yes":
		r.Yes++
	case "maybe":
		r.Maybe++
	}
}

func yieldRate(r YieldRow) string {
	if r.N == 0 {
		return "no data"
	}
	return fmt.Sprintf("%.2f (%.2f-%.2f)", float64(r.Yes)/float64(r.N), r.Lo, r.Hi)
}

// WithoutSelectedOnReading is labels less those drawn from the disagreement
// cells (SelectedOnReading), and how many were left out, for a report that
// reads the labels as a sample and has no exclusion of its own.
func WithoutSelectedOnReading(labels map[posting.Key]Entry) (map[posting.Key]Entry, int) {
	out := make(map[posting.Key]Entry, len(labels))
	n := 0
	for k, e := range labels {
		if SelectedOnReading(e.Source) {
			n++
			continue
		}
		out[k] = e
	}
	return out, n
}
