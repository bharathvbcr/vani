package eval

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
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
// Random is every random stratum pooled (deciles, drop classes, closed).
// Groups pools the strata the quotas actually move between (GroupDeciles,
// GroupTopDeciles, GroupDrops): five scored places a week over ten deciles is
// half a label per decile, so a single decile takes months to reach
// MinYieldJudged and only a group can be named in time (second Fable review,
// 2026-11-13). Richest is the row or group with the highest Wilson lower bound
// among those with at least MinYieldJudged labels, named only when that bound
// is above Random's upper bound (the intervals do not overlap, so a few random
// labels cannot make a stratum look rich); Advice says so in words, or why none
// is named. Cells is the kill rule's verdict on each disagreement cell.
type YesYield struct {
	Rows    []YieldRow    `json:"rows"`
	Groups  []YieldRow    `json:"groups"`
	Random  YieldRow      `json:"random"`
	Richest string        `json:"richest,omitempty"`
	Advice  string        `json:"advice"`
	Cells   []CellVerdict `json:"cells"`
}

// The yes yield's groups of random strata.
const (
	GroupDeciles    = "group:all_deciles"
	GroupTopDeciles = "group:top_three_deciles"
	GroupDrops      = "group:all_drop_classes"
)

// The disagreement cells' kill rule, fixed before any cell label exists
// (second Fable review): at KillFirstLook judged labels a cell with at most
// KillOffYes yes is turned off and one with at least KillKeepYes is kept;
// between, it runs to KillSecondLook judged, where the owner decides on the
// counts.
const (
	KillFirstLook  = 20
	KillSecondLook = 40
	KillOffYes     = 1
	KillKeepYes    = 4
)

// The kill rule's verdicts.
const (
	CellCollecting = "collecting"
	CellTurnOff    = "turn_off"
	CellKeep       = "keep"
	CellRunOn      = "run_to_40"
	CellOwnerCall  = "owner_call"
)

// CellVerdict is the kill rule applied to one disagreement cell's judged
// labels.
type CellVerdict struct {
	Cell    string `json:"cell"`
	N       int    `json:"judged"`
	Yes     int    `json:"yes"`
	Verdict string `json:"verdict"`
}

// killVerdict is the kill rule on n judged labels with yes of them yes.
func killVerdict(n, yes int) string {
	switch {
	case n < KillFirstLook:
		return CellCollecting
	case n < KillSecondLook && yes <= KillOffYes:
		return CellTurnOff
	case n < KillSecondLook && yes >= KillKeepYes:
		return CellKeep
	case n < KillSecondLook:
		return CellRunOn
	}
	return CellOwnerCall
}

// groupsOf are the groups source counts in.
func groupsOf(source string) []string {
	switch {
	case strings.HasPrefix(source, "decile:"):
		g := []string{GroupDeciles}
		if n, err := strconv.Atoi(strings.TrimPrefix(source, "decile:")); err == nil && n >= 8 && n <= 10 {
			g = append(g, GroupTopDeciles)
		}
		return g
	case strings.HasPrefix(source, DroppedPrefix):
		return []string{GroupDrops}
	}
	return nil
}

// BuildYesYield counts the judged would_apply labels by their Source. A manual
// label, one from Explore or another surface, and a skip are not counted: none
// was drawn from a stratum.
func BuildYesYield(labels map[posting.Key]Entry) YesYield {
	rows, groups := map[string]*YieldRow{}, map[string]*YieldRow{}
	out := YesYield{Rows: []YieldRow{}, Groups: []YieldRow{}, Random: YieldRow{Source: "random strata"}, Cells: []CellVerdict{}}
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
		for _, g := range groupsOf(e.Source) {
			if groups[g] == nil {
				groups[g] = &YieldRow{Source: g}
			}
			groups[g].add(v)
		}
	}
	for _, src := range slices.Sorted(maps.Keys(rows)) {
		r := *rows[src]
		r.Lo, r.Hi, _ = Wilson(r.Yes, r.N)
		out.Rows = append(out.Rows, r)
	}
	for _, g := range []string{GroupDeciles, GroupTopDeciles, GroupDrops} {
		if r := groups[g]; r != nil {
			r.Lo, r.Hi, _ = Wilson(r.Yes, r.N)
			out.Groups = append(out.Groups, *r)
		}
	}
	out.Random.Lo, out.Random.Hi, _ = Wilson(out.Random.Yes, out.Random.N)
	for _, cell := range []string{CellTeacherWants, CellTeacherRejects} {
		if r := rows[cell]; r != nil {
			out.Cells = append(out.Cells, CellVerdict{Cell: cell, N: r.N, Yes: r.Yes, Verdict: killVerdict(r.N, r.Yes)})
		}
	}

	best, bestLo := "", -1.0
	for _, r := range append(slices.Clone(out.Groups), out.Rows...) {
		if r.N >= MinYieldJudged && r.Lo > bestLo {
			best, bestLo = r.Source, r.Lo
		}
	}
	switch {
	case out.Random.N == 0:
		out.Advice = "no data: no judged label drawn from a random stratum yet, so nothing to compare a stratum with"
	case best == "":
		out.Advice = fmt.Sprintf("none named: no stratum has %d judged labels yet", MinYieldJudged)
	case bestLo <= out.Random.Hi:
		out.Advice = fmt.Sprintf("none named: no stratum's yes rate is clearly above the random strata's (%d of %d, upper bound %.2f)", out.Random.Yes, out.Random.N, out.Random.Hi)
	default:
		out.Richest = best
		how := "give its group more of the draw's places"
		switch {
		case best == CellTeacherWants:
			how = "raise --disagree (at most twice --scored + --dropped)"
		case best == CellTeacherRejects:
			how = "raise --disagree-rejects (at most twice --scored + --dropped)"
		case best == GroupDeciles || best == GroupTopDeciles:
			how = "move places from --dropped to --scored"
		case best == GroupDrops:
			how = "move places from --scored to --dropped"
		}
		out.Advice = fmt.Sprintf("%s is yes-rich: its 95%% lower bound %.2f is above the random strata's upper bound %.2f (%d of %d); %s. The quotas are the owner's call.", best, bestLo, out.Random.Hi, out.Random.Yes, out.Random.N, how)
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
	for _, r := range append(append(slices.Clone(y.Rows), y.Groups...), y.Random) {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %s |\n", r.Source, r.N, r.Yes, r.Maybe, yieldRate(r))
	}
	fmt.Fprintf(&b, "\n%s\n", y.Advice)
	for _, c := range y.Cells {
		fmt.Fprintf(&b, "\nKill rule, %s: %d yes of %d judged: %s (at %d judged, %d or fewer yes turns it off, %d or more keeps it, between runs to %d).\n",
			c.Cell, c.Yes, c.N, c.Verdict, KillFirstLook, KillOffYes, KillKeepYes, KillSecondLook)
	}
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
