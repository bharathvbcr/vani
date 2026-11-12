package eval

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// The drop-recall estimate: of the postings the owner would apply to, what
// share did the engine drop, overall and per drop class.
//
// It is post-stratified, and it does not use p_select. A label's p_select is
// relative to the unlabelled pool of the week it was drawn (AuditSample), so
// as the ledger grows the same stratum's p_select falls (0.0017 to 0.00029 for
// the deciles between 2026-10-22 and 11-03 in the live labels): weighting by
// 1/p_select would count late labels 6 to 9 times more than early ones.
// Within a stratum every weekly draw is uniform over its unlabelled members,
// so each label there is an equally likely draw, and the stratum's wanted rate
// is the plain share wanted among its labels. Each rate is then scaled by the
// stratum's pool as it stands now.
//
// The strata are the cells postings sit in now (AuditCell): kept (the score
// deciles merged: each decile is a tenth of the kept pool and gets the same
// places, so the deciles' labels are uniform over it) and each drop class. A
// label counts in a cell only if it was drawn from that cell and its posting
// is still there; one drawn from another stratum (a kept posting since
// dropped, a drop since reopened) is not a uniform draw of where it sits now
// and is left out as moved. A posting the owner marked is known, not sampled:
// saved or applied counts as wanted (and yes), hidden as not.
//
// Each cell's rate gets a Jeffreys Beta(k+½, n-k+½) posterior: its intervals
// cover well for small n, and unlike a uniform Beta(1, 1) prior it does not
// pull a 0 of 5 halfway to ½ (Jeffreys puts that cell's 97.5% point near 0.38,
// uniform near 0.46). Counts and shares are combined by seeded Monte Carlo
// over independent draws of every cell (RecallDraws, fixed seed, so the report
// is stable); the figures are posterior medians with 95% intervals, and the
// raw point is kept beside them (Observed). A cell with no labels is unknown:
// nothing is extrapolated into it, so while any cell holding unmarked postings
// has none, nothing is combined. A share is reported only when every such cell
// has at least MinRecallCellJudged labels (or all of its postings).
//
// Both readings of "would apply" are estimated: wanted (yes or maybe) and yes
// only, which diverge a lot on the kept side.
const (
	MinRecallCellJudged = 3
	RecallDraws         = 4000
	// RecallBoundTarget is the miss rate a drop class's one-sided 95% upper
	// bound is asked to reach; LabelsFor5pct says how many more zero-miss
	// labels that takes.
	RecallBoundTarget = 0.05
	recallAlpha       = 0.05
	recallSeed1       = 0x7661_6e69 // "vani"
	recallSeed2       = 0x7265_6361 // "reca"
)

// RecallMember is one posting of the pool the estimate stands for: its cell
// now (AuditCell) and, when the owner marked it, whether the mark says wanted
// (saved or applied) or not (hidden).
type RecallMember struct {
	Key  posting.Key
	Cell string
	// LegacyCell is the cell the posting had under a drop-class name since
	// renamed (LegacyDropClass), when it differs from Cell: a label drawn
	// from it was drawn from this posting's cause under its old name.
	LegacyCell   string
	Marked       bool
	MarkedWanted bool
}

// RecallRate is one cell's rate under one reading of "would apply".
type RecallRate struct {
	// K of the cell's Judged labels said so; Rate is K/Judged.
	K    int     `json:"k"`
	Rate float64 `json:"rate"`
	// Lo and Hi are the rate's 95% posterior interval.
	Lo float64 `json:"lo"`
	Hi float64 `json:"hi"`
	// Est is the posterior median of Pool x rate + the marked ones that count,
	// the postings in the cell the owner would apply to, with its interval.
	Est   float64 `json:"est"`
	EstLo float64 `json:"est_lo"`
	EstHi float64 `json:"est_hi"`
}

