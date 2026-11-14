package eval

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

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

// The kill rule for the yes-seeking cells (teacher wants, yes-neighbour),
// reworked 2026-11-14 before any cell label exists (Fable, after the audit
// benchmark): the old rule kept a cell at 4 yes anywhere from 20 to 39 judged,
// and kept a cell no better than random 39% of the time.
//
// Two looks, each on the cell's first labels by answer time, latched:
//   - at KillFirstLook judged, KillOffYes or fewer yes turns the cell off; there
//     is no early keep, since a kept cell and an undecided one both keep drawing;
//   - at KillSecondLook judged, the cell is kept when it has at least
//     KillKeepYes yes and its yes rate is at least KillKeepRatio times its
//     comparator's (the comparator's counts frozen at the cell's
//     KillSecondLook-th answer); otherwise it is turned off. A comparator with
//     no labels cannot decide, and that is never a keep.
//
// The wants cell's comparator is the random strata pooled; the yes-neighbour
// cell's is GroupDeciles, since its pool is mostly kept postings.
//
// The teacher-rejects cell is not under this rule. A no there is the engine
// error it exists to find, so "few yes turns it off" would kill it when it
// works, and at two picks a week no yes rule has power. At RejectsLook judged
// its no rate is set beside the top three deciles' and the owner decides.
const (
	KillFirstLook  = 20
	KillSecondLook = 40
	KillOffYes     = 1
	KillKeepYes    = 5
	KillKeepRatio  = 2
	RejectsLook    = 20
)

// The kill rule's verdicts.
const (
	CellCollecting   = "collecting"
	CellTurnOff      = "turn_off"
	CellKeep         = "keep"
	CellRunOn        = "run_to_40"
	CellOwnerCall    = "owner_call"
	CellCannotDecide = "cannot_decide"
)

// CellVerdict is the kill rule applied to one cell's judged labels. N, Yes and
// No are the cell's counts now; Why gives the counts the verdict was taken on
// (the looks are latched on the first answers). Comparator and its counts are
// what the cell was held against: frozen at the second look once it is reached,
// the comparator's counts now before then.
type CellVerdict struct {
	Cell       string `json:"cell"`
	N          int    `json:"judged"`
	Yes        int    `json:"yes"`
	No         int    `json:"no"`
	Verdict    string `json:"verdict"`
	Comparator string `json:"comparator,omitempty"`
	CompN      int    `json:"comparator_judged"`
	CompYes    int    `json:"comparator_yes"`
	CompNo     int    `json:"comparator_no"`
	Why        string `json:"why"`
}

// firstLook is the kill rule's first look: yes of the cell's first
// KillFirstLook answers. It reports whether the cell is turned off.
func firstLook(yes int) bool { return yes <= KillOffYes }

// secondLook is the kill rule's second look on yes of the cell's first
// KillSecondLook answers against compYes of compN comparator answers.
func secondLook(yes, compN, compYes int) string {
	switch {
	case compN == 0:
		return CellCannotDecide
	case yes >= KillKeepYes && yes*compN >= KillKeepRatio*KillSecondLook*compYes:
		return CellKeep
	}
	return CellTurnOff
}

// answer is one judged label with its answer time, for the latched looks.
type answer struct {
	at  time.Time
	key posting.Key
	v   string
}

// byAnswerTime orders answers by time, then key, so equal times stay stable.
func byAnswerTime(xs []answer) {
	slices.SortFunc(xs, func(a, b answer) int {
		if c := a.at.Compare(b.at); c != 0 {
			return c
		}
		return strings.Compare(string(a.key), string(b.key))
	})
}

// tally counts xs: judged, yes and no.
func tally(xs []answer) (n, yes, no int) {
	for _, x := range xs {
		n++
		switch x.v {
		case "yes":
			yes++
		case "no":
			no++
		}
	}
	return n, yes, no
}

// until is the answers in xs (ordered) given at or before t.
func until(xs []answer, t time.Time) []answer {
	i := 0
	for i < len(xs) && !xs[i].at.After(t) {
		i++
	}
	return xs[:i]
}

