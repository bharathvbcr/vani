package clusterstudy

import (
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
)

// A stratification is better for estimating a rate when its strata are more
// alike inside than the population is. For a yes/no target the variance inside
// a stratum s with n_s postings and pos_s positives is p_s(1-p_s) with
// p_s = pos_s/n_s, and the figure the audit cares about is the pooled mean of
// those, weighted by stratum size:
//
//	Plugin = (1/N) * sum_s n_s*p_s*(1-p_s)  = (1/N) * sum_s pos_s*neg_s/n_s
//
// It is the share of the target's variance a stratum does not explain: with
// the strata all one cell it is the total variance p(1-p), and it falls as the
// strata sort the positives. The plug-in form is biased low for small strata
// (a stratum of one has no variance), so Pooled also divides by the degrees of
// freedom N minus the non-empty strata (the pooled-variance estimator of an
// analysis of variance), which is the fairer figure when two stratifications
// have very different stratum sizes at the same cell count.

// Within is the within-stratum variance of a binary target over one
// stratification of N postings.
type Within struct {
	N     int `json:"n"`
	Cells int `json:"cells"` // non-empty strata
	// Positives is the postings with the target true.
	Positives int `json:"positives"`
	Total     Num `json:"total"`  // p(1-p) with no strata
	Plugin    Num `json:"plugin"` // size-weighted mean of p_s(1-p_s)
	// Pooled is the degrees-of-freedom corrected figure; NaN when N == Cells (no
	// stratum has two postings, so no within-stratum variance can be estimated).
	Pooled Num `json:"pooled"`
}

// Num is a float64 that JSON can carry when it is not a number: NaN and the
// infinities (a reduction of a variance that is zero, a variance of strata of
// one) encode as null instead of failing the whole reply, and null decodes
// back to NaN. An undefined figure stays visibly undefined; it is never written
// as 0.
type Num float64

// MarshalJSON writes null for a non-finite value.
func (n Num) MarshalJSON() ([]byte, error) {
	f := float64(n)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return []byte("null"), nil
	}
	return strconv.AppendFloat(nil, f, 'g', -1, 64), nil
}

// UnmarshalJSON reads null as NaN.
func (n *Num) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*n = Num(math.NaN())
		return nil
	}
	f, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return err
	}
	*n = Num(f)
	return nil
}

// ErrInput marks arguments that cannot be measured.
var ErrInput = errors.New("clusterstudy: cannot measure")

// compact renumbers cell ids to 0..c-1.
func compact(cell []int) (ids []int32, c int) {
	index := map[int]int32{}
	ids = make([]int32, len(cell))
	for i, v := range cell {
		id, ok := index[v]
		if !ok {
			id = int32(len(index))
			index[v] = id
		}
		ids[i] = id
	}
	return ids, len(index)
}

// withinOf measures the stratification ids (cell numbers below c) of the
// postings in rows, on target.
func withinOf(ids []int32, c int, target []bool, rows []int32) Within {
	tot := make([]int, c)
	pos := make([]int, c)
	for _, r := range rows {
		tot[ids[r]]++
		if target[r] {
			pos[ids[r]]++
		}
	}
	w := Within{N: len(rows)}
	var sum float64
	for s := range tot {
		if tot[s] == 0 {
			continue
		}
		w.Cells++
		w.Positives += pos[s]
		sum += float64(pos[s]) * float64(tot[s]-pos[s]) / float64(tot[s])
	}
	p := float64(w.Positives) / float64(w.N)
	w.Total = Num(p * (1 - p))
	w.Plugin = Num(sum / float64(w.N))
	w.Pooled = Num(math.NaN())
	if w.N > w.Cells {
		w.Pooled = Num(sum / float64(w.N-w.Cells))
	}
	return w
}

func checkMeasure(cell []int, target []bool) error {
	if len(cell) == 0 || len(cell) != len(target) {
		return ErrInput
	}
	return nil
}

func allRows(n int) []int32 {
	r := make([]int32, n)
	for i := range r {
		r[i] = int32(i)
	}
	return r
}

// WithinVariance measures the stratification cell (one id per posting) on
// target. Empty input or a length mismatch is an error, never a zero.
func WithinVariance(cell []int, target []bool) (Within, error) {
	if err := checkMeasure(cell, target); err != nil {
		return Within{}, err
	}
	ids, c := compact(cell)
	return withinOf(ids, c, target, allRows(len(cell))), nil
}