// RecallCell is one cell's part of the estimate.
type RecallCell struct {
	Cell string `json:"cell"`
	// Pool is the unmarked postings in the cell now, labelled or not: what the
	// labels stand for. Marked and MarkedWanted are the census beside it.
	Pool         int `json:"pool"`
	Marked       int `json:"marked"`
	MarkedWanted int `json:"marked_wanted"`
	// Judged is the owner's yes, maybe or no answers drawn from this cell on
	// postings still in it (and unmarked).
	Judged int `json:"judged"`
	// Known is false when nothing is judged and the pool is not empty: the
	// cell's rate is unknown, and Wanted and Yes are left zero, not estimated.
	Known  bool       `json:"known"`
	Wanted RecallRate `json:"wanted"` // yes or maybe
	Yes    RecallRate `json:"yes"`    // yes only
	// For a drop class: the exact one-sided 95% upper bound on its wanted rate
	// (1 when nothing is judged), counting KnownMisses (postings in the class
	// the owner saved or applied to) as wanted observations beside the judged
	// labels, and how many more labels, all of them not wanted, would bring it
	// to RecallBoundTarget.
	UpperBound    float64 `json:"upper_bound_95"`
	LabelsFor5pct int     `json:"labels_for_5pct_bound"`
	KnownMisses   int     `json:"known_misses"`
	// PriorDominated is true when the Jeffreys prior alone would put a 0-of-n
	// cell's mean at or above RecallBoundTarget (0.5/(Judged+1) >= target):
	// its rate and count reflect the prior more than the labels.
	PriorDominated bool `json:"prior_dominated"`
}

// RecallShare is a share of all wanted postings: the posterior median with
// its 95% interval, and the raw point (Observed) beside it.
type RecallShare struct {
	Cell     string  `json:"cell"`
	Share    float64 `json:"share"`
	Lo       float64 `json:"lo"`
	Hi       float64 `json:"hi"`
	Observed float64 `json:"observed"`
}

// Recall statuses.
const (
	RecallEstimated    = "estimated"
	RecallInsufficient = "insufficient"
)

// DropRecall is the estimate. Missed and MissedYes (the share of wanted, and
// of yes, postings that were dropped) and the ByClass lists are set only when
// Status is RecallEstimated; Reason says why not otherwise.
type DropRecall struct {
	Status      string        `json:"status"`
	Reason      string        `json:"reason,omitempty"`
	Cells       []RecallCell  `json:"cells"`
	Missed      *RecallShare  `json:"missed,omitempty"`
	MissedYes   *RecallShare  `json:"missed_yes,omitempty"`
	ByClass     []RecallShare `json:"by_class,omitempty"`
	ByClassYes  []RecallShare `json:"by_class_yes,omitempty"`
	OutOfPool   int           `json:"out_of_pool"` // posting closed, untriaged or gone
	Moved       int           `json:"moved"`       // drawn from another stratum than it sits in now
	Renamed     int           `json:"renamed"`     // drawn from its cell under an old class name; counted in the cell
	NotAudit    int           `json:"not_audit"`   // drawn by Explore or another surface, not an audit stratum
	Manual      int           `json:"manual"`      // no sampling stratum
	MarkedOut   int           `json:"marked"`      // counted by the mark instead
	Draws       int           `json:"draws"`
	MinJudged   int           `json:"min_judged_per_cell"`
	BoundTarget float64       `json:"bound_target"`
}

// inRecallPool reports whether a cell is part of the recall population: kept
// or dropped. Closed postings are neither kept nor dropped by a decision.
func inRecallPool(cell string) bool {
	return cell == CellKept || strings.HasPrefix(cell, DroppedPrefix)
}

// auditStratum reports whether source names a stratum the weekly audit draws
// from: a score decile, a drop class or a closed status. Anything else (an
// Explore row's "explore:…") was picked by another surface.
func auditStratum(source string) bool {
	return strings.HasPrefix(source, "decile:") || strings.HasPrefix(source, DroppedPrefix) || strings.HasPrefix(source, ClosedPrefix)
}

// drawnFrom reports whether a label drawn from source counts for cell: kept
// takes any decile, a drop class only its own stratum.
func drawnFrom(source, cell string) bool {
	if cell == CellKept {
		return strings.HasPrefix(source, "decile:")
	}
	return source == cell
}

type recallAcc struct {
	RecallCell
	nWanted, nYes int
}

// InsufficientDropRecall is an estimate that could not be made, for reason,
// with the settings every reply carries (draws, the per-cell minimum, the bound
// target). Every insufficient reply starts here, so none leaves them unset.
func InsufficientDropRecall(reason string) DropRecall {
	return DropRecall{Status: RecallInsufficient, Reason: reason, Cells: []RecallCell{}, Draws: RecallDraws,
		MinJudged: MinRecallCellJudged, BoundTarget: RecallBoundTarget}
}

