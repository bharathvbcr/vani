// Package semantic scores how well a job posting matches the owner's resume
// knowledge base with text embeddings, so synonyms and context match where a
// keyword list misses them ("k8s" against "Kubernetes").
//
// The embedding engine is behind the Embedder interface; Tessl's EmbeddingGemma
// 2 binding (embedgemma2.Model.EmbedTexts(ctx, texts, prompt, truncateDim))
// is adapted to it elsewhere. Kind mirrors that binding's prompt names.
//
// Data flow, and what each rule guards:
//
//   - A Profile holds chunks of the bank: role titles, skill terms and bullet
//     text only (profile.go). Org names, dates, locations and every contact
//     field never reach an Embedder.
//   - ChunkPosting cuts a posting into bounded chunks (text.go). Posting text
//     is untrusted: it is only embedded, never executed or followed.
//   - Matcher.Warm embeds and scores postings and caches the result; the
//     interactive Matcher.Lookup is cache-only and never calls the Embedder.
//   - No model, a failing model or a malformed answer yields an "unavailable" or
//     "failed" report, never a score of 0.
package semantic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
)

// Kind names the embedding prompt, with the strings Tessl's EmbedTexts takes.
type Kind string

const (
	// KindQuery embeds text as a search query. Posting chunks use it.
	KindQuery Kind = "query"
	// KindDocument embeds text as a document. Profile chunks use it.
	KindDocument Kind = "document"
)

// Embedder turns texts into embedding rows. Implementations return one row per
// text, each of the same width; rows are expected to be L2-normalized but the
// matcher re-normalizes and rejects NaN, infinite and zero rows. Model is the
// identity used in cache keys: it must change when the model or its output
// width changes.
type Embedder interface {
	Embed(ctx context.Context, texts []string, kind Kind) ([][]float32, error)
	Model() string
}

// Errors. All are matchable with errors.Is.
var (
	// ErrUnavailable is what an Embedder returns when no model can run. The
	// matcher reports it as Unavailable and never scores around it.
	ErrUnavailable = errors.New("semantic: embedding model unavailable")
	// ErrEmpty means the profile has nothing embeddable.
	ErrEmpty = errors.New("semantic: profile has no embeddable content")
	// ErrCache wraps every cache-file problem.
	ErrCache = errors.New("semantic: cache")
	// ErrEmbed means an Embedder answered with something unusable (wrong row
	// count or width, NaN, a zero vector, a panic).
	ErrEmbed = errors.New("semantic: embedder returned an unusable result")
	// ErrCalibration means the floor and ceiling anchors are too close for the
	// mapping to mean anything.
	ErrCalibration = errors.New("semantic: calibration anchors are degenerate")
)

// Bounds. All are ceilings, not targets.
const (
	// Version is folded into Profile.Hash. Bump it when chunking, the anchors
	// or the scoring change, so cached scores are recomputed.
	Version = 1

	// MaxBatch is the most texts one Embed call takes.
	MaxBatch = 32
	// MaxProfileChunks and MaxProfileChunkBytes bound a Profile.
	MaxProfileChunks     = 512
	MaxProfileChunkBytes = 1024
	// MaxPostingChunks and MaxPostingChunkBytes bound ChunkPosting.
	MaxPostingChunks     = 24
	MaxPostingChunkBytes = 512
	// MaxDescriptionBytes is how much of a description is read.
	MaxDescriptionBytes = 256 << 10
	// MaxWarmDocs caps the postings one Warm call scores.
	MaxWarmDocs = 5000
	// DefaultMaxEntries caps cached postings; the oldest are evicted first.
	DefaultMaxEntries = 20000
	// MaxCacheBytes caps a cache file, on read and on write.
	MaxCacheBytes = 64 << 20

	topChunks    = 8   // m: chunk maxima averaged into the raw score
	evidenceN    = 3   // evidence items kept
	snippetRunes = 120 // posting snippet length in evidence
	titleWeight  = 2.0
	minSpread    = 0.02 // smallest ceiling-floor gap calibration accepts
	maxConsecBad = 3    // consecutive failed Embed calls before Warm stops
)

