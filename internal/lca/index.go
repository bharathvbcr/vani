package lca

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/indexutil"
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
	_, err := x.merge(p, replaceNone)
	return err
}

// MergeReplacingYear is Merge for an import: it also drops every other source
// whose fiscal year (see PrimaryFY) is p's, because DOL publishes one file per
// fiscal year, so a different file for the same year is a correction or a
// later cumulative quarter of it, and adding both would double its counts. It
// returns the sources it dropped. It refuses with ErrShrinks, changing nothing,
// when the dropped sources hold more counted cases in any fiscal year than p
// does: an earlier quarter replaces nothing, a cumulative later one does.
func (x *Index) MergeReplacingYear(p Partial) ([]SourceMeta, error) {
	return x.merge(p, replaceGuarded)
}

// MergeReplacingYearAnyway is MergeReplacingYear without the ErrShrinks guard,
// for an import the person asked to shrink the year (`vani lca import --replace`).
func (x *Index) MergeReplacingYearAnyway(p Partial) ([]SourceMeta, error) {
	return x.merge(p, replaceAnyway)
}

// ErrShrinks means an import would replace files holding more counted cases
// than itself. The error names the fiscal year, the files and both totals.
var ErrShrinks = errors.New("lca: the file would shrink the fiscal year")

type replaceMode int

const (
	replaceNone    replaceMode = iota // Merge: add, never drop
	replaceGuarded                    // drop same-year sources unless that loses cases
	replaceAnyway                     // drop same-year sources whatever they hold
)

// yearTotals is the counted cases of a source per fiscal year, the sum
// PrimaryFY ranks by.
func yearTotals(p Partial) map[int]int {
	total := map[int]int{}
	for _, byFY := range p.Employers {
		for fy, c := range byFY {
			total[fy] += c.H1B + c.H1BWithdrawn + c.H1B1 + c.E3
		}
	}
	return total
}

// shrinkError is ErrShrinks naming what the replacement would lose, or nil when
// no fiscal year ends with fewer cases than the dropped sources held.
func shrinkError(fy int, dropped []Partial, p Partial) error {
	gain, lost := yearTotals(p), map[int]int{}
	var names []string
	var droppedAll, newAll int
	for _, d := range dropped {
		names = append(names, d.Source.Name)
		for y, n := range yearTotals(d) {
			lost[y] += n
			droppedAll += n
		}
	}
	for _, n := range gain {
		newAll += n
	}
	var years []int
	for y, n := range lost {
		if n > gain[y] {
			years = append(years, y)
		}
	}
	if len(years) == 0 {
		return nil
	}
	sort.Ints(years)
	return fmt.Errorf("%w: fiscal year %d would be replaced: %s hold %d counted cases, %s holds %d (fiscal years short: %v)",
		ErrShrinks, fy, strings.Join(names, ", "), droppedAll, p.Source.Name, newAll, years)
}

