//go:build tessl && darwin && arm64

package semantic

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/bharathvbcr/tessl/go/embedgemma2"
)

// TesslBuilt reports whether this binary carries the Tessl embedder.
const TesslBuilt = true

// Tessl is the Embedder over tessl's EmbeddingGemma 2 binding, running
// in-process on the Metal device. It is safe for concurrent use.
type Tessl struct {
	m    *embedgemma2.Model
	id   string
	call time.Duration
	sts  bool // the snapshot has the STS prompt KindSimilarity needs
}

// OpenTessl loads the snapshot in c onto the Metal device, text encoder only.
// A snapshot that is missing, unreadable, lacks the query and document
// prompts, or an engine archive that does not match the binding, is
// ErrUnavailable: the matcher then reports semantic matching as not run.
func OpenTessl(ctx context.Context, c TesslConfig) (*Tessl, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	lctx, cancel := context.WithTimeout(ctx, c.loadTimeout())
	defer cancel()
	m, err := embedgemma2.LoadContext(lctx, c.Snapshot, embedgemma2.Options{TextOnly: true})
	if err != nil {
		return nil, fmt.Errorf("%w: tessl load: %v", ErrUnavailable, err)
	}
	p, err := m.Prompts(lctx)
	if err != nil {
		m.Close()
		return nil, fmt.Errorf("%w: tessl prompts: %v", ErrUnavailable, err)
	}
	for _, k := range []Kind{KindQuery, KindDocument} {
		if !slices.Contains(p.Names, string(k)) {
			m.Close()
			return nil, fmt.Errorf("%w: tessl snapshot has no %q prompt", ErrUnavailable, k)
		}
	}
	return &Tessl{
		m:    m,
		id:   fmt.Sprintf("tessl/embeddinggemma-2@%s/f32/d%d", filepath.Base(filepath.Clean(c.Snapshot)), m.EmbeddingDim()),
		call: c.callTimeout(),
		sts:  slices.Contains(p.Names, string(KindSimilarity)),
	}, nil
}

// Embed embeds texts with the snapshot's prompt named kind, bounded by the
// per-call timeout.
func (t *Tessl) Embed(ctx context.Context, texts []string, kind Kind) ([][]float32, error) {
	switch kind {
	case KindQuery, KindDocument:
	case KindSimilarity:
		if !t.sts {
			return nil, fmt.Errorf("%w: tessl snapshot has no %q prompt", ErrUnavailable, kind)
		}
	default:
		return nil, fmt.Errorf("%w: unknown kind %q", ErrEmbed, kind)
	}
	if len(texts) == 0 {
		return nil, nil
	}
	cctx, cancel := context.WithTimeout(ctx, t.call)
	defer cancel()
	rows, err := t.m.EmbedTexts(cctx, texts, string(kind), 0)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: tessl: %v", ErrEmbed, err)
	}
	return rows, nil
}

// Model is the cache identity: the snapshot revision, precision and width.
func (t *Tessl) Model() string { return t.id }

// Close releases the model. Further calls fail.
func (t *Tessl) Close() error { return t.m.Close() }
