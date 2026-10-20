package semantic

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// fakeStop are words too common to carry meaning; a real model discounts them
// and the fake drops them so unrelated texts do not match on "the" and "and".
var fakeStop = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "of": true, "to": true,
	"in": true, "on": true, "for": true, "with": true, "at": true, "by": true, "as": true,
	"is": true, "are": true, "be": true, "you": true, "we": true, "our": true, "your": true,
	"will": true, "from": true, "that": true, "this": true, "it": true, "all": true, "per": true,
}

type fakeEmbedder struct {
	dim   int
	canon map[string]string
	name  string
}

// NewFakeEmbedder returns a deterministic, offline Embedder for tests: a
// hashed, signed bag of lower-cased tokens with stop words dropped and each
// synonym group collapsed onto its first member, L2-normalized. Texts that
// share tokens (or synonyms) land close; texts that share none are near
// orthogonal. It ignores Kind. Its Model changes with dim and synonyms. dim is
// raised to at least 16.
func NewFakeEmbedder(dim int, synonyms [][]string) Embedder {
	dim = max(dim, 16)
	canon := map[string]string{}
	h := fnv.New32a()
	for _, g := range synonyms {
		for i, w := range g {
			w = strings.ToLower(w)
			fmt.Fprintf(h, "%s\x00", w)
			if i > 0 {
				canon[w] = strings.ToLower(g[0])
			}
		}
		fmt.Fprint(h, "\x01")
	}
	return &fakeEmbedder{dim: dim, canon: canon, name: fmt.Sprintf("fake-bow/dim=%d/syn=%08x", dim, h.Sum32())}
}

func (f *fakeEmbedder) Model() string { return f.name }

func (f *fakeEmbedder) Embed(ctx context.Context, texts []string, _ Kind) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = f.vec(t)
	}
	return out, nil
}

func (f *fakeEmbedder) vec(text string) []float32 {
	v := make([]float64, f.dim)
	any := false
	for _, tok := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if fakeStop[tok] {
			continue
		}
		if c, ok := f.canon[tok]; ok {
			tok = c
		}
		h := fnv.New64a()
		h.Write([]byte(tok))
		s := h.Sum64()
		sign := 1.0
		if (s>>40)&1 == 1 {
			sign = -1
		}
		v[s%uint64(f.dim)] += sign
		any = true
	}
	if !any {
		v[0] = 1 // a text with no tokens still gets a valid unit vector
	}
	var sq float64
	for _, x := range v {
		sq += x * x
	}
	if sq == 0 { // tokens that cancelled in one bucket
		v[0], sq = 1, 1
	}
	n := math.Sqrt(sq)
	out := make([]float32, f.dim)
	for i, x := range v {
		out[i] = float32(x / n)
	}
	return out
}
