package lca

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// SchemaVersion is the on-disk index schema.
const SchemaVersion = 1

// maxIndexFileBytes bounds what Load will read.
const maxIndexFileBytes = 1 << 30

const (
	minFY = 1990
	maxFY = 2100
)

// Index holds merged LCA counts. Merge, Load and Lookup are safe for
// concurrent use.
type Index struct {
	mu      sync.RWMutex
	sources map[string]Partial // keyed by source sha256

	// derived by rebuild
	merged   map[string]map[int]Counts
	keys     []string // sorted employer keys
	coverage []int
}

// NewIndex returns an empty index.
func NewIndex() *Index {
	idx := &Index{sources: map[string]Partial{}}
	idx.rebuild()
	return idx
}

// Merge adds a Partial. A Partial whose source sha256 is already present
// replaces the earlier one, so re-importing a file never double-counts. The
// index takes ownership of p's maps.
func (x *Index) Merge(p Partial) error {
	if p.Source.SHA256 == "" {
		return errors.New("lca: partial has no source sha256")
	}
	if err := validateEmployers(p.Employers); err != nil {
		return err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.sources == nil {
		x.sources = map[string]Partial{}
	}
	x.sources[p.Source.SHA256] = p
	x.rebuild()
	return nil
}

// Sources returns metadata of the merged files, ordered by sha256.
func (x *Index) Sources() []SourceMeta {
	x.mu.RLock()
	defer x.mu.RUnlock()
	out := make([]SourceMeta, 0, len(x.sources))
	for _, s := range sortedSources(x.sources) {
		out = append(out, s.Source)
	}
	return out
}

// Coverage returns the fiscal years that have any counts, ascending.
func (x *Index) Coverage() []int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return append([]int(nil), x.coverage...)
}

// Employers reports the number of distinct normalised employers.
func (x *Index) Employers() int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return len(x.keys)
}

// rebuild recomputes the merged view. Caller holds the write lock.
func (x *Index) rebuild() {
	merged := map[string]map[int]Counts{}
	fys := map[int]struct{}{}
	for _, s := range x.sources {
		for name, byFY := range s.Employers {
			dst := merged[name]
			if dst == nil {
				dst = map[int]Counts{}
				merged[name] = dst
			}
			for fy, c := range byFY {
				d := dst[fy]
				d.H1B += c.H1B
				d.H1BWithdrawn += c.H1BWithdrawn
				d.H1B1 += c.H1B1
				d.E3 += c.E3
				dst[fy] = d
				fys[fy] = struct{}{}
			}
		}
	}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	x.merged, x.keys, x.coverage = merged, keys, sortedKeys(fys)
}

func sortedSources(m map[string]Partial) []Partial {
	out := make([]Partial, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source.SHA256 < out[j].Source.SHA256 })
	return out
}

func validateEmployers(emps map[string]map[int]Counts) error {
	for name, byFY := range emps {
		if name == "" {
			return errors.New("lca: empty employer key")
		}
		for fy, c := range byFY {
			if fy < minFY || fy > maxFY {
				return fmt.Errorf("lca: employer %q has fiscal year %d outside %d-%d", name, fy, minFY, maxFY)
			}
			if c.H1B < 0 || c.H1BWithdrawn < 0 || c.H1B1 < 0 || c.E3 < 0 {
				return fmt.Errorf("lca: employer %q FY%d has a negative count", name, fy)
			}
		}
	}
	return nil
}

// MarshalJSON encodes Counts as a compact [h1b, h1bWithdrawn, h1b1, e3] array.
func (c Counts) MarshalJSON() ([]byte, error) {
	return json.Marshal([4]int{c.H1B, c.H1BWithdrawn, c.H1B1, c.E3})
}

// UnmarshalJSON decodes the array form written by MarshalJSON.
func (c *Counts) UnmarshalJSON(b []byte) error {
	var s []int
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("lca: counts: %w", err)
	}
	if len(s) != 4 {
		return fmt.Errorf("lca: counts: want 4 values, got %d", len(s))
	}
	a := s
	*c = Counts{H1B: a[0], H1BWithdrawn: a[1], H1B1: a[2], E3: a[3]}
	return nil
}

type fileSource struct {
	SourceMeta
	Employers map[string]map[int]Counts `json:"employers"`
}

type fileDoc struct {
	Version  int          `json:"version"`
	Checksum string       `json:"checksum"` // sha256 of the JSON encoding of Sources
	Sources  []fileSource `json:"sources"`
}

func encodeSources(sources []fileSource) ([]byte, string, error) {
	b, err := json.Marshal(sources)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:]), nil
}

// Save writes the index as compact JSON (schema version 1) to path with mode
// 0600, atomically: a temp file in the same directory is written, synced and
// renamed over path, so a crash leaves either the old or the new file.
func (x *Index) Save(path string) error {
	x.mu.RLock()
	srcs := make([]fileSource, 0, len(x.sources))
	for _, p := range sortedSources(x.sources) {
		emps := p.Employers
		if emps == nil {
			emps = map[string]map[int]Counts{}
		}
		srcs = append(srcs, fileSource{SourceMeta: p.Source, Employers: emps})
	}
	_, sum, err := encodeSources(srcs)
	if err != nil {
		x.mu.RUnlock()
		return fmt.Errorf("lca: encode index: %w", err)
	}
	data, err := json.Marshal(fileDoc{Version: SchemaVersion, Checksum: sum, Sources: srcs})
	x.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("lca: encode index: %w", err)
	}
	return atomicfile.Write(path, data)
}

// Load reads an index written by Save. A corrupted, truncated, unknown-version
// or internally inconsistent file is refused; nothing partial is returned.
func Load(path string) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("lca: open index: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxIndexFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("lca: read index: %w", err)
	}
	if len(data) > maxIndexFileBytes {
		return nil, fmt.Errorf("lca: index file exceeds %d bytes", maxIndexFileBytes)
	}
	var doc fileDoc
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("lca: corrupt index: %w", err)
	}
	if dec.More() {
		return nil, errors.New("lca: corrupt index: trailing data")
	}
	if doc.Version != SchemaVersion {
		return nil, fmt.Errorf("lca: unsupported index schema version %d (want %d)", doc.Version, SchemaVersion)
	}
	if _, sum, err := encodeSources(doc.Sources); err != nil || sum != doc.Checksum {
		return nil, errors.New("lca: corrupt index: checksum mismatch")
	}
	idx := &Index{sources: map[string]Partial{}}
	for _, s := range doc.Sources {
		if s.SHA256 == "" {
			return nil, errors.New("lca: corrupt index: source without sha256")
		}
		if _, dup := idx.sources[s.SHA256]; dup {
			return nil, fmt.Errorf("lca: corrupt index: duplicate source %s", s.SHA256)
		}
		if err := validateEmployers(s.Employers); err != nil {
			return nil, fmt.Errorf("lca: corrupt index: %w", err)
		}
		idx.sources[s.SHA256] = Partial{Source: s.SourceMeta, Employers: s.Employers}
	}
	idx.rebuild()
	return idx, nil
}
