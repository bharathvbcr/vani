package rankfit

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// The part AUC asks, of each of Score's parts on its own, how well it orders
// the owner's wanted postings (would_apply yes or maybe) above the unwanted
// ones: the Mann-Whitney AUC, ties counted half, over audit judgments only
// (marks and impressions are not the owner's answer to the question). It is
// unweighted, like `vani relevance`: a description of the judged postings,
// with a stratified bootstrap interval, not an estimate for the pool. A part
// whose interval sits under 0.5 orders the owner's taste backwards.

const (
	// MinPartAUCClass is the fewest wanted and the fewest unwanted postings an
	// AUC is computed from; below it the report says "insufficient".
	MinPartAUCClass = 3
	// MaxPartAUCItems bounds each class: the AUC is quadratic in them, and a
	// bigger set is refused, never sampled.
	MaxPartAUCItems = 5000
	// DefaultPartAUCResamples and MaxPartAUCResamples bound the bootstrap.
	DefaultPartAUCResamples = 1000
	MaxPartAUCResamples     = 10000

	PartAUCOK           = "ok"
	PartAUCInsufficient = "insufficient"
	PartAUCTooMany      = "too_many"

	// PartMatchGate is the row of Score's own order under the hand weights:
	// match x gate (freshness and the company factor left out, as in Fit).
	PartMatchGate = "match x gate"
)

// PartAUCRow is one part's AUC. Known counts the wanted and unwanted postings
// whose part was known evidence; an unknown part takes the prior, so a part
// mostly unknown reads near 0.5 for that reason alone.
type PartAUCRow struct {
	Part     string  `json:"part"`
	KnownPos int     `json:"known_pos"`
	KnownNeg int     `json:"known_neg"`
	AUC      float64 `json:"auc"`
	Lo       float64 `json:"lo"`
	Hi       float64 `json:"hi"`
	OK       bool    `json:"ok"`
}

// PartAUC is the report for one scope of judged postings.
type PartAUC struct {
	Scope     string       `json:"scope"`
	Status    string       `json:"status"`
	Pos       int          `json:"pos"` // wanted: yes or maybe
	Neg       int          `json:"neg"` // unwanted: no
	Resamples int          `json:"resamples"`
	Seed      uint64       `json:"seed"`
	Rows      []PartAUCRow `json:"rows"`
}

// BuildPartAUC computes each part's AUC over the judged items keep admits (nil
// admits every judged item), and Score's own match x gate under base's hand
// shares. resamples <= 0 is the default; it is clamped to MaxPartAUCResamples.
func BuildPartAUC(items []Item, base jobinsight.Weights, scope string, keep func(*Item) bool, resamples int, seed uint64) PartAUC {
	switch {
	case resamples <= 0:
		resamples = DefaultPartAUCResamples
	case resamples > MaxPartAUCResamples:
		resamples = MaxPartAUCResamples
	}
	out := PartAUC{Scope: scope, Resamples: resamples, Seed: seed, Rows: []PartAUCRow{}}
	var pos, neg []*Item
	for i := range items {
		it := &items[i]
		if !it.Judged || (keep != nil && !keep(it)) {
			continue
		}
		if it.Grade >= GradeMaybe {
			pos = append(pos, it)
		} else {
			neg = append(neg, it)
		}
	}
	out.Pos, out.Neg = len(pos), len(neg)
	switch {
	case out.Pos > MaxPartAUCItems || out.Neg > MaxPartAUCItems:
		out.Status = PartAUCTooMany
		return out
	case out.Pos < MinPartAUCClass || out.Neg < MinPartAUCClass:
		out.Status = PartAUCInsufficient
		return out
	}
	out.Status = PartAUCOK
	b, hand := blendOf(base), SharesOf(base).Array()
	score := func(it *Item) float64 { return math.Exp(b.logMatch(it, &hand, nil)) * it.Gate }
	type column struct {
		name  string
		value func(*Item) float64
		known func(*Item) bool
	}
	cols := make([]column, 0, NumParts+1)
	for p := range NumParts {
		cols = append(cols, column{PartNames[p], func(it *Item) float64 { return it.Value[p] }, func(it *Item) bool { return it.Known[p] }})
	}
	cols = append(cols, column{PartMatchGate, score, func(*Item) bool { return true }})
	for ci, c := range cols {
		pv, nv := make([]float64, len(pos)), make([]float64, len(neg))
		row := PartAUCRow{Part: c.name}
		for i, it := range pos {
			pv[i] = c.value(it)
			if c.known(it) {
				row.KnownPos++
			}
		}
		for i, it := range neg {
			nv[i] = c.value(it)
			if c.known(it) {
				row.KnownNeg++
			}
		}
		row.AUC = auc(pv, nv)
		// Each column draws from its own stream so adding a part never moves
		// another part's interval.
		row.Lo, row.Hi = bootstrapAUC(pv, nv, resamples, rand.New(rand.NewPCG(seed, uint64(ci)+1)))
		row.OK = !math.IsNaN(row.AUC)
		out.Rows = append(out.Rows, row)
	}
	return out
}

