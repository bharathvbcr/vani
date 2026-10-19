package employer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"sync"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/lca"
)

// SchemaVersion is the on-disk index schema.
const SchemaVersion = 1

// maxIndexFileBytes bounds what Load will read.
const maxIndexFileBytes = 1 << 30

// Index holds employer attributes per source file, which Merge and Save
// need, plus the merged View that Lookup reads. A run that only looks names
// up loads the View alone with LoadView. The zero value is an empty index.
// Merge, Load and Lookup are safe for concurrent use.
type Index struct {
	mu      sync.RWMutex
	sources map[string]Partial // keyed by source sha256
	view    *View              // derived from sources; nil until Merge or Load sets it, read as empty
}

// emptyView is the view of an index with no sources.
var emptyView, _ = buildView(nil) // an empty view always fits

// NewIndex returns an empty index.
func NewIndex() *Index {
	return &Index{sources: map[string]Partial{}, view: emptyView}
}

// currentView returns the merged view. Caller holds the lock.
func (x *Index) currentView() *View {
	if x.view == nil {
		return emptyView
	}
	return x.view
}

// Merge adds a Partial. A Partial whose source sha256 is already present
// replaces the earlier one, so re-importing a file changes nothing. The index
// takes ownership of p's map. A failed Merge leaves the index unchanged.
func (x *Index) Merge(p Partial) error {
	if err := validatePartial(p); err != nil {
		return err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	next := make(map[string]Partial, len(x.sources)+1)
	for k, s := range x.sources {
		next[k] = s
	}
	next[p.Source.SHA256] = p
	v, err := buildView(next)
	if err != nil {
		return err
	}
	x.sources, x.view = next, v
	return nil
}

func validatePartial(p Partial) error {
	if p.Source.SHA256 == "" {
		return errors.New("employer: partial has no source sha256")
	}
	if p.Source.Kind != KindIRSBMF && p.Source.Kind != KindIPEDS {
		return fmt.Errorf("employer: unknown source kind %q", p.Source.Kind)
	}
	for name, a := range p.Names {
		if name == "" || lca.Normalize(name) != name {
			return fmt.Errorf("employer: name %q is not a normalised key", name)
		}
		if a == 0 || a&^bitsAll != 0 {
			return fmt.Errorf("employer: name %q has attribute bits %#x", name, uint8(a))
		}
	}
	return nil
}

// Sources returns metadata of the merged files, ordered by sha256.
func (x *Index) Sources() []SourceMeta {
	x.mu.RLock()
	defer x.mu.RUnlock()
	out := make([]SourceMeta, 0, len(x.sources))
	for _, p := range sortedSources(x.sources) {
		out = append(out, p.Source)
	}
	return out
}

// Names reports the number of distinct normalised names.
func (x *Index) Names() int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.currentView().Len()
}

func sortedSources(m map[string]Partial) []Partial {
	out := make([]Partial, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source.SHA256 < out[j].Source.SHA256 })
	return out
}

type fileSource struct {
	SourceMeta
	Names map[string]attr `json:"names"`
}

// fileDoc is the index file. Sources is kept as the bytes json.Marshal gave
// []fileSource: the checksum is of exactly those bytes, so a load hashes what
// it read instead of re-encoding the whole index to compare.
type fileDoc struct {
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"` // sha256 of the JSON encoding of Sources
	Sources  json.RawMessage `json:"sources"`
}

func sumOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Save writes the index as compact JSON with mode 0600, atomically, then the
// lookup view derived from it to ViewPath(path), bound to the index bytes.
func (x *Index) Save(path string) error {
	x.mu.RLock()
	view := x.currentView()
	srcs := make([]fileSource, 0, len(x.sources))
	for _, p := range sortedSources(x.sources) {
		names := p.Names
		if names == nil {
			names = map[string]attr{}
		}
		srcs = append(srcs, fileSource{SourceMeta: p.Source, Names: names})
	}
	b, err := json.Marshal(srcs)
	if err != nil {
		x.mu.RUnlock()
		return fmt.Errorf("employer: encode index: %w", err)
	}
	data, err := json.Marshal(fileDoc{Version: SchemaVersion, Checksum: sumOf(b), Sources: b})
	x.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("employer: encode index: %w", err)
	}
	if err := atomicfile.Write(path, data); err != nil {
		return err
	}
	// The index is written first: a crash before the view lands leaves a view
	// bound to the old bytes, which LoadView detects and rebuilds.
	fileSum := sha256.Sum256(data)
	if err := writeView(ViewPath(path), view, hex.EncodeToString(fileSum[:]), int64(len(data))); err != nil {
		return fmt.Errorf("employer: index saved but its lookup view was not: %w", err)
	}
	return nil
}

// Load reads the full index written by Save, per-source names included.
// Import uses Update, and a run that only looks names up uses LoadView. A
// corrupted, truncated, unknown-version or internally inconsistent file is
// refused; nothing partial is returned.
func Load(path string) (*Index, error) {
	data, err := readIndexFile(path)
	if err != nil {
		return nil, err
	}
	return decodeIndex(data)
}

func readIndexFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("employer: open index: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxIndexFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("employer: read index: %w", err)
	}
	if len(data) > maxIndexFileBytes {
		return nil, fmt.Errorf("employer: index file exceeds %d bytes", maxIndexFileBytes)
	}
	return data, nil
}

// Update is the import path: it merges p into the index at path (an absent
// file is an empty index) and saves the index and its view. The merged view
// is built once, for the result; the index as loaded never gets one. A refused
// partial or a corrupt index leaves both files as they were.
func Update(path string, p Partial) (*Index, error) {
	sources := map[string]Partial{}
	data, err := readIndexFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if sources, err = decodeSources(data); err != nil {
			return nil, err
		}
	}
	x := &Index{sources: sources} // its view is set by Merge
	if err := x.Merge(p); err != nil {
		return nil, err
	}
	if err := x.Save(path); err != nil {
		return nil, err
	}
	return x, nil
}

func decodeIndex(data []byte) (*Index, error) {
	sources, err := decodeSources(data)
	if err != nil {
		return nil, err
	}
	v, err := buildView(sources)
	if err != nil {
		return nil, fmt.Errorf("employer: corrupt index: %w", err)
	}
	return &Index{sources: sources, view: v}, nil
}

func decodeSources(data []byte) (map[string]Partial, error) {
	var doc fileDoc
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("employer: corrupt index: %w", err)
	}
	if dec.More() {
		return nil, errors.New("employer: corrupt index: trailing data")
	}
	if doc.Version != SchemaVersion {
		return nil, fmt.Errorf("employer: unsupported index schema version %d (want %d)", doc.Version, SchemaVersion)
	}
	// The bytes Save hashed are the bytes on disk, so they are hashed as
	// read. A sources block re-spaced or re-ordered by hand no longer
	// matches, where re-encoding it used to.
	if sumOf(doc.Sources) != doc.Checksum {
		return nil, errors.New("employer: corrupt index: checksum mismatch")
	}
	var srcs []fileSource
	sdec := json.NewDecoder(bytes.NewReader(doc.Sources))
	sdec.DisallowUnknownFields()
	if err := sdec.Decode(&srcs); err != nil {
		return nil, fmt.Errorf("employer: corrupt index: %w", err)
	}
	doc.Sources = nil // decoded; let the bytes go
	sources := make(map[string]Partial, len(srcs))
	for _, s := range srcs {
		p := Partial{Source: s.SourceMeta, Names: s.Names}
		if err := validatePartial(p); err != nil {
			return nil, fmt.Errorf("employer: corrupt index: %w", err)
		}
		if _, dup := sources[s.SHA256]; dup {
			return nil, fmt.Errorf("employer: corrupt index: duplicate source %s", s.SHA256)
		}
		sources[s.SHA256] = p
	}
	return sources, nil
}
