package semantic

import (
	"context"
	"errors"
	"fmt"
	"math"
)

// MaxDupTextBytes is how much of a description NewSimilarity embeds. The first
// kilobyte carries the role; the rest is benefits boilerplate.
const MaxDupTextBytes = 1024

// NewSimilarity adapts e to the near-duplicate stage's optional second opinion
// (pipeline.NearDupOptions.Similarity): the cosine of two postings' texts under
// KindSimilarity, each cut to MaxDupTextBytes at a rune boundary. Vectors are
// read from and added to c (nil for none); the caller saves c once after the
// stage, because a save per pair would rewrite the whole file per pair.
//
// ok is false, with no error, when either text is blank: there is no reading.
// A failure is returned as the error and the stage counts it; this never turns
// one into a cosine of 0. The stage's own deadline bounds every call through
// ctx, so this is only practical cache-first with the model already loaded.
//
// The stage hands over descriptions only, so the posting's title is not part of
// the embedded text.
func NewSimilarity(e Embedder, c *VectorCache) func(ctx context.Context, a, b string) (float64, bool, error) {
	return func(ctx context.Context, a, b string) (float64, bool, error) {
		if e == nil {
			return 0, false, fmt.Errorf("%w: no embedder", ErrUnavailable)
		}
		a, _ = clipBytes(a, MaxDupTextBytes)
		b, _ = clipBytes(b, MaxDupTextBytes)
		if isBlank(a) || isBlank(b) {
			return 0, false, nil
		}
		texts := []string{a, b}
		if a == b {
			texts = texts[:1]
		}
		var rep Report
		rows, err := embedDistinct(ctx, e, c, texts, &rep)
		if err != nil {
			return 0, false, err
		}
		cos := 1.0
		if len(rows) == 2 {
			cos = math.Max(-1, math.Min(1, dot(rows[0], rows[1])))
		}
		if math.IsNaN(cos) {
			return 0, false, errors.New("semantic: similarity is not a number")
		}
		return cos, true, nil
	}
}