// Doc is a posting to score. Key is the caller's identity for it (posting.Key).
type Doc struct {
	Key         string
	Title       string
	Description string
}

// Evidence is one reason for a score: a posting chunk and the profile chunk it
// matched best.
type Evidence struct {
	Snippet string  `json:"snippet"` // posting chunk, at most 120 runes
	Label   string  `json:"label"`   // profile chunk label: a role title or "skills"
	Cosine  float64 `json:"cosine"`
}

// Match is the cached verdict for one posting.
type Match struct {
	Score    float64 // calibrated, 0 to 1
	Raw      float64 // aggregate cosine before calibration
	Evidence []Evidence
	Model    string
}

// Report says what a Warm call did. Scored + Cached + Failed + Skipped covers
// the distinct postings it was given.
type Report struct {
	Scored      int    // postings embedded and scored now
	Cached      int    // postings that already had a current score
	Failed      int    // postings that could not be scored
	Skipped     int    // postings not attempted: over max, or the run stopped first
	Unavailable bool   // the embedding model was unavailable
	Canceled    bool   // the context ended before the run did
	Err         string // first problem, empty when none
}

// TextHash identifies a posting's scored text.
func TextHash(title, description string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d:%s\x00%d:%s", len(title), title, len(description), description)
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// dot is the cosine of two vectors of equal width that validRow normalized.
func dot(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// validRow returns a unit-length copy of v, or false when v has the wrong
// width (dim 0 accepts any width above 0), a NaN or infinite value, or no
// length.
func validRow(v []float32, dim int) ([]float32, bool) {
	if len(v) == 0 || (dim > 0 && len(v) != dim) {
		return nil, false
	}
	var sq float64
	for _, x := range v {
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, false
		}
		sq += f * f
	}
	n := math.Sqrt(sq)
	if n == 0 || math.IsInf(n, 0) || math.IsNaN(n) {
		return nil, false
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / n)
	}
	return out, true
}

// callEmbed calls e.Embed once and checks the row count. The call runs in its
// own goroutine so an Embedder that ignores ctx cannot hold the caller past
// ctx; its late answer is dropped.
func callEmbed(ctx context.Context, e Embedder, texts []string, kind Kind) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type result struct {
		rows [][]float32
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- result{err: fmt.Errorf("%w: embedder panicked: %v", ErrEmbed, r)}
			}
		}()
		rows, err := e.Embed(ctx, texts, kind)
		ch <- result{rows: rows, err: err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		if len(r.rows) != len(texts) {
			return nil, fmt.Errorf("%w: %d rows for %d texts", ErrEmbed, len(r.rows), len(texts))
		}
		return r.rows, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// embedStrict embeds texts in batches of MaxBatch and returns normalized rows.
// Any unusable row fails the whole call: it is used for the profile and the
// anchors, where a missing vector would skew every later score. dim 0 takes
// the width of the first row.
func embedStrict(ctx context.Context, e Embedder, texts []string, kind Kind, dim int) ([][]float32, int, error) {
	out := make([][]float32, 0, len(texts))
	for i := 0; i < len(texts); i += MaxBatch {
		j := min(i+MaxBatch, len(texts))
		rows, err := callEmbed(ctx, e, texts[i:j], kind)
		if err != nil {
			return nil, 0, err
		}
		for k, r := range rows {
			v, ok := validRow(r, dim)
			if !ok {
				return nil, 0, fmt.Errorf("%w: row %d is the wrong width, non-finite or zero", ErrEmbed, i+k)
			}
			if dim == 0 {
				dim = len(v)
			}
			out = append(out, v)
		}
	}
	return out, dim, nil
}
