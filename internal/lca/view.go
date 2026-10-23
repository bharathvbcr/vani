package lca

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// ViewVersion is the on-disk lookup-view schema.
const ViewVersion = 1

const (
	viewFormat         = "vani-lca-view"
	maxViewHeaderBytes = 4096
)

// View is the read-only form of an index that Lookup needs: every normalised
// employer in sorted order with its certified H-1B count in each fiscal year of
// the badge window. It is held as one string, so a loaded view costs about its
// file size. A View is immutable and safe for concurrent use.
type View struct {
	n    int
	fys  []int  // the badge window, ascending, at most badgeWindow years
	body string // offsets (n+1 little-endian uint32) | counts (n*len(fys) uint32, employer-major) | names
	cnt  int    // start of the counts in body
	blob int    // start of the names in body
}

// Len reports the number of distinct normalised employers.
func (v *View) Len() int { return v.n }

// Coverage returns the badge-window fiscal years, ascending: the last (up to)
// three covered years of the index the view was derived from.
func (v *View) Coverage() []int { return append([]int(nil), v.fys...) }

func (v *View) u32(at int) int {
	s := v.body[at : at+4]
	return int(uint32(s[0]) | uint32(s[1])<<8 | uint32(s[2])<<16 | uint32(s[3])<<24)
}

func (v *View) key(i int) string { return v.body[v.blob+v.u32(4*i) : v.blob+v.u32(4*i+4)] }

func (v *View) search(s string) int {
	return sort.Search(v.n, func(i int) bool { return v.key(i) >= s })
}

// Lookup matches company against the view; see lookup.
func (v *View) Lookup(company string) Badge { return lookup(v, v.fys, company) }

func (v *View) exact(norm string) (int, bool) {
	i := v.search(norm)
	return i, i < v.n && v.key(i) == norm
}

func (v *View) prefix(p string) (int, bool) {
	i := v.search(p)
	if i >= v.n || !strings.HasPrefix(v.key(i), p) {
		return 0, false
	}
	if i+1 < v.n && strings.HasPrefix(v.key(i+1), p) {
		return 0, false
	}
	return i, true
}

func (v *View) h1b(ref, fy int) int {
	for j, y := range v.fys {
		if y == fy {
			return v.u32(v.cnt + 4*(ref*len(v.fys)+j))
		}
	}
	return 0
}

// buildView derives the view of x's merged data.
func buildView(x *Index) (*View, error) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	fys := append([]int(nil), lookupWindow(x.coverage)...)
	n := len(x.keys)
	names := 0
	for _, k := range x.keys {
		names += len(k)
	}
	size := 4*(n+1) + 4*n*len(fys) + names
	if size > maxIndexFileBytes {
		return nil, fmt.Errorf("lca: view would take %d bytes, over the %d-byte limit", size, maxIndexFileBytes)
	}
	var b strings.Builder
	b.Grow(size)
	put := func(o int) {
		b.WriteByte(byte(o))
		b.WriteByte(byte(o >> 8))
		b.WriteByte(byte(o >> 16))
		b.WriteByte(byte(o >> 24))
	}
	o := 0
	put(o)
	for _, k := range x.keys {
		o += len(k)
		put(o)
	}
	for _, k := range x.keys {
		for _, fy := range fys {
			c := x.merged[k][fy].H1B
			if c < 0 || int64(c) > math.MaxUint32 {
				return nil, fmt.Errorf("lca: employer %q FY%d count %d does not fit a view", k, fy, c)
			}
			put(c)
		}
	}
	for _, k := range x.keys {
		b.WriteString(k)
	}
	return &View{n: n, fys: fys, body: b.String(), cnt: 4 * (n + 1), blob: 4*(n+1) + 4*n*len(fys)}, nil
}