// auc is the Mann-Whitney AUC of pos over neg, ties counted half.
func auc(pos, neg []float64) float64 {
	if len(pos) == 0 || len(neg) == 0 {
		return math.NaN()
	}
	var s float64
	for _, p := range pos {
		for _, n := range neg {
			switch {
			case p > n:
				s++
			case p == n:
				s += 0.5
			}
		}
	}
	return s / float64(len(pos)*len(neg))
}

// bootstrapAUC is the 95% percentile interval of the AUC over resamples drawn
// with replacement within each class (the class sizes are the design's).
func bootstrapAUC(pos, neg []float64, resamples int, r *rand.Rand) (lo, hi float64) {
	stats := make([]float64, resamples)
	ps, ns := make([]float64, len(pos)), make([]float64, len(neg))
	for i := range stats {
		for j := range ps {
			ps[j] = pos[r.IntN(len(pos))]
		}
		for j := range ns {
			ns[j] = neg[r.IntN(len(neg))]
		}
		stats[i] = auc(ps, ns)
	}
	slices.Sort(stats)
	at := func(q float64) float64 { return stats[min(int(q*float64(resamples)), resamples-1)] }
	return at(0.025), at(0.975)
}

// Markdown renders the report as one table, or why there is none.
func (r PartAUC) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n### Score parts against the owner: %s\n\n", r.Scope)
	switch r.Status {
	case PartAUCInsufficient:
		fmt.Fprintf(&b, "insufficient: %d wanted and %d unwanted judged postings; an AUC needs at least %d of each\n", r.Pos, r.Neg, MinPartAUCClass)
		return b.String()
	case PartAUCTooMany:
		fmt.Fprintf(&b, "not computed: %d wanted and %d unwanted judged postings, over the bound of %d per class\n", r.Pos, r.Neg, MaxPartAUCItems)
		return b.String()
	}
	fmt.Fprintf(&b, "%d wanted, %d unwanted. AUC: the chance a wanted posting's value is above an unwanted one's (ties half); 0.5 is no better than chance. 95%% interval from %d stratified resamples (seed %d). \"known\" counts postings where the part was evidence rather than the prior.\n\n", r.Pos, r.Neg, r.Resamples, r.Seed)
	b.WriteString("| part | AUC | 95% interval | known (wanted / unwanted) | reading |\n|---|---|---|---|---|\n")
	for _, x := range r.Rows {
		reading := "unclear"
		switch {
		case !x.OK:
			reading = "no data"
		case x.Lo > 0.5:
			reading = "orders the owner's taste"
		case x.Hi < 0.5:
			reading = "reversed: orders against the owner"
		}
		fmt.Fprintf(&b, "| %s | %.2f | %.2f-%.2f | %d / %d | %s |\n", x.Part, x.AUC, x.Lo, x.Hi, x.KnownPos, x.KnownNeg, reading)
	}
	return b.String()
}
