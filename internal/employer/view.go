package employer

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// ViewVersion is the on-disk lookup-view schema.
const ViewVersion = 1

const (
	viewFormat         = "vani-employer-view"
	maxViewHeaderBytes = 4096
)

// kindNames lists the source kinds in sorted order: bit i of a kinds mask is
// kindNames[i], so a mask expands to a sorted list.
var kindNames = []string{KindIPEDS, KindIRSBMF}

// A name's meta byte holds its attribute bits low and its kinds mask high.
const (
	kindsShift = 4
	attrMask   = 1<<kindsShift - 1
	kindsAll   = 1<<2 - 1 // one bit per kindNames entry
)

func kindBit(kind string) uint8 {
	for i, k := range kindNames {
		if k == kind {
			return 1 << i
		}
	}
	return 0
}

func kindsOf(mask uint8) []string {
	out := make([]string, 0, len(kindNames))
	for i, k := range kindNames {
		if mask&(1<<i) != 0 {
			out = append(out, k)
		}
	}
	return out
}

// View is the merged, read-only form of an index: every normalised name in
// sorted order with its attributes and the kinds of source that listed it.
// It is all Lookup needs, and it is held as one string so a loaded view costs
// about its file size. A View is immutable and safe for concurrent use.
type View struct {
	n    int
	body string // offsets (n+1 little-endian uint32) | meta (n bytes) | names
	meta int    // start of the meta bytes in body
	blob int    // start of the names in body
}

// Len reports the number of distinct normalised names.
func (v *View) Len() int { return v.n }

func (v *View) off(i int) int {
	s := v.body[4*i : 4*i+4]
	return int(uint32(s[0]) | uint32(s[1])<<8 | uint32(s[2])<<16 | uint32(s[3])<<24)
}

func (v *View) key(i int) string { return v.body[v.blob+v.off(i) : v.blob+v.off(i+1)] }

// search returns the first index whose name is >= s.
func (v *View) search(s string) int {
	return sort.Search(v.n, func(i int) bool { return v.key(i) >= s })
}

// buildView merges the sources' names. Attributes and kinds are unions, so
// the order sources are visited in does not matter.
func buildView(sources map[string]Partial) (*View, error) {
	merged := map[string]uint8{}
	for _, p := range sources {
		kb := kindBit(p.Source.Kind) << kindsShift
		for name, a := range p.Names {
			merged[name] |= uint8(a) | kb
		}
	}
	keys := make([]string, 0, len(merged))
	names := 0
	for k := range merged {
		keys = append(keys, k)
		names += len(k)
	}
	sort.Strings(keys)
	n := len(keys)
	size := 4*(n+1) + n + names
	if size > maxIndexFileBytes {
		return nil, fmt.Errorf("employer: merged index would take %d bytes, over the %d-byte limit", size, maxIndexFileBytes)
	}
	var b strings.Builder
	b.Grow(size)
	putOff := func(o int) {
		b.WriteByte(byte(o))
		b.WriteByte(byte(o >> 8))
		b.WriteByte(byte(o >> 16))
		b.WriteByte(byte(o >> 24))
	}
	o := 0
	putOff(o)
	for _, k := range keys {
		o += len(k)
		putOff(o)
	}
	for _, k := range keys {
		b.WriteByte(merged[k])
	}
	for _, k := range keys {
		b.WriteString(k)
	}
	return &View{n: n, body: b.String(), meta: 4 * (n + 1), blob: 4*(n+1) + n}, nil
}

// validate checks every invariant Lookup relies on: offsets in bounds and
// strictly increasing (no empty name), names strictly sorted (binary search,
// no duplicates), and known attribute and kind bits on every name. Names are
// not re-normalised: Merge enforced that before the view was written, and the
// body checksum covers what was written.
func (v *View) validate() error {
	names := len(v.body) - v.blob
	if v.off(0) != 0 || v.off(v.n) != names {
		return errors.New("name offsets do not span the names")
	}
	for i := 0; i < v.n; i++ {
		if o := v.off(i + 1); o <= v.off(i) || o > names {
			return fmt.Errorf("name %d: bad offset", i)
		}
	}
	for i := 0; i < v.n; i++ {
		if i > 0 && v.key(i-1) >= v.key(i) {
			return fmt.Errorf("name %d: names not strictly sorted", i)
		}
		m := v.body[v.meta+i]
		a, k := m&attrMask, m>>kindsShift
		if a == 0 || a&^uint8(bitsAll) != 0 || k == 0 || k&^kindsAll != 0 {
			return fmt.Errorf("name %d: meta bits %#x", i, m)
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
	Names       int    `json:"names"`
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
		Names: v.n, BodyBytes: int64(len(v.body)), BodySHA256: hex.EncodeToString(body[:]),
	})
	if err != nil {
		return fmt.Errorf("employer: encode view: %w", err)
	}
	data := make([]byte, 0, len(h)+1+len(v.body))
	data = append(append(append(data, h...), '\n'), v.body...)
	return atomicfile.Write(path, data)
}