func (x *Index) merge(p Partial, mode replaceMode) ([]SourceMeta, error) {
	if p.Source.SHA256 == "" {
		return nil, errors.New("lca: partial has no source sha256")
	}
	if err := validateEmployers(p.Employers); err != nil {
		return nil, err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.sources == nil {
		x.sources = map[string]Partial{}
	}
	var dropped []SourceMeta
	if fy := PrimaryFY(p); mode != replaceNone && fy != 0 {
		var olds []Partial
		for _, old := range sortedSources(x.sources) {
			if old.Source.SHA256 != p.Source.SHA256 && PrimaryFY(old) == fy {
				olds = append(olds, old)
			}
		}
		if mode == replaceGuarded {
			if err := shrinkError(fy, olds, p); err != nil {
				return nil, err
			}
		}
		for _, old := range olds {
			dropped = append(dropped, old.Source)
			delete(x.sources, old.Source.SHA256)
		}
	}
	x.sources[p.Source.SHA256] = p
	x.rebuild()
	return dropped, nil
}

// PrimaryFY is the fiscal year holding most of a source's counted cases (the
// earliest on a tie), or 0 for a source with none. A file carries a few cases
// of neighbouring years (a decision date just past a boundary), which must not
// make it replace those years' own files.
func PrimaryFY(p Partial) int {
	total := yearTotals(p)
	best, bestN := 0, 0
	for _, fy := range sortedKeys(total) {
		if total[fy] > bestN {
			best, bestN = fy, total[fy]
		}
	}
	return best
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
	return indexutil.SortedBySHA(m, func(p Partial) string { return p.Source.SHA256 })
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

// fileDoc is the index file. Sources is kept as the bytes json.Marshal gave
// []fileSource: the checksum is of exactly those bytes, so Load hashes what it
// read instead of re-encoding the whole index to compare.
type fileDoc struct {
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"` // sha256 of the JSON encoding of Sources
	Sources  json.RawMessage `json:"sources"`
}

func encodeSources(sources []fileSource) ([]byte, string, error) {
	b, err := json.Marshal(sources)
	if err != nil {
		return nil, "", err
	}
	return b, indexutil.SumHex(b), nil
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
	b, sum, err := encodeSources(srcs)
	if err != nil {
		x.mu.RUnlock()
		return fmt.Errorf("lca: encode index: %w", err)
	}
	data, err := json.Marshal(fileDoc{Version: SchemaVersion, Checksum: sum, Sources: b})
	x.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("lca: encode index: %w", err)
	}
	return atomicfile.Write(path, data)
}

// ErrBusy means another import held the index for longer than the wait bound.
var ErrBusy = indexutil.ErrBusy

// Update is the import path: under the index's lock it merges p into the index
// at path (an absent file is an empty index) and saves it, so two imports never
// overwrite each other's rows. A corrupt index or a refused partial leaves the
// file as it was; a lock held past indexutil.LockWait is ErrBusy. The sources
// that p replaced, a corrected file for the same fiscal year, are returned.
// A replacement that would lose cases is ErrShrinks; Update never allows it.
func Update(path string, p Partial) (*Index, []SourceMeta, error) {
	return UpdateWith(path, p, UpdateOptions{})
}

// UpdateOptions are the import's choices beyond the file itself.
type UpdateOptions struct {
	// Replace allows a replacement that shrinks the fiscal year
	// (`vani lca import --replace`).
	Replace bool
	// Waiting, when set, is called once (from another goroutine) with the
	// index path if the lock is still held after indexutil.NoticeAfter.
	Waiting func(index string)
}

// UpdateWith is UpdateContext without a context to cancel the lock wait.
func UpdateWith(path string, p Partial, opts UpdateOptions) (*Index, []SourceMeta, error) {
	return UpdateContext(context.Background(), path, p, opts)
}

// UpdateContext is Update with options; ctx ending stops the wait for the lock.
func UpdateContext(ctx context.Context, path string, p Partial, opts UpdateOptions) (*Index, []SourceMeta, error) {
	path, release, err := indexutil.LockNotify(ctx, path, opts.Waiting)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	x, err := Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		x, err = NewIndex(), nil
	}
	if err != nil {
		return nil, nil, err
	}
	merge := x.MergeReplacingYear
	if opts.Replace {
		merge = x.MergeReplacingYearAnyway
	}
	dropped, err := merge(p)
	if err != nil {
		return nil, nil, err
	}
	if err := x.Save(path); err != nil {
		return nil, nil, err
	}
	return x, dropped, nil
}

// Load reads an index written by Save. A corrupted, truncated, unknown-version
// or internally inconsistent file is refused; nothing partial is returned.
func Load(path string) (*Index, error) {
	data, err := readIndexFile(path)
	if err != nil {
		return nil, err
	}
	return decodeIndex(data)
}

// readIndexFile reads the index file, bounded by maxIndexFileBytes.
func readIndexFile(path string) ([]byte, error) {
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
	return data, nil
}

// decodeIndex decodes and verifies the bytes of an index file.
func decodeIndex(data []byte) (*Index, error) {
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
	// The bytes Save hashed are the bytes on disk, so they are hashed as
	// read. A sources block re-spaced or re-ordered by hand no longer
	// matches, where re-encoding it used to.
	if indexutil.SumHex(doc.Sources) != doc.Checksum {
		return nil, errors.New("lca: corrupt index: checksum mismatch")
	}
	var sources []fileSource
	sdec := json.NewDecoder(bytes.NewReader(doc.Sources))
	sdec.DisallowUnknownFields()
	if err := sdec.Decode(&sources); err != nil {
		return nil, fmt.Errorf("lca: corrupt index: %w", err)
	}
	doc.Sources = nil // its bytes are decoded; let them go before rebuild
	idx := &Index{sources: map[string]Partial{}}
	for _, s := range sources {
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
