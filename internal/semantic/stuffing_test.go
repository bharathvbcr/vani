package semantic

import (
	"fmt"
	"math"
	"testing"
)

// tilt is a unit vector at cosine c to the first axis, tilted toward axis k.
func tilt(c float64, k int) []float32 {
	v := make([]float32, 8)
	v[0], v[k] = float32(c), float32(math.Sqrt(1-c*c))
	return v
}

// A strong line reworded twenty times must not fill the top-m mean: the
// posting scores as if it said the line once. The vectors are built by hand
// so the variants are near-identical to each other (cosine above 0.99) and
// strongly matched, while the title and fillers match weakly.
func TestRewordedStuffingCountsOnce(t *testing.T) {
	pd := &profileData{vecs: [][]float32{tilt(1, 1)}, dim: 8}
	m := &Matcher{p: Profile{Chunks: []Chunk{{Label: "role", Text: "x"}}}}
	build := func(variants int) ([]Chunk, [][]float32) {
		chunks := []Chunk{{Label: "title", Text: "title", Weight: titleWeight}, {Label: "line", Text: "filler a", Weight: 1}, {Label: "line", Text: "filler b", Weight: 1}}
		vecs := [][]float32{tilt(0.1, 2), tilt(0.2, 3), tilt(0.2, 4)}
		for i := range variants {
			chunks = append(chunks, Chunk{Label: "line", Text: fmt.Sprintf("stuffed %d", i), Weight: 1})
			v := tilt(0.9, 5)
			v[6] = float32(0.01 * float64(i%3)) // a reworded variant, cosine > 0.99 to the others
			vecs = append(vecs, v)
		}
		for i := range vecs {
			vecs[i], _ = validRow(vecs[i], 8)
		}
		return chunks, vecs
	}
	cs, vs := build(20)
	co, vo := build(1)
	stuffed, _, nS := m.rawScoreN(cs, vs, pd, topChunks)
	once, _, nO := m.rawScoreN(co, vo, pd, topChunks)
	t.Logf("stuffed raw %.4f over %d chunks, once raw %.4f over %d", stuffed, nS, once, nO)
	if math.Abs(stuffed-once) > 1e-9 {
		t.Fatalf("reworded stuffing moved the raw score: stuffed %.4f vs once %.4f", stuffed, once)
	}
	if nS < minDistinctChunks || nO < minDistinctChunks || nS != nO {
		t.Fatalf("four distinct chunks counted as %d and %d (thin under %d)", nS, nO, minDistinctChunks)
	}
}

// A posting with too little text to split (a title alone) is thin, and a
// fuller posting is not.
func TestThinMatch(t *testing.T) {
	m, _ := newMatcher(t, NewFakeEmbedder(256, synonyms))
	title := Doc{Key: "title-only", Title: "Kubernetes Platform Engineer"}
	full := k8sDoc()
	full.Description += "\n- Mentor engineers and review designs for reliability"
	mustWarm(t, m, title, full)
	if !lookup(t, m, title).Thin {
		t.Fatalf("a title-only posting is not thin")
	}
	if lookup(t, m, full).Thin {
		t.Fatalf("a posting with a title and three requirement lines is thin")
	}
}
