package eval

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// The auditor: a language model asked the owner's own audit question ("would
// you apply?", internal/decide/wouldapply) about postings the owner has judged
// or marked, so its agreement with the owner can be measured before it is used
// for anything. Its answers live in AuditorFile, in the teacher log's row
// format (TeacherRow, task wouldapply.Task), never in the labels; nothing in the
// engine reads them (Rule 7). This file is the measurement: AUC agreement with
// the owner within the postings the engine kept and within those it dropped,
// against two baselines on the same postings, and the gate that says whether
// the auditor may be trusted.

const (
	// AuditorFile is the auditor's readings log inside the state directory.
	AuditorFile = "auditor_readings.jsonl"

	// AuditorResamples is the bootstrap's resample count.
	AuditorResamples = 2000
	// MinGateClass is the fewest wanted and the fewest unwanted postings a group
	// needs before the gate will decide on it. A percentile bootstrap over a
	// handful of positives is overconfident (two wanted postings separated by
	// luck give an interval that excludes zero), so below this the gate says
	// "not yet" whatever the intervals show.
	MinGateClass = 10
	// minValidShare is the share of resamples that must have both classes for an
	// interval to count; below it the interval is reported as unstable.
	minValidShare = 0.8
	// MaxAuditorItems bounds a report's postings (the bootstrap is
	// O(resamples x n log n)).
	MaxAuditorItems = 5000
)

// Groups of postings, by what the engine did with them.
const (
	GroupKept    = "kept"
	GroupDropped = "dropped"
	GroupClosed  = "closed" // dead or a duplicate when judged: reported, never gated
	GroupMarks   = "marks"  // liked (saved, applied) against hidden
)

// Outcomes the owner's answer is read as.
const (
	OutcomeWanted  = "wanted"   // yes or maybe
	OutcomeYesOnly = "yes_only" // yes
	OutcomeLiked   = "liked"    // saved or applied, against hidden
)

// Gate verdicts.
const (
	GatePasses = "passes"
	GateNotYet = "not yet"
	GateCannot = "cannot be computed"
)

// AuditorItem is one judged posting and the three scores compared on it. A
// score that is missing (no auditor answer, no teacher reading, no relevance)
// leaves the posting out of every paired comparison.
type AuditorItem struct {
	Key   posting.Key
	Group string
	// Wanted and Yes are the owner's audit answer (Wanted: yes or maybe); Liked
	// is the owner's mark. A label item uses the first two, a mark item the third.
	Wanted, Yes, Liked                   bool
	Auditor, Teacher, Relevance          float64
	HasAuditor, HasTeacher, HasRelevance bool
}

// AUCEstimate is one scorer's AUC with its percentile-bootstrap interval.
// Defined is false when the paired set has no wanted or no unwanted posting;
// Interval is false when no resample had both classes (Lo and Hi are then 0
// and mean nothing).
type AUCEstimate struct {
	Defined  bool    `json:"defined"`
	AUC      float64 `json:"auc"`
	Interval bool    `json:"interval"`
	Lo       float64 `json:"lo"`
	Hi       float64 `json:"hi"`
}

// DiffEstimate is the auditor's AUC less a baseline's, paired over the same
// resamples. Stable is false when too few resamples had both classes.
type DiffEstimate struct {
	Defined bool    `json:"defined"`
	Diff    float64 `json:"diff"`
	Lo      float64 `json:"lo"`
	Hi      float64 `json:"hi"`
	Stable  bool    `json:"stable"`
	// Valid is how many of the resamples had both classes.
	Valid int `json:"valid_resamples"`
}

// AuditorRow is one group and outcome.
type AuditorRow struct {
	Group   string `json:"group"`
	Outcome string `json:"outcome"`
	// Judged is every posting in the group; N those with all three scores,
	// Positives and Negatives the owner's split of N.
	Judged    int          `json:"judged"`
	N         int          `json:"n"`
	Positives int          `json:"positives"`
	Negatives int          `json:"negatives"`
	Auditor   AUCEstimate  `json:"auditor"`
	Teacher   AUCEstimate  `json:"teacher"`
	Relevance AUCEstimate  `json:"relevance"`
	VsTeacher DiffEstimate `json:"auditor_minus_teacher"`
	VsRelev   DiffEstimate `json:"auditor_minus_relevance"`
	// Missing counts, among Judged, postings left out for want of each score.
	MissingAuditor   int `json:"missing_auditor"`
	MissingTeacher   int `json:"missing_teacher"`
	MissingRelevance int `json:"missing_relevance"`
}