// BuildDropRecall estimates drop recall from the would_apply labels and the
// pool as it stands now. members lists every kept and dropped posting (closed
// ones are ignored); a key listed twice counts once.
func BuildDropRecall(labels map[posting.Key]Entry, members []RecallMember) DropRecall {
	out := InsufficientDropRecall("")
	cells := map[string]*recallAcc{}
	where := map[posting.Key]RecallMember{}
	for _, m := range members {
		if m.Key == "" || !inRecallPool(m.Cell) {
			continue
		}
		if _, dup := where[m.Key]; dup {
			continue
		}
		where[m.Key] = m
		c := cells[m.Cell]
		if c == nil {
			c = &recallAcc{RecallCell: RecallCell{Cell: m.Cell}}
			cells[m.Cell] = c
		}
		switch {
		case !m.Marked:
			c.Pool++
		case m.MarkedWanted:
			c.Marked++
			c.MarkedWanted++
		default:
			c.Marked++
		}
	}
	for k, e := range labels {
		v, ok := e.Labels[WouldApply]
		if !ok {
			continue
		}
		hit, judged := wanted(v)
		if !judged {
			continue
		}
		m, in := where[k]
		switch {
		case e.Source == SourceManual || e.Source == "":
			out.Manual++
			continue
		case !auditStratum(e.Source):
			out.NotAudit++ // Explore or another surface: not a uniform draw of any cell
			continue
		case !in:
			out.OutOfPool++
			continue
		case m.Marked:
			out.MarkedOut++
			continue
		case drawnFrom(e.Source, m.Cell):
		case m.LegacyCell != "" && e.Source == m.LegacyCell:
			out.Renamed++ // its cell under an old class name: the same cause
		default:
			out.Moved++
			continue
		}
		c := cells[m.Cell]
		c.Judged++
		if hit {
			c.nWanted++
		}
		if v == "yes" {
			c.nYes++
		}
	}

	names := make([]string, 0, len(cells))
	for n := range cells {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if (names[i] == CellKept) != (names[j] == CellKept) {
			return names[i] == CellKept
		}
		return names[i] < names[j]
	})
	var short, unknown []string
	anyDropped := false
	for _, n := range names {
		c := cells[n]
		c.Known = c.Judged > 0 || c.Pool == 0
		c.PriorDominated = c.Judged > 0 && priorDominated(c.Judged)
		if strings.HasPrefix(n, DroppedPrefix) {
			anyDropped = true
			// A saved or applied posting in a drop class is a miss the owner
			// already found. It is counted as a wanted observation beside the
			// judged labels, which can only raise the bound; hidden ones are
			// not counted as not-wanted, since marks are picked from the top of
			// a ranking, not drawn at random.
			c.KnownMisses = c.MarkedWanted
			k, n := c.nWanted+c.KnownMisses, c.Judged+c.KnownMisses
			c.UpperBound = upperBound(k, n, recallAlpha)
			c.LabelsFor5pct = labelsForBound(k, n)
		}
		if !c.Known {
			unknown = append(unknown, n)
		}
		if need := min(MinRecallCellJudged, c.Pool); c.Judged < need {
			short = append(short, fmt.Sprintf("%s (%d of %d judged, pool %d)", n, c.Judged, need, c.Pool))
		}
	}

	wantedSims := simulateRecall(cells, names, func(c *recallAcc) int { return c.nWanted })
	yesSims := simulateRecall(cells, names, func(c *recallAcc) int { return c.nYes })
	for _, n := range names {
		c := cells[n]
		if c.Known {
			c.Wanted = rateOf(c, c.nWanted, wantedSims, n)
			c.Yes = rateOf(c, c.nYes, yesSims, n)
		}
		out.Cells = append(out.Cells, c.RecallCell)
	}

	switch {
	case len(cells) == 0:
		out.Reason = "no kept or dropped postings in the pool"
		return out
	case !anyDropped:
		out.Reason = "nothing is dropped: there is no recall to estimate"
		return out
	case len(unknown) > 0 || len(short) > 0:
		out.Reason = fmt.Sprintf("every cell needs at least %d judged labels (or all of its postings), and a cell with none is unknown, not 0: short are %s",
			MinRecallCellJudged, strings.Join(short, "; "))
		return out
	case len(wantedSims.missed) == 0:
		out.Reason = "no wanted posting is possible in any cell (every cell is empty or all marked unwanted)"
		return out
	}
	out.Status = RecallEstimated
	out.Missed, out.ByClass = shares(cells, names, wantedSims, func(c *recallAcc) float64 { return float64(c.nWanted) })
	if len(yesSims.missed) > 0 {
		out.MissedYes, out.ByClassYes = shares(cells, names, yesSims, func(c *recallAcc) float64 { return float64(c.nYes) })
	}
	return out
}

