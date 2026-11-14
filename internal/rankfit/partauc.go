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
// whose part was known evidence; an unknown part takes the prior. AUC is over
// every judged posting, prior included, so it mixes "how the value orders"
// with "whether the part was known at all": when one class is known more
// often, AUC can move for that reason alone. KnownAUC is over the postings
// whose part was known in both classes, and only it gives the verdict;
// KnownOK is false when either class has fewer than MinPartAUCClass known.
type PartAUCRow struct {
	Part     string  `json:"part"`
	KnownPos int     `json:"known_pos"`
	KnownNeg int     `json:"known_neg"`
	AUC      float64 `json:"auc"`
	Lo       float64 `json:"lo"`
	Hi       float64 `json:"hi"`
	OK       bool    `json:"ok"`
	KnownAUC float64 `json:"known_auc"`
	KnownLo  float64 `json:"known_lo"`
	KnownHi  float64 `json:"known_hi"`
	KnownOK  bool    `json:"known_ok"`
	// KnownP is AUCPValue on the known-only AUC, set with KnownOK (0 without
	// it); KnownPExact is false when it is the normal approximation.
	KnownP      float64 `json:"known_p"`
	KnownPExact bool    `json:"known_p_exact"`
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
		var kp, kn []float64
		row := PartAUCRow{Part: c.name}
		for i, it := range pos {
			pv[i] = c.value(it)
			if c.known(it) {
				kp = append(kp, pv[i])
			}
		}
		for i, it := range neg {
			nv[i] = c.value(it)
			if c.known(it) {
				kn = append(kn, nv[i])
			}
		}
		row.KnownPos, row.KnownNeg = len(kp), len(kn)
		row.AUC = auc(pv, nv)
		// Each column draws from its own streams so adding a part never moves
		// another part's interval.
		row.Lo, row.Hi = bootstrapAUC(pv, nv, resamples, rand.New(rand.NewPCG(seed, 2*uint64(ci)+1)))
		row.OK = !math.IsNaN(row.AUC)
		if len(kp) >= MinPartAUCClass && len(kn) >= MinPartAUCClass {
			row.KnownAUC = auc(kp, kn)
			row.KnownLo, row.KnownHi = bootstrapAUC(kp, kn, resamples, rand.New(rand.NewPCG(seed, 2*uint64(ci)+2)))
			row.KnownOK = !math.IsNaN(row.KnownAUC)
			if row.KnownOK {
				row.KnownP, row.KnownPExact = AUCPValue(kp, kn)
			}
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}

// AUCWithInterval is the Mann-Whitney AUC of pos over neg with its 95%
// stratified bootstrap interval, the statistic BuildPartAUC reports; ok is
// false below MinPartAUCClass in either class or above MaxPartAUCItems.
// resamples is clamped as in BuildPartAUC.
func AUCWithInterval(pos, neg []float64, resamples int, seed uint64) (a, lo, hi float64, ok bool) {
	switch {
	case resamples <= 0:
		resamples = DefaultPartAUCResamples
	case resamples > MaxPartAUCResamples:
		resamples = MaxPartAUCResamples
	}
	if len(pos) < MinPartAUCClass || len(neg) < MinPartAUCClass || len(pos) > MaxPartAUCItems || len(neg) > MaxPartAUCItems {
		return 0, 0, 0, false
	}
	a = auc(pos, neg)
	lo, hi = bootstrapAUC(pos, neg, resamples, rand.New(rand.NewPCG(seed, 1)))
	return a, lo, hi, !math.IsNaN(a)
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

// MaxExactPItems bounds AUCPValue's exact count: above this many pooled
// values it takes the normal approximation, tie-corrected, and says so.
const MaxExactPItems = 100

// AUCPValue is the one-sided chance that labels carrying no information give
// pos an AUC over neg at least as high as this one: of every way to choose
// which len(pos) of the pooled values are the wanted ones, the share whose
// rank sum is at least pos's. Ties take midranks (doubled, so the count is in
// integers), so a tied part is not credited for its ties. A bootstrap interval
// on three positives is not to be trusted; this is exact and deterministic. It
// is NaN when a class is empty; exact is false when the pool is over
// MaxExactPItems and the normal approximation was used.
func AUCPValue(pos, neg []float64) (p float64, exact bool) {
	n1, n0 := len(pos), len(neg)
	if n1 == 0 || n0 == 0 {
		return math.NaN(), true
	}
	type v struct {
		x   float64
		pos bool
	}
	all := make([]v, 0, n1+n0)
	for _, x := range pos {
		all = append(all, v{x, true})
	}
	for _, x := range neg {
		all = append(all, v{x, false})
	}
	slices.SortFunc(all, func(a, b v) int { return cmpFloat(a.x, b.x) })
	n := len(all)
	r2 := make([]int, n) // doubled midranks
	tieTerm := 0.0
	for i := 0; i < n; {
		j := i
		for j+1 < n && all[j+1].x == all[i].x {
			j++
		}
		for k := i; k <= j; k++ {
			r2[k] = (i + 1) + (j + 1)
		}
		t := float64(j - i + 1)
		tieTerm += t*t*t - t
		i = j + 1
	}
	obs, total := 0, 0
	for i, a := range all {
		total += r2[i]
		if a.pos {
			obs += r2[i]
		}
	}
	if n > MaxExactPItems {
		// U from the rank sum, tie-corrected variance, continuity correction.
		u := float64(obs)/2 - float64(n1*(n1+1))/2
		mean := float64(n1*n0) / 2
		sd := math.Sqrt(float64(n1*n0) / 12 * (float64(n+1) - tieTerm/float64(n*(n-1))))
		if sd == 0 {
			return 1, false
		}
		z := (u - mean - 0.5) / sd
		return 0.5 * math.Erfc(z/math.Sqrt2), false
	}
	// Count over the smaller class: pos's sum >= obs is the other class's sum
	// <= total-obs.
	k, want, atLeast := n1, obs, true
	if n0 < n1 {
		k, want, atLeast = n0, total-obs, false
	}
	maxS := 2 * n * k
	ways := make([][]float64, k+1)
	for j := range ways {
		ways[j] = make([]float64, maxS+1)
	}
	ways[0][0] = 1
	for i := range n {
		for j := min(i+1, k); j >= 1; j-- {
			from, to := ways[j-1], ways[j]
			for s := maxS - r2[i]; s >= 0; s-- {
				if from[s] != 0 {
					to[s+r2[i]] += from[s]
				}
			}
		}
	}
	var hit, all2 float64
	for s, w := range ways[k] {
		all2 += w
		if (atLeast && s >= want) || (!atLeast && s <= want) {
			hit += w
		}
	}
	return hit / all2, true
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
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
	fmt.Fprintf(&b, "%d wanted, %d unwanted. AUC: the chance a wanted posting's value is above an unwanted one's (ties half); 0.5 is no better than chance. 95%% intervals from %d stratified resamples (seed %d). \"known\" counts postings where the part was evidence rather than the prior. The all-postings AUC mixes how the value orders with whether the part was known at all, so only the known-only AUC gives a reading, and only with at least %d known in each class.\n\n", r.Pos, r.Neg, r.Resamples, r.Seed, MinPartAUCClass)
	b.WriteString("| part | AUC, all (95%) | known (wanted / unwanted) | AUC, known only (95%) | p, known only | reading |\n|---|---|---|---|---|---|\n")
	tested := 0
	for _, x := range r.Rows {
		all, known, pv := "no data", "too few known", "-"
		if x.OK {
			all = fmt.Sprintf("%.2f (%.2f-%.2f)", x.AUC, x.Lo, x.Hi)
		}
		if x.KnownOK {
			tested++
			pv = fmt.Sprintf("%.3f", x.KnownP)
			if !x.KnownPExact {
				pv += " (normal approx.)"
			}
		}
		reading := "too few known to read"
		if x.KnownOK {
			known = fmt.Sprintf("%.2f (%.2f-%.2f)", x.KnownAUC, x.KnownLo, x.KnownHi)
			switch {
			case x.KnownLo > 0.5:
				reading = "orders the owner's taste"
			case x.KnownHi < 0.5:
				reading = "reversed: orders against the owner"
			default:
				reading = "unclear"
			}
		}
		fmt.Fprintf(&b, "| %s | %s | %d / %d | %s | %s | %s |\n", x.Part, all, x.KnownPos, x.KnownNeg, known, pv, reading)
	}
	if tested > 0 {
		fmt.Fprintf(&b, "\np: the exact one-sided chance that labels carrying no information score the part at least this well (rank sums over every way to choose the wanted postings, ties as midranks). %d rows tested here, so about %.1f would fall under 0.05 by chance alone.\n", tested, 0.05*float64(tested))
	}
	return b.String()
}