// AuditorGate is the verdict: GatePasses only when, within the kept and within
// the dropped postings, on the wanted outcome, the auditor's AUC beats both
// baselines with the bootstrap interval of each difference above zero, each
// group holds at least MinGateClass wanted and unwanted postings, and the
// intervals are stable.
type AuditorGate struct {
	Verdict string   `json:"verdict"`
	Why     []string `json:"why"`
	// Reachable says when the gate can decide at all (MinGateClass wanted and
	// not wanted in each of the kept and dropped groups) and where the counts
	// stand now, so a calibration on a handful of labels is not read as a test
	// of the auditor.
	Reachable string `json:"reachable"`
}

// AuditorAgreement is the whole measurement.
type AuditorAgreement struct {
	Rows      []AuditorRow `json:"rows"`
	Gate      AuditorGate  `json:"gate"`
	Resamples int          `json:"resamples"`
	Seed      uint64       `json:"seed"`
}

// AUC is the probability that a random wanted posting scores above a random
// unwanted one, ties counting half (the Mann-Whitney U over average ranks).
// ok is false when either class is empty or a score is not finite.
func AUC(scores []float64, pos []bool) (float64, bool) {
	if len(scores) != len(pos) {
		return 0, false
	}
	n := len(scores)
	idx := make([]int, n)
	var p int
	for i := range n {
		if math.IsNaN(scores[i]) || math.IsInf(scores[i], 0) {
			return 0, false
		}
		idx[i] = i
		if pos[i] {
			p++
		}
	}
	q := n - p
	if p == 0 || q == 0 {
		return 0, false
	}
	sort.Slice(idx, func(a, b int) bool { return scores[idx[a]] < scores[idx[b]] })
	var rankSum float64
	for i := 0; i < n; {
		j := i
		for j+1 < n && scores[idx[j+1]] == scores[idx[i]] {
			j++
		}
		avg := float64(i+j)/2 + 1 // ranks are 1-based
		for k := i; k <= j; k++ {
			if pos[idx[k]] {
				rankSum += avg
			}
		}
		i = j + 1
	}
	u := rankSum - float64(p)*float64(p+1)/2
	return u / (float64(p) * float64(q)), true
}

// auditorStream is the fixed second word of the bootstrap's PCG state.
const auditorStream = 0x61756469_746f7273 // "audi" "tors"

// BuildAuditorAgreement measures items: for each group (kept, dropped, closed)
// and outcome (wanted, yes only), and for the marks, the three AUCs and the
// auditor's paired differences from each baseline, with seeded percentile
// bootstrap intervals; then the gate. The same items and seed give the same
// report.
func BuildAuditorAgreement(items []AuditorItem, seed uint64) (AuditorAgreement, error) {
	if len(items) > MaxAuditorItems {
		return AuditorAgreement{}, fmt.Errorf("eval: %d postings to measure, more than %d", len(items), MaxAuditorItems)
	}
	seen := map[posting.Key]map[string]bool{}
	for _, it := range items {
		if seen[it.Key] == nil {
			seen[it.Key] = map[string]bool{}
		}
		if seen[it.Key][it.Group] {
			return AuditorAgreement{}, fmt.Errorf("eval: posting %s appears twice in group %s", it.Key, it.Group)
		}
		seen[it.Key][it.Group] = true
	}
	out := AuditorAgreement{Resamples: AuditorResamples, Seed: seed}
	type spec struct {
		group, outcome string
		read           func(AuditorItem) bool
	}
	specs := []spec{
		{GroupKept, OutcomeWanted, func(i AuditorItem) bool { return i.Wanted }},
		{GroupKept, OutcomeYesOnly, func(i AuditorItem) bool { return i.Yes }},
		{GroupDropped, OutcomeWanted, func(i AuditorItem) bool { return i.Wanted }},
		{GroupDropped, OutcomeYesOnly, func(i AuditorItem) bool { return i.Yes }},
		{GroupClosed, OutcomeWanted, func(i AuditorItem) bool { return i.Wanted }},
		{GroupClosed, OutcomeYesOnly, func(i AuditorItem) bool { return i.Yes }},
		{GroupMarks, OutcomeLiked, func(i AuditorItem) bool { return i.Liked }},
	}
	for si, s := range specs {
		var in []AuditorItem
		for _, it := range items {
			if it.Group == s.group {
				in = append(in, it)
			}
		}
		if len(in) == 0 && s.group == GroupClosed {
			continue // no closed postings were judged: nothing to report
		}
		// Each row has its own stream so adding a row never moves another's draws.
		out.Rows = append(out.Rows, agreementRow(s.group, s.outcome, in, s.read, seed+uint64(si)))
	}
	out.Gate = auditorGate(out.Rows)
	return out, nil
}