// validate checks every invariant Lookup relies on: offsets spanning the names
// and strictly increasing (no empty name), names strictly sorted (binary
// search, no duplicates). Names are not re-normalised: the index checksum
// covered what the view was derived from, and the body checksum covers what
// was written.
func (v *View) validate() error {
	names := len(v.body) - v.blob
	if v.u32(0) != 0 || v.u32(4*v.n) != names {
		return errors.New("name offsets do not span the names")
	}
	for i := 0; i < v.n; i++ {
		if o := v.u32(4*i + 4); o <= v.u32(4*i) || o > names {
			return fmt.Errorf("employer %d: bad offset", i)
		}
		if i > 0 && v.key(i-1) >= v.key(i) {
			return fmt.Errorf("employer %d: names not strictly sorted", i)
		}
	}
	return nil
}

// viewHeader is the first line of a view file; the body follows it.
type viewHeader struct {
	Format      string `json:"format"`
	Version     int    `json:"version"`
	IndexSHA256 string `json:"index_sha256"` // of the index file bytes the view was derived from
	IndexBytes  int64  `json:"index_bytes"`
	Employers   int    `json:"employers"`
	FYs         []int  `json:"fiscal_years"`
	BodyBytes   int64  `json:"body_bytes"`
	BodySHA256  string `json:"body_sha256"`
}

// ViewPath is where the lookup view derived from the index at indexPath lives.
func ViewPath(indexPath string) string {
	return strings.TrimSuffix(indexPath, filepath.Ext(indexPath)) + ".view"
}

func writeView(path string, v *View, indexSum string, indexBytes int64) error {
	body := sha256.Sum256([]byte(v.body))
	h, err := json.Marshal(viewHeader{
		Format: viewFormat, Version: ViewVersion,
		IndexSHA256: indexSum, IndexBytes: indexBytes,
		Employers: v.n, FYs: v.fys, BodyBytes: int64(len(v.body)), BodySHA256: hex.EncodeToString(body[:]),
	})
	if err != nil {
		return fmt.Errorf("lca: encode view: %w", err)
	}
	data := make([]byte, 0, len(h)+1+len(v.body))
	data = append(append(append(data, h...), '\n'), v.body...)
	return atomicfile.Write(path, data)
}

// ErrStaleView marks a view derived from other index bytes than the current.
var ErrStaleView = errors.New("lca: view was derived from a different index file")

// readView reads a view file and refuses one that is corrupt, truncated, of an
// unknown format or version, or not derived from the index whose bytes hash to
// indexSum.
func readView(path, indexSum string, indexBytes int64) (*View, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("lca: open view: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("lca: stat view: %w", err)
	}
	br := bufio.NewReaderSize(f, maxViewHeaderBytes)
	line, err := br.ReadSlice('\n')
	if err != nil {
		return nil, fmt.Errorf("lca: corrupt view: header: %w", err)
	}
	var h viewHeader
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&h); err != nil {
		return nil, fmt.Errorf("lca: corrupt view: header: %w", err)
	}
	if dec.More() {
		return nil, errors.New("lca: corrupt view: trailing header data")
	}
	if h.Format != viewFormat {
		return nil, fmt.Errorf("lca: corrupt view: format %q", h.Format)
	}
	if h.Version != ViewVersion {
		return nil, fmt.Errorf("lca: unsupported view schema version %d (want %d)", h.Version, ViewVersion)
	}
	if h.IndexSHA256 != indexSum || h.IndexBytes != indexBytes {
		return nil, ErrStaleView
	}
	if len(h.FYs) > badgeWindow {
		return nil, fmt.Errorf("lca: corrupt view: %d fiscal years", len(h.FYs))
	}
	for i, fy := range h.FYs {
		if fy < minFY || fy > maxFY || (i > 0 && fy <= h.FYs[i-1]) {
			return nil, fmt.Errorf("lca: corrupt view: fiscal years %v", h.FYs)
		}
	}
	// The body length is the file's own remainder, so a forged header cannot ask
	// for more memory than the file holds. Each employer takes at least five
	// body bytes, so bounding Employers by BodyBytes first keeps the minimum
	// below from overflowing.
	nfy := int64(len(h.FYs))
	if h.BodyBytes != st.Size()-int64(len(line)) || h.BodyBytes > maxIndexFileBytes ||
		h.Employers < 0 || int64(h.Employers) > h.BodyBytes || h.BodyBytes < (5+4*nfy)*int64(h.Employers)+4 {
		return nil, fmt.Errorf("lca: corrupt view: %d employers in a %d-byte body of a %d-byte file", h.Employers, h.BodyBytes, st.Size())
	}
	var b strings.Builder
	b.Grow(int(h.BodyBytes))
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(&b, sum), io.LimitReader(br, h.BodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("lca: read view: %w", err)
	}
	if n != h.BodyBytes {
		return nil, fmt.Errorf("lca: corrupt view: body is %d bytes, header says %d", n, h.BodyBytes)
	}
	if hex.EncodeToString(sum.Sum(nil)) != h.BodySHA256 {
		return nil, errors.New("lca: corrupt view: checksum mismatch")
	}
	cnt := 4 * (h.Employers + 1)
	v := &View{n: h.Employers, fys: h.FYs, body: b.String(), cnt: cnt, blob: cnt + 4*h.Employers*len(h.FYs)}
	if err := v.validate(); err != nil {
		return nil, fmt.Errorf("lca: corrupt view: %w", err)
	}
	return v, nil
}

