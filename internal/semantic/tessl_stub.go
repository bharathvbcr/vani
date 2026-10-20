//go:build !(tessl && darwin && arm64)

package semantic

import (
	"context"
	"fmt"
)

// TesslBuilt reports whether this binary carries the Tessl embedder.
const TesslBuilt = false

// Tessl is the Embedder over tessl's EmbeddingGemma 2 binding. This build
// does not carry it (build with -tags tessl on darwin/arm64).
type Tessl struct{}

// OpenTessl is always ErrUnavailable in this build, so semantic matching is
// reported as not run rather than scored around.
func OpenTessl(ctx context.Context, c TesslConfig) (*Tessl, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%w: this vani was built without -tags tessl", ErrUnavailable)
}

// Embed is never reached: OpenTessl returns no Tessl in this build.
func (*Tessl) Embed(context.Context, []string, Kind) ([][]float32, error) {
	return nil, ErrUnavailable
}

// Model is empty in this build.
func (*Tessl) Model() string { return "" }

// Close does nothing in this build.
func (*Tessl) Close() error { return nil }