// rateOf is one cell's rate under one reading, from its draws.
func rateOf(c *recallAcc, k int, sims recallSims, cell string) RecallRate {
	r := RecallRate{K: k}
	if c.Judged > 0 {
		r.Rate = float64(k) / float64(c.Judged)
	}
	if s := sims.rate[cell]; len(s) > 0 {
		r.Lo, r.Hi = quantile(s, 0.025), quantile(s, 0.975)
	}
	if e := sims.est[cell]; len(e) > 0 {
		r.Est, r.EstLo, r.EstHi = quantile(e, 0.5), quantile(e, 0.025), quantile(e, 0.975)
	}
	return r
}

// shares is the dropped share of all postings counted by k, overall and per
// drop class: posterior medians and intervals, with the raw points.
func shares(cells map[string]*recallAcc, names []string, sims recallSims, k func(*recallAcc) float64) (*RecallShare, []RecallShare) {
	point := func(class string) float64 {
		var all, part float64
		for _, n := range names {
			c := cells[n]
			w := float64(c.MarkedWanted)
			if c.Judged > 0 {
				w += float64(c.Pool) * k(c) / float64(c.Judged)
			}
			all += w
			if strings.HasPrefix(n, DroppedPrefix) && (class == "" || n == class) {
				part += w
			}
		}
		if all <= 0 {
			return 0
		}
		return part / all
	}
	total := &RecallShare{Cell: "all drops", Share: quantile(sims.missed, 0.5), Lo: quantile(sims.missed, 0.025),
		Hi: quantile(sims.missed, 0.975), Observed: point("")}
	var by []RecallShare
	for _, n := range names {
		if !strings.HasPrefix(n, DroppedPrefix) {
			continue
		}
		s := sims.byClass[n]
		by = append(by, RecallShare{Cell: n, Share: quantile(s, 0.5), Lo: quantile(s, 0.025), Hi: quantile(s, 0.975), Observed: point(n)})
	}
	return total, by
}

type recallSims struct {
	rate, est, byClass map[string][]float64
	missed             []float64
}

// simulateRecall draws each known cell's rate from its Jeffreys posterior,
// with k(c) of its Judged labels counting, and records the cell's rate and
// count, the dropped share of all counted postings, and each class's share. A
// cell that is not known takes no draws: its postings are unknown, never 0,
// and the caller combines nothing while one exists. A draw in which nothing
// counts anywhere is left out of the ratios.
func simulateRecall(cells map[string]*recallAcc, names []string, k func(*recallAcc) int) recallSims {
	out := recallSims{rate: map[string][]float64{}, est: map[string][]float64{}, byClass: map[string][]float64{}}
	r := rand.New(rand.NewPCG(recallSeed1, recallSeed2))
	w := make([]float64, len(names))
	for range RecallDraws {
		var all, drop float64
		for i, n := range names {
			c := cells[n]
			w[i] = float64(c.MarkedWanted)
			if c.Judged > 0 {
				kk := float64(k(c))
				rate := betaSample(r, kk+0.5, float64(c.Judged)-kk+0.5)
				out.rate[n] = append(out.rate[n], rate)
				w[i] += float64(c.Pool) * rate
			} else if c.Pool > 0 {
				continue // unknown: no draw
			}
			out.est[n] = append(out.est[n], w[i])
			all += w[i]
			if strings.HasPrefix(n, DroppedPrefix) {
				drop += w[i]
			}
		}
		if all <= 0 {
			continue
		}
		out.missed = append(out.missed, drop/all)
		for i, n := range names {
			if strings.HasPrefix(n, DroppedPrefix) {
				out.byClass[n] = append(out.byClass[n], w[i]/all)
			}
		}
	}
	return out
}

// upperBound is the exact (Clopper-Pearson) one-sided upper confidence bound
// at level 1-alpha on a rate with k of n: 1 for n = 0.
func upperBound(k, n int, alpha float64) float64 {
	if n <= 0 {
		return 1
	}
	if k == 0 {
		return 1 - math.Pow(alpha, 1/float64(n))
	}
	return 1 - lowerBound(n-k, n, alpha)
}