// ViewInfo reports how LoadView produced its view.
type ViewInfo struct {
	// Rebuilt is set when the view file was missing, stale or refused, so the
	// full index was loaded and the view derived from it.
	Rebuilt bool
	Reason  error // why, when Rebuilt
	// WriteErr is set when the rebuilt view could not be written back; the next
	// load then rebuilds again.
	WriteErr error
}

// Routine reports whether a rebuild is the expected kind (no view yet, or the
// index was re-imported) rather than a refused, corrupt view or a failed
// write, which deserve a visible note.
func (i ViewInfo) Routine() bool {
	if i.WriteErr != nil {
		return false
	}
	return !i.Rebuilt || errors.Is(i.Reason, fs.ErrNotExist) || errors.Is(i.Reason, ErrStaleView)
}

// LoadView returns the read-only lookup view of the index at indexPath without
// decoding it. It reads ViewPath(indexPath) when that file is intact and was
// derived from the index file's current bytes (both are hashed). Otherwise it
// loads the full index, refusing a corrupt one as Load does, derives the view
// and writes it back; a view is never served for other index bytes than it was
// derived from. An absent index is an error wrapping fs.ErrNotExist.
func LoadView(indexPath string) (*View, ViewInfo, error) {
	sum, size, err := hashIndexFile(indexPath)
	if err != nil {
		return nil, ViewInfo{}, err
	}
	viewPath := ViewPath(indexPath)
	v, err := readView(viewPath, sum, size)
	if err == nil {
		return v, ViewInfo{}, nil
	}
	info := ViewInfo{Rebuilt: true, Reason: err}
	data, err := readIndexFile(indexPath)
	if err != nil {
		return nil, ViewInfo{}, err
	}
	x, err := decodeIndex(data)
	if err != nil {
		return nil, ViewInfo{}, err
	}
	if v, err = buildView(x); err != nil {
		return nil, ViewInfo{}, err
	}
	// Bound to the bytes just decoded, not to sum: the file may have changed
	// since it was hashed above.
	ds := sha256.Sum256(data)
	info.WriteErr = writeView(viewPath, v, hex.EncodeToString(ds[:]), int64(len(data)))
	return v, info, nil
}

// hashIndexFile streams the index file through sha256 without holding it.
func hashIndexFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("lca: open index: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxIndexFileBytes+1))
	if err != nil {
		return "", 0, fmt.Errorf("lca: read index: %w", err)
	}
	if n > maxIndexFileBytes {
		return "", 0, fmt.Errorf("lca: index file exceeds %d bytes", maxIndexFileBytes)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
