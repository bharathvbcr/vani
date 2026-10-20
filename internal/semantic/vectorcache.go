package semantic

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// VectorCacheVersion is the vector-cache file format. Any other version is an
// ErrCache error, as in cache.go.
const VectorCacheVersion = 1

const (
	vectorsFileName = "semantic-vectors.json"
	// DefaultVectorEntries caps cached vectors; the oldest are evicted first.
	DefaultVectorEntries = 10000
	// maxVectorDim bounds one cached vector's width.
	maxVectorDim = 8192
)

// VectorCache maps a text to its embedding, keyed by model id, kind and the
// sha256 of the text, so a changed model or prompt never reads another's
// vectors. It is the one place a label's or description's vector is kept: the
// similar-wording pass and the near-duplicate tiebreaker share it.
//
// It lives in one file under dir (the stateDir/semantic folder). Put changes
// memory only; Save writes the file atomically, evicting the oldest vectors past
// the entry cap and past MaxCacheBytes. A file that cannot be read, is too big,
// has another version or holds a malformed vector is an ErrCache error at open:
// nothing is guessed from it. Two processes saving at once leave the last
// writer's file; a lost vector is only recomputed.
type VectorCache struct {
	mu      sync.Mutex
	path    string
	max     int
	nextSeq uint64
	entries map[string]*vecEntry
	dirty   bool
}

type vecEntry struct {
	vec []float32
	seq uint64 // write order; the smallest is evicted first
}

type vectorFile struct {
	Version int                `json:"version"`
	NextSeq uint64             `json:"next_seq"`
	Entries map[string]vecWire `json:"entries"`
}

// vecWire is one stored vector: base64 little-endian float32.
type vecWire struct {
	V string `json:"v"`
	S uint64 `json:"s"`
}

// OpenVectorCache opens (or starts) the vector cache under dir. maxEntries <= 0
// means DefaultVectorEntries.
func OpenVectorCache(dir string, maxEntries int) (*VectorCache, error) {
	if dir == "" {
		return nil, fmt.Errorf("%w: no directory", ErrCache)
	}
	if maxEntries <= 0 {
		maxEntries = DefaultVectorEntries
	}
	c := &VectorCache{path: filepath.Join(dir, vectorsFileName), max: maxEntries, entries: map[string]*vecEntry{}}
	var vf vectorFile
	missing, err := readCache(c.path, &vf)
	if err != nil {
		return nil, err
	}
	if missing {
		return c, nil
	}
	if vf.Version != VectorCacheVersion {
		return nil, fmt.Errorf("%w: %s has version %d, want %d", ErrCache, c.path, vf.Version, VectorCacheVersion)
	}
	for k, w := range vf.Entries {
		if !validVectorKey(k) {
			return nil, fmt.Errorf("%w: %s holds a malformed key", ErrCache, c.path)
		}
		v, err := decodeAnyVec(w.V)
		if err != nil {
			return nil, fmt.Errorf("%w: %s vector %q: %v", ErrCache, c.path, k, err)
		}
		c.entries[k] = &vecEntry{vec: v, seq: w.S}
		if w.S >= c.nextSeq {
			c.nextSeq = w.S + 1
		}
	}
	if vf.NextSeq > c.nextSeq {
		c.nextSeq = vf.NextSeq
	}
	return c, nil
}

// vectorKey is model|kind|sha256(text). The model id and kind come from the
// embedder and this package, never from the text, so '|' cannot be forged by one.
func vectorKey(model string, kind Kind, text string) (string, bool) {
	if model == "" || strings.ContainsRune(model, '|') || kind == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(text))
	return model + "|" + string(kind) + "|" + hex.EncodeToString(sum[:]), true
}

func validVectorKey(k string) bool {
	parts := strings.Split(k, "|")
	return len(parts) == 3 && parts[0] != "" && parts[1] != "" && len(parts[2]) == 2*sha256.Size
}

func decodeAnyVec(s string) ([]float32, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || len(b)%4 != 0 || len(b)/4 > maxVectorDim {
		return nil, fmt.Errorf("vector has %d bytes", len(b))
	}
	return decodeVec(s, len(b)/4)
}

// Get returns a copy of the cached unit vector for text, if there is one.
func (c *VectorCache) Get(model string, kind Kind, text string) ([]float32, bool) {
	k, ok := vectorKey(model, kind, text)
	if !ok {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[k]
	if e == nil {
		return nil, false
	}
	return append([]float32(nil), e.vec...), true
}

// Put stores v (normalized) for text in memory; Save writes it. A vector that
// is the wrong shape, non-finite or zero is refused, never stored.
func (c *VectorCache) Put(model string, kind Kind, text string, v []float32) error {
	k, ok := vectorKey(model, kind, text)
	if !ok {
		return fmt.Errorf("%w: no model identity to key a vector by", ErrCache)
	}
	u, ok := validRow(v, 0)
	if !ok || len(u) > maxVectorDim {
		return fmt.Errorf("%w: refusing to cache an unusable vector", ErrEmbed)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.entries[k]; old != nil && equalVec(old.vec, u) {
		return nil
	}
	c.entries[k] = &vecEntry{vec: u, seq: c.nextSeq}
	c.nextSeq++
	c.dirty = true
	return nil
}

func equalVec(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Len is the number of cached vectors.
func (c *VectorCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Save evicts the oldest vectors past the entry cap and writes the file
// atomically, evicting more if the encoding would pass MaxCacheBytes. It writes
// nothing when nothing changed since the last Save.
func (c *VectorCache) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return nil
	}
	c.evictLocked(c.max)
	for {
		vf := vectorFile{Version: VectorCacheVersion, NextSeq: c.nextSeq, Entries: make(map[string]vecWire, len(c.entries))}
		for k, e := range c.entries {
			vf.Entries[k] = vecWire{V: encodeVec(e.vec), S: e.seq}
		}
		raw, err := json.Marshal(vf)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrCache, err)
		}
		if len(raw) <= MaxCacheBytes {
			if err := atomicfile.Write(c.path, raw); err != nil {
				return fmt.Errorf("%w: %v", ErrCache, err)
			}
			c.dirty = false
			return nil
		}
		if len(c.entries) == 0 {
			return fmt.Errorf("%w: vector cache cannot fit in %d bytes", ErrCache, MaxCacheBytes)
		}
		c.evictLocked(len(c.entries) / 2)
	}
}

// evictLocked drops the oldest entries until at most max remain.
func (c *VectorCache) evictLocked(max int) {
	if len(c.entries) <= max {
		return
	}
	keys := make([]string, 0, len(c.entries))
	for k := range c.entries {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := c.entries[keys[i]], c.entries[keys[j]]
		if a.seq != b.seq {
			return a.seq < b.seq
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys[:len(keys)-max] {
		delete(c.entries, k)
	}
	c.dirty = true
}

// ResetVectorCache removes the cache file under dir so OpenVectorCache starts
// empty. A cache holds nothing its model cannot recompute, so a caller that got
// ErrCache from OpenVectorCache may reset and reopen, and say it did (the way
// the score cache is rebuilt).
func ResetVectorCache(dir string) error {
	if err := os.Remove(filepath.Join(dir, vectorsFileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %v", ErrCache, err)
	}
	return nil
}
