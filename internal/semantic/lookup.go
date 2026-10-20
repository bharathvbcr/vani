package semantic

import (
	"context"
	"fmt"
	"path/filepath"
)

// lookupEmbedder stands in for a model on the cache-only path. It never embeds.
type lookupEmbedder struct{ model string }

func (l lookupEmbedder) Model() string { return l.model }

func (lookupEmbedder) Embed(context.Context, []string, Kind) ([][]float32, error) {
	return nil, fmt.Errorf("%w: this matcher was opened for lookup only", ErrUnavailable)
}

// OpenLookup opens the score cache in cacheDir for reading only, loading no
// model. It accepts the model the score file records for this profile's hash,
// so Lookup answers exactly what the last Warm stored; a cache written for a
// different profile, or none at all, is simply empty (Lookup finds nothing,
// which is not a score of 0). A malformed or oversized cache file is an
// ErrCache error. Warm on the result scores only what is already cached and
// returns ErrUnavailable for anything else.
func OpenLookup(p Profile, cacheDir string) (*Matcher, error) {
	if len(p.Chunks) == 0 {
		return nil, fmt.Errorf("%w: use ProfileFromBank", ErrEmpty)
	}
	if cacheDir == "" {
		return nil, fmt.Errorf("%w: no cache directory", ErrCache)
	}
	hash := p.Hash()
	sf, err := loadScores(filepath.Join(cacheDir, scoresFileName), hash, "")
	if err != nil {
		return nil, err
	}
	return &Matcher{e: lookupEmbedder{model: sf.Model}, p: p, model: sf.Model, hash: hash, dir: cacheDir,
		sf: sf, maxEntries: DefaultMaxEntries}, nil
}

// Model is the embedding model the cached scores came from; "" when the
// matcher holds none.
func (m *Matcher) Model() string { return m.model }