// yesCellVerdict is the kill rule on a yes-seeking cell's answers against its
// comparator's, both ordered by answer time.
func yesCellVerdict(cell string, xs []answer, compName string, comp []answer) CellVerdict {
	n, yes, no := tally(xs)
	cn, cy, cno := tally(comp)
	v := CellVerdict{Cell: cell, N: n, Yes: yes, No: no, Comparator: compName, CompN: cn, CompYes: cy, CompNo: cno}
	if n < KillFirstLook {
		v.Verdict = CellCollecting
		v.Why = fmt.Sprintf("%d of %d judged before the first look", n, KillFirstLook)
		return v
	}
	_, y20, _ := tally(xs[:KillFirstLook])
	if firstLook(y20) {
		v.Verdict = CellTurnOff
		v.Why = fmt.Sprintf("first look: %d yes in its first %d answers (%d or fewer turns it off)", y20, KillFirstLook, KillOffYes)
		return v
	}
	if n < KillSecondLook {
		v.Verdict = CellRunOn
		v.Why = fmt.Sprintf("first look passed (%d yes in %d); %d of %d judged before the second look", y20, KillFirstLook, n, KillSecondLook)
		return v
	}
	_, y40, _ := tally(xs[:KillSecondLook])
	v.CompN, v.CompYes, v.CompNo = tally(until(comp, xs[KillSecondLook-1].at))
	v.Verdict = secondLook(y40, v.CompN, v.CompYes)
	switch v.Verdict {
	case CellCannotDecide:
		v.Why = fmt.Sprintf("second look: %d yes in its first %d answers, but %s had no answers by then", y40, KillSecondLook, compName)
	default:
		v.Why = fmt.Sprintf("second look: %d yes in its first %d answers against %s's %d of %d (keep needs %d or more yes and at least %dx the comparator's rate)",
			y40, KillSecondLook, compName, v.CompYes, v.CompN, KillKeepYes, KillKeepRatio)
	}
	return v
}

// rejectsCellVerdict sets the teacher-rejects cell's no rate beside the top
// three deciles' once it has RejectsLook answers; the owner decides.
func rejectsCellVerdict(xs []answer, top []answer) CellVerdict {
	n, yes, no := tally(xs)
	cn, cy, cno := tally(top)
	v := CellVerdict{Cell: CellTeacherRejects, N: n, Yes: yes, No: no, Comparator: GroupTopDeciles, CompN: cn, CompYes: cy, CompNo: cno}
	if n < RejectsLook {
		v.Verdict = CellCollecting
		v.Why = fmt.Sprintf("%d of %d judged before its no rate is shown", n, RejectsLook)
		return v
	}
	v.Verdict = CellOwnerCall
	v.Why = fmt.Sprintf("no in %d of %d answers, against %d of %d in %s; no yes rule applies here (a no is the engine error the cell looks for), so keeping it is the owner's call", no, n, cno, cn, GroupTopDeciles)
	return v
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
	// answers keeps each cell's and comparator's answers with their times for
	// the kill rule's latched looks.
	answers := map[string][]answer{}
	for k, e := range labels {
		v, ok := e.Labels[WouldApply]
		if !ok {
			continue
		}
		if _, judged := wanted(v); !judged {
			continue
		}
		random := auditStratum(e.Source)
		if !random && !SelectedOnReading(e.Source) && !EngineeringStratum(e.Source) {
			continue
		}
		r := rows[e.Source]
		if r == nil {
			r = &YieldRow{Source: e.Source}
			rows[e.Source] = r
		}
		r.add(v)
		a := answer{at: e.LabeledAt, key: k, v: v}
		answers[e.Source] = append(answers[e.Source], a)
		if random {
			out.Random.add(v)
			answers[out.Random.Source] = append(answers[out.Random.Source], a)
		}
		for _, g := range groupsOf(e.Source) {
			if groups[g] == nil {
				groups[g] = &YieldRow{Source: g}
			}
			groups[g].add(v)
			answers[g] = append(answers[g], a)
		}
	}
	for _, xs := range answers {
		byAnswerTime(xs)
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
	if xs := answers[CellTeacherWants]; len(xs) > 0 {
		out.Cells = append(out.Cells, yesCellVerdict(CellTeacherWants, xs, out.Random.Source, answers[out.Random.Source]))
	}
	if xs := answers[CellTeacherRejects]; len(xs) > 0 {
		out.Cells = append(out.Cells, rejectsCellVerdict(xs, answers[GroupTopDeciles]))
	}
	if xs := answers[CellYesNeighbour]; len(xs) > 0 {
		out.Cells = append(out.Cells, yesCellVerdict(CellYesNeighbour, xs, GroupDeciles, answers[GroupDeciles]))
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
		case best == CellYesNeighbour:
			how = "raise --yes-neighbours (at most twice --scored + --dropped, with the cells)"
		case EngineeringStratum(best):
			how = "raise --engineering"
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
		fmt.Fprintf(&b, "\nKill rule, %s: %d yes of %d judged: %s (%s).\n", c.Cell, c.Yes, c.N, c.Verdict, c.Why)
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
