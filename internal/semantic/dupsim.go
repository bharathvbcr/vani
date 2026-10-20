package semantic

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
)

// The near-duplicate stage's second opinion. pipeline.NearDupOptions.Similarity
// is func(ctx, a, b string) (cos float64, ok bool, err error) and is handed the
// two postings' descriptions only. NewSimilarity adapts an Embedder to it as it
// stands; NewTitledSimilarity also embeds each posting's title, found through a
// TitleIndex the caller fills from the same records the stage reads. See
// docs/SIMILARITY.md for how to construct either in cmd/vani/wire.go.

const (
	// MaxDupTextBytes is how much of a description is embedded. The first
	// kilobyte carries the role; the rest is benefits boilerplate.
	MaxDupTextBytes = 1024
	// MaxDupTitleBytes is how much of a title is embedded.
	MaxDupTitleBytes = 256
	// MaxTitleIndex bounds a TitleIndex: past it Add keeps what it has and
	// reports false, and the later postings are embedded without a title.
	MaxTitleIndex = 100000
)

// TitleIndex maps a description to the title of the posting that carries it, so
// an adapter handed only descriptions can still embed titles. The key is the
// description cut to MaxDupTextBytes, the part that is embedded. A key that
// two postings reach with different titles is ambiguous and yields no title,
// for either of them: an absent title is a weaker reading, never a wrong one.
// It is safe for concurrent use.
type TitleIndex struct {
	mu sync.Mutex
	t  map[string]string
	// amb holds keys seen with two titles.
	amb map[string]bool
}

// NewTitleIndex returns an empty index.
func NewTitleIndex() *TitleIndex {
	return &TitleIndex{t: map[string]string{}, amb: map[string]bool{}}
}

// Add records title for description. It reports false when the index is full
// or the title or description is blank (nothing recorded).
func (x *TitleIndex) Add(title, description string) bool {
	key, _ := clipBytes(description, MaxDupTextBytes)
	title, _ = clipBytes(title, MaxDupTitleBytes)
	if isBlank(key) || isBlank(title) {
		return false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if old, ok := x.t[key]; ok {
		if old != title {
			x.amb[key] = true
		}
		return true
	}
	if len(x.t) >= MaxTitleIndex {
		return false
	}
	x.t[key] = title
	return true
}

// Title is the title recorded for description, or "" when there is none or it
// is ambiguous.
func (x *TitleIndex) Title(description string) string {
	if x == nil {
		return ""
	}
	key, _ := clipBytes(description, MaxDupTextBytes)
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.amb[key] {
		return ""
	}
	return x.t[key]
}

// DupText is what is embedded for a posting: its title, a line break, then the
// first MaxDupTextBytes of its description, each cut at a rune boundary. With
// no title it is the description's first kilobyte alone.
func DupText(title, description string) string {
	d, _ := clipBytes(description, MaxDupTextBytes)
	t, _ := clipBytes(title, MaxDupTitleBytes)
	if isBlank(t) {
		return d
	}
	return t + "\n" + d
}

// NewSimilarity adapts e to the stage's Similarity option with descriptions
// alone. See NewTitledSimilarity.
func NewSimilarity(e Embedder, c *VectorCache) func(ctx context.Context, a, b string) (float64, bool, error) {
	return NewTitledSimilarity(e, c, nil)
}

// NewTitledSimilarity adapts e to the stage's Similarity option: the cosine of
// two postings' DupText under KindSimilarity, the title taken from titles (nil
// for none). Vectors are read from and added to c (nil for none); the caller
// saves c once after the stage, because a save per pair would rewrite the whole
// file per pair.
//
// ok is false, with no error, when either description is blank: there is no
// reading. A failure is returned as the error and the stage counts it
// (NearDupReport.SimilarityErrors); this never turns one into a cosine of 0.
// The stage gives its similarity a budget of its own (a share of the stage's
// deadline) and bounds every call by it through ctx; a pair the budget does not
// reach gets no reading (NearDupReport.SimilaritySkipped). This is only
// practical with the model already loaded and the vectors mostly cached.
func NewTitledSimilarity(e Embedder, c *VectorCache, titles *TitleIndex) func(ctx context.Context, a, b string) (float64, bool, error) {
	return func(ctx context.Context, a, b string) (float64, bool, error) {
		if e == nil {
			return 0, false, fmt.Errorf("%w: no embedder", ErrUnavailable)
		}
		if isBlank(a) || isBlank(b) {
			return 0, false, nil
		}
		ta, tb := DupText(titles.Title(a), a), DupText(titles.Title(b), b)
		texts := []string{ta, tb}
		if ta == tb {
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
