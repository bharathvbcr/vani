package semantic

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
	"unicode/utf8"
)

// Similar-wording bounds. All are ceilings, not targets.
const (
	// MaxSimilarTexts is the most texts one SimilarPairs call takes. The pair
	// pass is quadratic: 4000 texts is 8 million comparisons.
	MaxSimilarTexts = 4000
	// MaxSimilarTextBytes is the longest text SimilarPairs takes. Question
	// wordings are short; ClipSimilarText cuts a longer one for the caller.
	MaxSimilarTextBytes = 512
	// MaxSimilarPairs is the most pairs one call returns. Past it the call fails
	// (ErrBounds) rather than hand back a capped sample as the whole answer.
	MaxSimilarPairs = 100000
	// SimilarDeadline bounds one call when its context has no sooner deadline.
	SimilarDeadline = 2 * time.Minute
)

// Cutoffs for SimilarPairs' two tiers, measured with TestRealLabelCalibration
// on google/embeddinggemma-2 (KindSimilarity, d768) over the 106 labelled form
// labels in extension/test/vectors/label-pairs.json (52 same question, 54
// different, 46 of them hard negatives). Short labels sit on a high floor:
// every pair in the fixture scores at least 0.8, and wordings that differ by one
// word ("First name"/"Last name") score above many true rewordings.
//
//   - LikelyCos is 0.95. A cosine alone cannot hold the line: "First name" ~ "Last name"
//     scores 0.9646 and now-versus-future sponsorship 0.9598, so "likely" is the
//     cosine at or above LikelyCos AND no lexical veto (LexicalVeto: a one-word
//     substitution or a contrast word such as first/last). Of the fixture's 54
//     different-question pairs 36 are vetoed; the highest one left scores 0.9204
//     ("Years of experience" ~ "Years of experience with Python", a superset the
//     veto does not see), so LikelyCos sits 0.03 above it. It keeps 15 of the 52
//     same-question pairs (29%) with no false merge; 16 true pairs are vetoed
//     down to "possible" (cost of the veto). The veto's words were read off the
//     same fixture, so nothing was held out: treat "likely" as "no counter-example
//     yet", not a proof. Without the veto the cutoff would have to be 0.97 (9 pairs).
//   - PossibleCos keeps 50 of 52 same-question pairs (96%), the knee of the
//     curve: 0.87 adds one pair for two more false merges. At it 30 of the 54
//     different-question pairs are also flagged (precision 62%), so it is a list
//     for a person to review, never a merge.
//
// Use TierPair, not the bare cosine band.
const (
	LikelyCos   = 0.95
	PossibleCos = 0.88
)

// cosineTier is a cosine's band: "likely", "possible", or "" below PossibleCos.
// It does not know the labels; TierPair adds the lexical veto.
func cosineTier(cos float64) string {
	switch {
	case cos >= LikelyCos:
		return "likely"
	case cos >= PossibleCos:
		return "possible"
	}
	return ""
}

// ErrBounds means the input to SimilarPairs is over a documented bound.
var ErrBounds = errors.New("semantic: input over bounds")

// Pair is two texts, by index into the input, whose embeddings are this close.
// A < B.
type Pair struct {
	A, B int
	Cos  float64
}

// ClipSimilarText cuts s to at most MaxSimilarTextBytes at a rune boundary, and
// says whether it cut. Hosts that cap by characters (the Mac app cuts at 300
// UTF-16 units, up to 900 bytes) use it so one long wording does not fail a
// whole call; the cut text is what is embedded.
func ClipSimilarText(s string) (string, bool) { return clipBytes(s, MaxSimilarTextBytes) }