func agreementRow(group, outcome string, in []AuditorItem, read func(AuditorItem) bool, seed uint64) AuditorRow {
	row := AuditorRow{Group: group, Outcome: outcome, Judged: len(in)}
	var aud, tea, rel []float64
	var pos []bool
	for _, it := range in {
		if !it.HasAuditor {
			row.MissingAuditor++
		}
		if !it.HasTeacher {
			row.MissingTeacher++
		}
		if !it.HasRelevance {
			row.MissingRelevance++
		}
		if !it.HasAuditor || !it.HasTeacher || !it.HasRelevance {
			continue
		}
		aud, tea, rel = append(aud, it.Auditor), append(tea, it.Teacher), append(rel, it.Relevance)
		pos = append(pos, read(it))
	}
	row.N = len(pos)
	for _, p := range pos {
		if p {
			row.Positives++
		}
	}
	row.Negatives = row.N - row.Positives
	a, okA := AUC(aud, pos)
	t, okT := AUC(tea, pos)
	r, okR := AUC(rel, pos)
	row.Auditor, row.Teacher, row.Relevance = AUCEstimate{Defined: okA, AUC: a}, AUCEstimate{Defined: okT, AUC: t}, AUCEstimate{Defined: okR, AUC: r}
	if !okA || !okT || !okR {
		return row
	}
	row.VsTeacher = DiffEstimate{Defined: true, Diff: a - t}
	row.VsRelev = DiffEstimate{Defined: true, Diff: a - r}

	rng := rand.New(rand.NewPCG(seed, auditorStream))
	n := row.N
	var sa, st, sr, dt, dr []float64
	ba, bt, br, bp := make([]float64, n), make([]float64, n), make([]float64, n), make([]bool, n)
	for range AuditorResamples {
		for i := range n {
			j := rng.IntN(n)
			ba[i], bt[i], br[i], bp[i] = aud[j], tea[j], rel[j], pos[j]
		}
		xa, ok := AUC(ba, bp)
		if !ok {
			continue // a resample with one class has no AUC
		}
		xt, _ := AUC(bt, bp)
		xr, _ := AUC(br, bp)
		sa, st, sr = append(sa, xa), append(st, xt), append(sr, xr)
		dt, dr = append(dt, xa-xt), append(dr, xa-xr)
	}
	stable := float64(len(sa)) >= minValidShare*AuditorResamples
	row.Auditor.Lo, row.Auditor.Hi, row.Auditor.Interval = percentileInterval(sa)
	row.Teacher.Lo, row.Teacher.Hi, row.Teacher.Interval = percentileInterval(st)
	row.Relevance.Lo, row.Relevance.Hi, row.Relevance.Interval = percentileInterval(sr)
	row.VsTeacher.Lo, row.VsTeacher.Hi, _ = percentileInterval(dt)
	row.VsRelev.Lo, row.VsRelev.Hi, _ = percentileInterval(dr)
	row.VsTeacher.Valid, row.VsRelev.Valid = len(dt), len(dr)
	row.VsTeacher.Stable, row.VsRelev.Stable = stable, stable
	return row
}

// percentileInterval is the 2.5th and 97.5th percentiles of xs; ok is false
// (and both 0) when xs is empty. xs is sorted in place.
func percentileInterval(xs []float64) (lo, hi float64, ok bool) {
	if len(xs) == 0 {
		return 0, 0, false
	}
	slices.Sort(xs)
	cut := int(0.025 * float64(len(xs)))
	return xs[cut], xs[len(xs)-1-cut], true
}

// auditorGate decides from the wanted rows of the kept and dropped groups. A
// group that cannot be measured makes the whole gate GateCannot; one too small
// or not clearly better makes it GateNotYet; it passes only when both pass.
func auditorGate(rows []AuditorRow) AuditorGate {
	g := gateVerdict(rows)
	g.Reachable = gateReachable(rows)
	return g
}

// gateReachable states the gate's floor and the kept and dropped groups'
// wanted / not wanted counts among postings with all three scores.
func gateReachable(rows []AuditorRow) string {
	count := func(group string) (int, int) {
		i := slices.IndexFunc(rows, func(r AuditorRow) bool { return r.Group == group && r.Outcome == OutcomeWanted })
		if i < 0 {
			return 0, 0
		}
		return rows[i].Positives, rows[i].Negatives
	}
	kp, kn := count(GroupKept)
	dp, dn := count(GroupDropped)
	return fmt.Sprintf("The gate can decide only when the kept and the dropped postings each hold at least %d wanted and %d not wanted with all three scores; now: kept %d wanted / %d not wanted, dropped %d wanted / %d not wanted.",
		MinGateClass, MinGateClass, kp, kn, dp, dn)
}