// Reduction is how much lower cand is than base: 1 - cand/base. A base with no
// variance has no reduction to speak of: NaN.
func Reduction(base, cand float64) float64 {
	if !(base > 0) || math.IsNaN(cand) {
		return math.NaN()
	}
	return 1 - cand/base
}

// Interval is a 95% percentile interval.
type Interval struct {
	Lo Num `json:"lo"`
	Hi Num `json:"hi"`
}

// MaxResamples bounds a bootstrap or permutation run.
const MaxResamples = 100_000

// percentile is the q-quantile (0..1) of the sorted finite values, by linear
// interpolation; NaN for none.
func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

func finiteSorted(v []float64) []float64 {
	out := make([]float64, 0, len(v))
	for _, x := range v {
		if !math.IsNaN(x) && !math.IsInf(x, 0) {
			out = append(out, x)
		}
	}
	slices.Sort(out)
	return out
}

// Compared is cand's plug-in variance reduction against base on one shared set
// of postings, with a bootstrap interval over the postings and a permutation
// null for the noise floor.
type Compared struct {
	Base Within `json:"base"`
	Cand Within `json:"cand"`
	// Reduction and PooledReduction are 1 - cand/base on the plug-in and the
	// degrees-of-freedom corrected figures.
	Reduction       Num `json:"reduction"`
	PooledReduction Num `json:"pooled_reduction"`
	// Bootstrap is the 95% interval of Reduction over resamples of the postings
	// (with replacement), each measured with the stratifications held fixed.
	Bootstrap Interval `json:"bootstrap"`
	// Null is what Reduction does when the target has nothing to do with either
	// stratification: the target is shuffled across postings (cell sizes and the
	// positive count kept). A reduction inside this band is noise.
	Null Interval `json:"null"`
	// NullAtOrAbove is the share of null reductions at or above Reduction. It
	// is NaN, never 0, when Reduction is NaN: no reduction is at or above an
	// undefined one. As a Num it is null in JSON, "n/a" in the text report.
	NullAtOrAbove Num `json:"null_at_or_above"`
	Resamples     int `json:"resamples"`
}

// Compare measures cand against base on target. Both stratifications and the
// target cover the same postings. reps resamples (1..MaxResamples) drive the
// bootstrap and the permutation null; seed makes them reproducible.
func Compare(base, cand []int, target []bool, reps int, seed uint64) (Compared, error) {
	if err := checkMeasure(base, target); err != nil {
		return Compared{}, err
	}
	if len(cand) != len(base) {
		return Compared{}, ErrInput
	}
	if reps < 1 || reps > MaxResamples {
		return Compared{}, ErrInput
	}
	bi, bc := compact(base)
	ci, cc := compact(cand)
	n := len(base)
	all := allRows(n)
	out := Compared{Resamples: reps}
	out.Base = withinOf(bi, bc, target, all)
	out.Cand = withinOf(ci, cc, target, all)
	out.Reduction = Num(Reduction(float64(out.Base.Plugin), float64(out.Cand.Plugin)))
	out.PooledReduction = Num(Reduction(float64(out.Base.Pooled), float64(out.Cand.Pooled)))

	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	boot := make([]float64, 0, reps)
	rows := make([]int32, n)
	for range reps {
		for i := range rows {
			rows[i] = int32(rng.IntN(n))
		}
		b := withinOf(bi, bc, target, rows)
		c := withinOf(ci, cc, target, rows)
		boot = append(boot, Reduction(float64(b.Plugin), float64(c.Plugin)))
	}
	bs := finiteSorted(boot)
	out.Bootstrap = Interval{Num(percentile(bs, 0.025)), Num(percentile(bs, 0.975))}

	shuffled := slices.Clone(target)
	null := make([]float64, 0, reps)
	atOrAbove := 0
	for range reps {
		rng.Shuffle(n, func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		b := withinOf(bi, bc, shuffled, all)
		c := withinOf(ci, cc, shuffled, all)
		r := Reduction(float64(b.Plugin), float64(c.Plugin))
		null = append(null, r)
		if !math.IsNaN(r) && r >= float64(out.Reduction) { // false when Reduction is NaN
			atOrAbove++
		}
	}
	ns := finiteSorted(null)
	out.Null = Interval{Num(percentile(ns, 0.025)), Num(percentile(ns, 0.975))}
	out.NullAtOrAbove = Num(float64(atOrAbove) / float64(reps))
	if math.IsNaN(float64(out.Reduction)) {
		out.NullAtOrAbove = Num(math.NaN())
	}
	return out, nil
}