func clipBytes(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// SimilarPairs embeds texts with KindSimilarity and returns every pair (a < b)
// whose cosine is at least minCos, most similar first. Blank texts are skipped
// and pair with nothing; identical texts pair at 1.
//
// c, when not nil, is read first and every miss is embedded in batches of at
// most MaxBatch, then stored and saved: a call that runs out of time still
// leaves its finished batches cached, so the next one resumes. Report counts
// distinct texts: Scored were embedded now, Cached came from c, Skipped were
// blank.
//
// It never turns a check that did not run into "no pairs". Any embedding
// failure (the embedder unavailable, a deadline, a wrong row count, a NaN, a
// wrong width) returns no pairs, the error, and a Report with Failed set to the
// texts left without a vector, Unavailable or Canceled where those are the
// cause. A cache that cannot be saved does not change the pairs: it is named in
// Report.Err with a nil error.
//
// The bounds are MaxSimilarTexts texts of at most MaxSimilarTextBytes each, at
// most MaxSimilarPairs pairs, and a context deadline of at most SimilarDeadline
// (ErrBounds on the first three).
func SimilarPairs(ctx context.Context, e Embedder, c *VectorCache, texts []string, minCos float64) ([]Pair, Report, error) {
	var rep Report
	switch {
	case e == nil:
		return nil, rep, fmt.Errorf("%w: no embedder", ErrUnavailable)
	case len(texts) > MaxSimilarTexts:
		return nil, rep, fmt.Errorf("%w: %d texts, at most %d", ErrBounds, len(texts), MaxSimilarTexts)
	case math.IsNaN(minCos) || minCos < -1 || minCos > 1:
		return nil, rep, fmt.Errorf("%w: min_cos %v is outside -1..1", ErrBounds, minCos)
	}
	for i, t := range texts {
		if len(t) > MaxSimilarTextBytes {
			return nil, rep, fmt.Errorf("%w: text %d is %d bytes, at most %d", ErrBounds, i, len(t), MaxSimilarTextBytes)
		}
	}
	if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > SimilarDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, SimilarDeadline)
		defer cancel()
	}

	// Distinct non-blank texts, in first-seen order; slot[i] is text i's index
	// in distinct, or -1 for a blank text.
	slot := make([]int, len(texts))
	var distinct []string
	at := map[string]int{}
	blank := map[string]bool{}
	for i, t := range texts {
		if isBlank(t) {
			slot[i] = -1
			if !blank[t] {
				blank[t] = true
				rep.Skipped++
			}
			continue
		}
		j, ok := at[t]
		if !ok {
			j = len(distinct)
			at[t] = j
			distinct = append(distinct, t)
		}
		slot[i] = j
	}

	rows, err := embedDistinct(ctx, e, c, distinct, &rep)
	if c != nil {
		if serr := c.Save(); serr != nil && rep.Err == "" {
			rep.Err = serr.Error()
		}
	}
	if err != nil {
		return nil, rep, err
	}

	pairs := []Pair{}
	for i := 0; i < len(texts); i++ {
		if slot[i] < 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			rep.Canceled, rep.Err = true, err.Error()
			return nil, rep, err
		}
		vi := rows[slot[i]]
		for j := i + 1; j < len(texts); j++ {
			if slot[j] < 0 {
				continue
			}
			cos := 1.0 // the same text is the same vector, whatever float32 rounding says
			if slot[i] != slot[j] {
				cos = math.Max(-1, math.Min(1, dot(vi, rows[slot[j]])))
			}
			if cos < minCos {
				continue
			}
			if len(pairs) >= MaxSimilarPairs {
				return nil, rep, fmt.Errorf("%w: more than %d pairs at min_cos %.3f; raise it", ErrBounds, MaxSimilarPairs, minCos)
			}
			pairs = append(pairs, Pair{A: i, B: j, Cos: cos})
		}
	}
	sort.Slice(pairs, func(x, y int) bool {
		p, q := pairs[x], pairs[y]
		switch {
		case p.Cos != q.Cos:
			return p.Cos > q.Cos
		case p.A != q.A:
			return p.A < q.A
		}
		return p.B < q.B
	})
	return pairs, rep, nil
}

func isBlank(s string) bool {
	for _, r := range s {
		if r > ' ' && r != 0x7f && r != 0xa0 {
			return false
		}
	}
	return true
}

// embedDistinct returns one unit row per text (distinct, none blank), reading c
// first and embedding the misses with KindSimilarity in batches of MaxBatch. It
// stores what it embeds in c (memory; the caller saves). A failure returns the
// error with rep.Failed the texts still without a vector; vectors from batches
// that finished stay in c.
func embedDistinct(ctx context.Context, e Embedder, c *VectorCache, texts []string, rep *Report) ([][]float32, error) {
	model := e.Model()
	if model == "" && len(texts) > 0 {
		rep.Failed, rep.Err = len(texts), "embedder has no model identity"
		return nil, fmt.Errorf("%w: embedder has no model identity", ErrEmbed)
	}
	rows := make([][]float32, len(texts))
	var miss []int
	for i, t := range texts {
		if c != nil {
			if v, ok := c.Get(model, KindSimilarity, t); ok {
				rows[i] = v
				rep.Cached++
				continue
			}
		}
		miss = append(miss, i)
	}
	fail := func(left int, err error) ([][]float32, error) {
		rep.Failed = left
		rep.Unavailable = errors.Is(err, ErrUnavailable)
		rep.Canceled = errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
		rep.Err = err.Error()
		return nil, err
	}
	// One width for every row, cached or fresh: a mixed set cannot be compared,
	// and a fresh row of another width must not reach the cache.
	dim := 0
	for _, r := range rows {
		if r == nil {
			continue
		}
		if dim == 0 {
			dim = len(r)
		}
		if len(r) != dim {
			return fail(len(miss), fmt.Errorf("%w: cached rows of width %d and %d under one model id", ErrEmbed, dim, len(r)))
		}
	}
	for lo := 0; lo < len(miss); lo += MaxBatch {
		idx := miss[lo:min(lo+MaxBatch, len(miss))]
		batch := make([]string, len(idx))
		for k, i := range idx {
			batch[k] = texts[i]
		}
		got, err := callEmbed(ctx, e, batch, KindSimilarity)
		if err != nil {
			return fail(len(miss)-lo, err)
		}
		for k, i := range idx {
			v, ok := validRow(got[k], dim)
			if !ok {
				return fail(len(miss)-lo, fmt.Errorf("%w: row %d is the wrong width, non-finite or zero", ErrEmbed, i))
			}
			if dim == 0 {
				dim = len(v)
			}
			rows[i] = v
		}
		if c != nil {
			for k, i := range idx {
				if err := c.Put(model, KindSimilarity, texts[i], rows[i]); err != nil {
					return fail(len(miss)-lo-k, err)
				}
			}
		}
		rep.Scored += len(idx)
	}
	return rows, nil
}