// errStaleView marks a view derived from other index bytes than the current.
var errStaleView = errors.New("employer: view was derived from a different index file")

// readView reads a view file and refuses one that is corrupt, truncated, of
// an unknown format or version, or not derived from the index whose bytes
// hash to indexSum.
func readView(path, indexSum string, indexBytes int64) (*View, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("employer: open view: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("employer: stat view: %w", err)
	}
	br := bufio.NewReaderSize(f, maxViewHeaderBytes)
	line, err := br.ReadSlice('\n')
	if err != nil {
		return nil, fmt.Errorf("employer: corrupt view: header: %w", err)
	}
	var h viewHeader
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&h); err != nil {
		return nil, fmt.Errorf("employer: corrupt view: header: %w", err)
	}
	if dec.More() {
		return nil, errors.New("employer: corrupt view: trailing header data")
	}
	if h.Format != viewFormat {
		return nil, fmt.Errorf("employer: corrupt view: format %q", h.Format)
	}
	if h.Version != ViewVersion {
		return nil, fmt.Errorf("employer: unsupported view schema version %d (want %d)", h.Version, ViewVersion)
	}
	if h.IndexSHA256 != indexSum || h.IndexBytes != indexBytes {
		return nil, errStaleView
	}
	// The body length is the file's own remainder, so a forged header cannot
	// ask for more memory than the file holds. Each name takes at least five
	// body bytes, so bounding Names by BodyBytes first keeps 5*Names+4 from
	// overflowing.
	if h.BodyBytes != st.Size()-int64(len(line)) || h.BodyBytes > maxIndexFileBytes ||
		h.Names < 0 || int64(h.Names) > h.BodyBytes || h.BodyBytes < 5*int64(h.Names)+4 {
		return nil, fmt.Errorf("employer: corrupt view: %d names in a %d-byte body of a %d-byte file", h.Names, h.BodyBytes, st.Size())
	}
	var b strings.Builder
	b.Grow(int(h.BodyBytes))
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(&b, sum), io.LimitReader(br, h.BodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("employer: read view: %w", err)
	}
	if n != h.BodyBytes {
		return nil, fmt.Errorf("employer: corrupt view: body is %d bytes, header says %d", n, h.BodyBytes)
	}
	if hex.EncodeToString(sum.Sum(nil)) != h.BodySHA256 {
		return nil, errors.New("employer: corrupt view: checksum mismatch")
	}
	v := &View{n: h.Names, body: b.String(), meta: 4 * (h.Names + 1), blob: 5*h.Names + 4}
	if err := v.validate(); err != nil {
		return nil, fmt.Errorf("employer: corrupt view: %w", err)
	}
	return v, nil
}

// ViewInfo reports how LoadView produced its view.
type ViewInfo struct {
	// Rebuilt is set when the view file was missing, stale or refused, so the
	// full index was loaded and the view derived from it.
	Rebuilt bool
	Reason  error // why, when Rebuilt
	// WriteErr is set when the rebuilt view could not be written back; the
	// next load then rebuilds again.
	WriteErr error
}

// LoadView returns the read-only lookup view of the index at indexPath,
// without holding the per-source names. It reads ViewPath(indexPath) when that
// file is intact and was derived from the index file's current bytes (both
// are hashed). Otherwise it loads the full index, refusing a corrupt one as
// Load does, derives the view and writes it back. An absent index is an error
// wrapping fs.ErrNotExist.
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
	ds := sha256.Sum256(data)
	info.WriteErr = writeView(viewPath, x.view, hex.EncodeToString(ds[:]), int64(len(data)))
	return x.view, info, nil
}

// hashIndexFile streams the index file through sha256 without holding it.
func hashIndexFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("employer: open index: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxIndexFileBytes+1))
	if err != nil {
		return "", 0, fmt.Errorf("employer: read index: %w", err)
	}
	if n > maxIndexFileBytes {
		return "", 0, fmt.Errorf("employer: index file exceeds %d bytes", maxIndexFileBytes)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