// maxBoundLabels caps labelsForBound's search: more labels than this in one
// class is beyond any audit Vani runs, and the answer is reported as -1.
const maxBoundLabels = 1_000_000

// labelsForBound is how many more labels, none of them wanted, bring a class
// with k wanted of n to a one-sided 95% upper bound of RecallBoundTarget: the
// smallest total N >= n with upperBound(k, N) <= target, less n. With k = 0
// that is ln(alpha) / ln(1 - target) = 59 in all; one miss needs about 93.
// -1 only when no N up to maxBoundLabels reaches it.
func labelsForBound(k, n int) int {
	if k <= 0 {
		need := int(math.Ceil(math.Log(recallAlpha) / math.Log(1-RecallBoundTarget)))
		return max(0, need-n)
	}
	from := max(n, k) // a total below k cannot hold k misses
	if upperBound(k, from, recallAlpha) <= RecallBoundTarget {
		return max(0, from-n)
	}
	if upperBound(k, maxBoundLabels, recallAlpha) > RecallBoundTarget {
		return -1
	}
	lo, hi := from, maxBoundLabels // bound(lo) > target >= bound(hi); the bound falls as N grows
	for hi-lo > 1 {
		mid := lo + (hi-lo)/2
		if upperBound(k, mid, recallAlpha) <= RecallBoundTarget {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi - n
}

// priorDominated reports whether a cell with n judged labels is ruled by its
// Jeffreys prior: the prior alone puts a 0-of-n cell's mean, 0.5/(n+1), at or
// above RecallBoundTarget, the miss rate the report is trying to bound (n <= 9
// at 5%). Below that the cell's median says more about the prior than the
// labels.
func priorDominated(n int) bool { return 0.5/float64(n+1) >= RecallBoundTarget }

// quantile is the q-quantile of xs by the nearest-rank method; xs is sorted in
// place. NaN for an empty slice.
func quantile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	sort.Float64s(xs)
	i := int(math.Ceil(q*float64(len(xs)))) - 1
	return xs[max(0, min(i, len(xs)-1))]
}

// betaSample draws from Beta(a, b), a, b > 0, as X/(X+Y) of two gamma draws.
func betaSample(r *rand.Rand, a, b float64) float64 {
	x, y := gammaSample(r, a), gammaSample(r, b)
	if x+y == 0 {
		return 0.5 // both underflowed: a and b are tiny and the draw is a coin
	}
	return x / (x + y)
}

// gammaSample draws from Gamma(a, 1), a > 0: Marsaglia and Tsang's method,
// with the u^(1/a) boost for a < 1.
func gammaSample(r *rand.Rand, a float64) float64 {
	if a < 1 {
		return gammaSample(r, a+1) * math.Pow(r.Float64(), 1/a)
	}
	d := a - 1.0/3
	c := 1 / math.Sqrt(9*d)
	for {
		x := r.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := r.Float64()
		if u < 1-0.0331*x*x*x*x || math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}

// Markdown renders the estimate.
func (d DropRecall) Markdown() string {
	var b strings.Builder
	b.WriteString("\n## Drop recall estimate: postings you would apply to that the engine dropped\n\n")
	fmt.Fprintf(&b, "Post-stratified: each cell's rate among the labels drawn from it (p_select is not used: it shrinks as the pool grows), times the cell's pool now; marked postings count by their mark. Jeffreys intervals, combined over %d seeded draws; figures are posterior medians with 95%% intervals. A cell with no labels is unknown, and nothing is combined while one exists. \"wanted\" is yes or maybe, \"yes\" is yes only. A cell's labels are pooled across the weeks they were drawn in, which assumes its wanted rate is stable over those weeks; new boards or rule changes can break that. A cell marked prior-dominated has so few labels (%d or fewer) that the prior alone puts a 0-of-n mean at %.0f%% or more: its figures mostly reflect the prior, not the owner.\n\n", d.Draws, maxPriorDominatedJudged(), 100*RecallBoundTarget)
	if len(d.Cells) > 0 {
		b.WriteString("| cell | pool | marked (wanted) | judged | wanted: rate (95%) | wanted postings: median (95%) | yes: rate (95%) | yes postings: median (95%) |\n|---|---|---|---|---|---|---|---|\n")
		for _, c := range d.Cells {
			wr, we, yr, ye := "unknown", "unknown", "unknown", "unknown"
			if c.Known && c.Judged > 0 {
				wr, yr = rateText(c.Wanted, c.Judged), rateText(c.Yes, c.Judged)
			} else if c.Known {
				wr, yr = "no unmarked postings", "no unmarked postings"
			}
			if c.Known {
				we, ye = estText(c.Wanted), estText(c.Yes)
			}
			cell := c.Cell
			if c.PriorDominated {
				cell += " (prior-dominated)"
			}
			fmt.Fprintf(&b, "| %s | %d | %d (%d) | %d | %s | %s | %s | %s |\n", cell, c.Pool, c.Marked, c.MarkedWanted, c.Judged, wr, we, yr, ye)
		}
		b.WriteString("\n| drop class | judged | wanted (judged + saved or applied) | one-sided 95% upper bound on its wanted rate | more not-wanted labels for a 5% bound |\n|---|---|---|---|---|\n")
		for _, c := range d.Cells {
			if !strings.HasPrefix(c.Cell, DroppedPrefix) {
				continue
			}
			need := fmt.Sprintf("%d", c.LabelsFor5pct)
			if c.LabelsFor5pct < 0 {
				need = fmt.Sprintf("more than %d", maxBoundLabels)
			}
			wanted := fmt.Sprintf("%d", c.Wanted.K)
			if c.KnownMisses > 0 {
				wanted = fmt.Sprintf("%d + %d", c.Wanted.K, c.KnownMisses)
			}
			fmt.Fprintf(&b, "| %s | %d | %s | %.1f%% | %s |\n", c.Cell, c.Judged, wanted, 100*c.UpperBound, need)
		}
		b.WriteString("\n")
	}
	if d.Status != RecallEstimated {
		fmt.Fprintf(&b, "Recall: insufficient: %s\n", d.Reason)
	} else {
		writeShare(&b, "wanted (yes or maybe)", d.Missed, d.ByClass)
		writeShare(&b, "yes only", d.MissedYes, d.ByClassYes)
	}
	if n := d.OutOfPool + d.Moved + d.NotAudit + d.Manual + d.MarkedOut; n > 0 {
		fmt.Fprintf(&b, "\nLabels left out: %d on postings no longer kept or dropped (closed, untriaged or gone), %d drawn from another stratum than the posting sits in now, %d not drawn from an audit stratum (Explore or another surface), %d with no sampling stratum, %d on marked postings (counted by the mark).\n", d.OutOfPool, d.Moved, d.NotAudit, d.Manual, d.MarkedOut)
	}
	if d.Renamed > 0 {
		fmt.Fprintf(&b, "\n%d label(s) were drawn from their cell under an older drop-class name and are counted in it.\n", d.Renamed)
	}
	return b.String()
}

// maxPriorDominatedJudged is the largest judged count priorDominated flags.
func maxPriorDominatedJudged() int {
	n := 0
	for n < maxBoundLabels && priorDominated(n+1) {
		n++
	}
	return n
}

func writeShare(b *strings.Builder, what string, s *RecallShare, by []RecallShare) {
	if s == nil {
		fmt.Fprintf(b, "\nShare of %s postings dropped: none possible (nothing counts as %s anywhere)\n", what, what)
		return
	}
	fmt.Fprintf(b, "\nShare of %s postings dropped: median %.1f%% (95%% interval %.1f-%.1f%%), observed %.1f%%; recall %.1f%% (%.1f-%.1f%%)\n",
		what, 100*s.Share, 100*s.Lo, 100*s.Hi, 100*s.Observed, 100*(1-s.Share), 100*(1-s.Hi), 100*(1-s.Lo))
	if len(by) > 0 {
		b.WriteString("\n| drop class | its share of them: median (95% interval) | observed |\n|---|---|---|\n")
		for _, c := range by {
			fmt.Fprintf(b, "| %s | %.1f%% (%.1f-%.1f%%) | %.1f%% |\n", c.Cell, 100*c.Share, 100*c.Lo, 100*c.Hi, 100*c.Observed)
		}
	}
}

func rateText(r RecallRate, n int) string {
	return fmt.Sprintf("%d/%d (%.0f-%.0f%%)", r.K, n, 100*r.Lo, 100*r.Hi)
}

func estText(r RecallRate) string {
	return fmt.Sprintf("%s (%s-%s)", count(r.Est), count(r.EstLo), count(r.EstHi))
}

func count(f float64) string {
	if math.IsNaN(f) {
		return "?"
	}
	return fmt.Sprintf("%.0f", f)
}
