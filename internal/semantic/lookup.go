package semantic

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
	return OpenLookupFor(p, cacheDir, "")
}

// ErrModelMismatch means the cached scores were written by a model other than
// the one required: they say nothing about the model now configured.
var ErrModelMismatch = errors.New("semantic: cached scores are from another model")

// OpenLookupFor is OpenLookup that requires the cached scores' model id to
// start with modelPrefix (TesslConfig.ModelPrefix), so scores a previous
// snapshot wrote are not shown as the current model's. A cache holding no
// scores matches any prefix (there is nothing to mislead); an empty prefix
// accepts any model. A mismatch is an ErrModelMismatch error carrying the
// recorded model id.
func OpenLookupFor(p Profile, cacheDir, modelPrefix string) (*Matcher, error) {
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
	if modelPrefix != "" && sf.Model != "" && len(sf.Entries) > 0 && !strings.HasPrefix(sf.Model, modelPrefix) {
		return nil, fmt.Errorf("%w: %q, want %q...", ErrModelMismatch, sf.Model, modelPrefix)
	}
	return &Matcher{e: lookupEmbedder{model: sf.Model}, p: p, model: sf.Model, hash: hash, dir: cacheDir,
		sf: sf, maxEntries: DefaultMaxEntries}, nil
}

// StaleScores counts the cached scores written for a profile other than p.
// OpenLookup ignores them (the next warm replaces them), so without this a
// stale cache reads the same as one never warmed. 0 when the file is missing
// or is p's; a malformed file is an ErrCache error.
func StaleScores(p Profile, cacheDir string) (int, error) {
	var sf scoreFile
	missing, err := readCache(filepath.Join(cacheDir, scoresFileName), &sf)
	if err != nil || missing || sf.ProfileHash == p.Hash() {
		return 0, err
	}
	return len(sf.Entries), nil
}

// Model is the embedding model the cached scores came from; "" when the
// matcher holds none.
func (m *Matcher) Model() string { return m.model }