func gateVerdict(rows []AuditorRow) AuditorGate {
	var cannot, notYet []string
	for _, g := range []string{GroupKept, GroupDropped} {
		i := slices.IndexFunc(rows, func(r AuditorRow) bool { return r.Group == g && r.Outcome == OutcomeWanted })
		if i < 0 {
			cannot = append(cannot, g+": no row")
			continue
		}
		r := rows[i]
		switch {
		case r.N == 0:
			cannot = append(cannot, fmt.Sprintf("%s: no posting has an auditor answer, a teacher reading and a relevance score (%d judged; missing auditor %d, teacher %d, relevance %d)",
				g, r.Judged, r.MissingAuditor, r.MissingTeacher, r.MissingRelevance))
		case r.Positives == 0 || r.Negatives == 0:
			cannot = append(cannot, fmt.Sprintf("%s: AUC is undefined (%d wanted, %d not wanted of %d)", g, r.Positives, r.Negatives, r.N))
		case r.Positives < MinGateClass || r.Negatives < MinGateClass:
			notYet = append(notYet, fmt.Sprintf("%s: n=%d (%d wanted, %d not wanted); the gate needs at least %d of each", g, r.N, r.Positives, r.Negatives, MinGateClass))
		case !r.VsTeacher.Stable || !r.VsRelev.Stable:
			notYet = append(notYet, fmt.Sprintf("%s: the bootstrap is unstable (%d of %d resamples had both classes)", g, r.VsTeacher.Valid, AuditorResamples))
		case !(r.VsTeacher.Lo > 0) || !(r.VsRelev.Lo > 0):
			notYet = append(notYet, fmt.Sprintf("%s: n=%d; auditor minus teacher %+.3f [%+.3f, %+.3f], minus relevance %+.3f [%+.3f, %+.3f]; both intervals must lie above 0",
				g, r.N, r.VsTeacher.Diff, r.VsTeacher.Lo, r.VsTeacher.Hi, r.VsRelev.Diff, r.VsRelev.Lo, r.VsRelev.Hi))
		}
	}
	switch {
	case len(cannot) > 0:
		return AuditorGate{Verdict: GateCannot, Why: append(cannot, notYet...)}
	case len(notYet) > 0:
		return AuditorGate{Verdict: GateNotYet, Why: notYet}
	}
	return AuditorGate{Verdict: GatePasses, Why: []string{"within the kept and the dropped postings the auditor's AUC beats the teacher's reading and the relevance score, every difference's 95% interval above 0"}}
}

// Markdown renders the agreement for a terminal.
func (a AuditorAgreement) Markdown() string {
	var sb strings.Builder
	sb.WriteString("## Agreement with the owner (AUC, 95% percentile bootstrap)\n\n")
	sb.WriteString("Only postings with all three scores count (n). AUC 0.5 is chance. Wanted = yes or maybe.\n\n")
	sb.WriteString("| group | outcome | n (wanted/not) | auditor | teacher | relevance | auditor - teacher | auditor - relevance |\n|---|---|---|---|---|---|---|---|\n")
	for _, r := range a.Rows {
		fmt.Fprintf(&sb, "| %s | %s | %d (%d/%d) of %d | %s | %s | %s | %s | %s |\n", r.Group, r.Outcome, r.N, r.Positives, r.Negatives, r.Judged,
			aucCell(r.Auditor), aucCell(r.Teacher), aucCell(r.Relevance), diffCell(r.VsTeacher), diffCell(r.VsRelev))
	}
	fmt.Fprintf(&sb, "\nGate: **%s**\n", a.Gate.Verdict)
	for _, w := range a.Gate.Why {
		fmt.Fprintf(&sb, "- %s\n", w)
	}
	if a.Gate.Reachable != "" {
		fmt.Fprintf(&sb, "\n%s\n", a.Gate.Reachable)
	}
	return sb.String()
}

func aucCell(e AUCEstimate) string {
	if !e.Defined {
		return "undefined"
	}
	if !e.Interval {
		return fmt.Sprintf("%.3f (no interval)", e.AUC)
	}
	return fmt.Sprintf("%.3f [%.3f, %.3f]", e.AUC, e.Lo, e.Hi)
}

func diffCell(d DiffEstimate) string {
	if !d.Defined {
		return "undefined"
	}
	if d.Valid == 0 {
		return fmt.Sprintf("%+.3f (no interval)", d.Diff)
	}
	s := fmt.Sprintf("%+.3f [%+.3f, %+.3f]", d.Diff, d.Lo, d.Hi)
	if !d.Stable {
		s += " unstable"
	}
	return s
}
