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
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/lca"
)

// refIndex is the map-backed merge and Lookup this package used before the
// View, kept verbatim as the oracle the View must agree with.
type refEntry struct {
	attrs attr
	kinds []string
}

type refIndex struct {
	merged map[string]refEntry
	keys   []string
}

func newRef(sources map[string]Partial) *refIndex {
	merged := map[string]refEntry{}
	for _, p := range sortedSources(sources) {
		for name, a := range p.Names {
			e := merged[name]
			e.attrs |= a
			if i := sort.SearchStrings(e.kinds, p.Source.Kind); i == len(e.kinds) || e.kinds[i] != p.Source.Kind {
				e.kinds = append(e.kinds, "")
				copy(e.kinds[i+1:], e.kinds[i:])
				e.kinds[i] = p.Source.Kind
			}
			merged[name] = e
		}
	}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return &refIndex{merged, keys}
}

func (x *refIndex) Lookup(company string) Badge {
	b := Badge{Confidence: ConfNone}
	norm := lca.Normalize(company)
	if norm == "" {
		return b
	}
	key := ""
	if _, ok := x.merged[norm]; ok {
		key, b.Confidence = norm, ConfExact
	} else if len(strings.Fields(norm)) >= 2 {
		p := norm + " "
		i := sort.SearchStrings(x.keys, p)
		if i < len(x.keys) && strings.HasPrefix(x.keys[i], p) && !(i+1 < len(x.keys) && strings.HasPrefix(x.keys[i+1], p)) {
			key, b.Confidence = x.keys[i], ConfUniquePrefix
		}
	}
	if key == "" {
		return b
	}
	e := x.merged[key]
	b.MatchedName = key
	b.Attributes = e.attrs.names()
	b.Kinds = append([]string(nil), e.kinds...)
	if b.Confidence == ConfExact && !strings.Contains(key, " ") && !contains(e.kinds, KindIPEDS) {
		b.Confidence = ConfExactOneWord
	}
	return b
}

// fixtureIndex merges every synthetic fixture in this package's tests.
func fixtureIndex(t *testing.T) *Index {
	t.Helper()
	x := NewIndex()
	for name, csv := range map[string]string{
		"eo_zz.csv":  bmfCSV,
		"hd2024.csv": ipedsCSV,
		"eo_one.csv": "EIN,NAME,SUBSECTION\n000000001,ZORP INC,03\n000000002,QUILLWORT FOUNDATION,03\n",
		"hd_one.csv": "UNITID,INSTNM,CONTROL,DEGGRANT\n1,Nowhereversity,2,1\n",
	} {
		if err := x.Merge(mustImport(t, csv, name)); err != nil {
			t.Fatal(err)
		}
	}
	return x
}

// probes are queries for every name: exact, as a company would write it,
// each whole-token prefix, and names that are absent.
func probes(keys []string) []string {
	out := []string{"", " ", "Inc.", "The", "zzz", "AAAA", "Quillwort", "Zorp Inc.", "State University", "Nowhere"}
	for _, k := range keys {
		out = append(out, k, strings.ToLower(k), "The "+k+", Inc.", k+" X")
		f := strings.Fields(k)
		for i := 1; i < len(f); i++ {
			out = append(out, strings.Join(f[:i], " "))
		}
	}
	return out
}

func assertParity(t *testing.T, ref *refIndex, qs []string, lookups map[string]func(string) Badge) {
	t.Helper()
	fails := 0
	for _, q := range qs {
		want := ref.Lookup(q)
		for name, lookup := range lookups {
			got := lookup(q)
			if !reflect.DeepEqual(got, want) || got.String() != want.String() || got.Confident() != want.Confident() {
				t.Errorf("%s Lookup(%q) = %+v, reference %+v", name, q, got, want)
				if fails++; fails > 20 {
					t.Fatal("too many mismatches")
				}
			}
		}
	}
}

func saveAndLoadView(t *testing.T, x *Index) (string, *View) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.json")
	if err := x.Save(path); err != nil {
		t.Fatal(err)
	}
	v, info, err := LoadView(path)
	if err != nil || info.Rebuilt {
		t.Fatalf("LoadView after Save: %v %+v", err, info)
	}
	return path, v
}

func TestViewParityWithReference(t *testing.T) {
	x := fixtureIndex(t)
	path, v := saveAndLoadView(t, x)
	full, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	ref := newRef(x.sources)
	assertParity(t, ref, probes(ref.keys), map[string]func(string) Badge{
		"Index": x.Lookup, "LoadView": v.Lookup, "Load": full.Lookup,
	})
	if v.Len() != x.Names() || v.Len() != len(ref.keys) {
		t.Fatalf("names: view %d index %d reference %d", v.Len(), x.Names(), len(ref.keys))
	}
}

// TestViewParityGenerated stresses the prefix rule: names drawn from a small
// vocabulary share long prefixes, and the two kinds overlap.
func TestViewParityGenerated(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	words := []string{"ALPHA", "BETA", "GAMMA", "DELTA", "NORTH", "STATE", "UNIVERSITY", "OF", "INSTITUTE", "Ä", "Z"}
	gen := func(n int) map[string]attr {
		m := map[string]attr{}
		for len(m) < n {
			f := make([]string, 1+r.IntN(5))
			for i := range f {
				f[i] = words[r.IntN(len(words))]
			}
			if k := lca.Normalize(strings.Join(f, " ")); k != "" {
				m[k] = attr(1 + r.IntN(3))
			}
		}
		return m
	}
	irs := gen(1500)
	for k := range irs {
		irs[k] = bitNonprofit
	}
	x := NewIndex()
	for _, p := range []Partial{
		{Source: SourceMeta{Name: "eo.csv", Kind: KindIRSBMF, SHA256: "a"}, Names: irs},
		{Source: SourceMeta{Name: "hd.csv", Kind: KindIPEDS, SHA256: "b"}, Names: gen(800)},
	} {
		if err := x.Merge(p); err != nil {
			t.Fatal(err)
		}
	}
	_, v := saveAndLoadView(t, x)
	ref := newRef(x.sources)
	assertParity(t, ref, probes(ref.keys), map[string]func(string) Badge{"Index": x.Lookup, "LoadView": v.Lookup})
}

// Update is the import path. It must write exactly what NewIndex, Merge and
// Save write, be idempotent, and leave both files alone when it refuses.
func TestUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	for _, c := range []struct{ csv, name string }{{bmfCSV, "eo_zz.csv"}, {ipedsCSV, "hd2024.csv"}} {
		if _, err := Update(path, mustImport(t, c.csv, c.name)); err != nil {
			t.Fatal(err)
		}
	}
	want := filepath.Join(dir, "want.json")
	if err := buildIndex(t).Save(want); err != nil {
		t.Fatal(err)
	}
	read := func(p string) []byte {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	got, gotView := read(path), read(ViewPath(path))
	if !bytes.Equal(got, read(want)) {
		t.Fatal("Update wrote a different index than Merge and Save")
	}
	x, err := Update(path, mustImport(t, bmfCSV, "eo_zz.csv"))
	if err != nil || x.Names() != buildIndex(t).Names() {
		t.Fatalf("re-import: %v", err)
	}
	if !bytes.Equal(read(path), got) || !bytes.Equal(read(ViewPath(path)), gotView) {
		t.Fatal("re-importing the same file changed the index or view")
	}
	if v, info, err := LoadView(path); err != nil || info.Rebuilt || !v.Lookup("Quillwort Foundation").Confident() {
		t.Fatalf("view after Update: %v %+v", err, info)
	}
	// Refusals: a bad partial, then a corrupt index. Neither file changes.
	if _, err := Update(path, Partial{Source: SourceMeta{SHA256: "x", Kind: "nope"}}); err == nil {
		t.Fatal("bad partial merged")
	}
	if !bytes.Equal(read(path), got) || !bytes.Equal(read(ViewPath(path)), gotView) {
		t.Fatal("a refused partial changed the files")
	}
	corrupt := got[:len(got)/2]
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(path, mustImport(t, ipedsCSV, "hd.csv")); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("corrupt index updated: %v", err)
	}
	if !bytes.Equal(read(path), corrupt) || !bytes.Equal(read(ViewPath(path)), gotView) {
		t.Fatal("a refused update changed the files")
	}
}

// The zero Index is empty, as it was before the view: Lookup, Names, Save
// and Merge all work on it.
func TestZeroIndex(t *testing.T) {
	var x Index
	if b := x.Lookup("Quillwort Foundation"); b.Confidence != ConfNone || x.Names() != 0 {
		t.Fatalf("zero index: %+v, %d names", b, x.Names())
	}
	path := filepath.Join(t.TempDir(), "index.json")
	if err := x.Save(path); err != nil {
		t.Fatal(err)
	}
	if v, _, err := LoadView(path); err != nil || v.Len() != 0 {
		t.Fatalf("zero index round trip: %v", err)
	}
	if err := x.Merge(mustImport(t, bmfCSV, "eo.csv")); err != nil || !x.Lookup("Quillwort Foundation").Confident() {
		t.Fatalf("merge into zero index: %v", err)
	}
}

func TestEmptyIndexView(t *testing.T) {
	path, v := saveAndLoadView(t, NewIndex())
	if v.Len() != 0 || v.Lookup("Anything").Confidence != ConfNone {
		t.Fatalf("empty view: %d names", v.Len())
	}
	if _, err := os.Stat(ViewPath(path)); err != nil {
		t.Fatal(err)
	}
}

// signView returns a view file whose header and checksum match body, so the
// structural checks, not the checksum, must refuse it.
func signView(t *testing.T, indexPath string, names int, body string) []byte {
	t.Helper()
	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	is, bs := sha256.Sum256(data), sha256.Sum256([]byte(body))
	h, err := json.Marshal(viewHeader{Format: viewFormat, Version: ViewVersion,
		IndexSHA256: hex.EncodeToString(is[:]), IndexBytes: int64(len(data)),
		Names: names, BodyBytes: int64(len(body)), BodySHA256: hex.EncodeToString(bs[:])})
	if err != nil {
		t.Fatal(err)
	}
	return append(append(h, '\n'), body...)
}

func TestViewFileRefusals(t *testing.T) {
	x := fixtureIndex(t)
	path, good := saveAndLoadView(t, x)
	vp := ViewPath(path)
	data, err := os.ReadFile(vp)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(vp)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("view mode %v", st.Mode())
	}
	nl := bytes.IndexByte(data, '\n')
	body := string(data[nl+1:])
	n := good.Len()
	// The same names in reverse order, laid out like buildView would.
	var rev, metas, names strings.Builder
	o := 0
	put := func(o int) { rev.Write([]byte{byte(o), byte(o >> 8), byte(o >> 16), byte(o >> 24)}) }
	put(0)
	for i := n - 1; i >= 0; i-- {
		o += len(good.key(i))
		put(o)
		metas.WriteByte(good.body[good.meta+i])
		names.WriteString(good.key(i))
	}
	unsorted := rev.String() + metas.String() + names.String()

	replace := func(old, new string) []byte { return bytes.Replace(data, []byte(old), []byte(new), 1) }
	cases := map[string]func() []byte{
		"truncated":       func() []byte { return data[:len(data)-7] },
		"header only":     func() []byte { return data[:nl+1] },
		"no header":       func() []byte { return []byte(body) },
		"empty":           func() []byte { return nil },
		"tampered":        func() []byte { return replace("QUILLWORT", "QUILLWORX") },
		"version":         func() []byte { return replace(`"version":1`, `"version":9`) },
		"format":          func() []byte { return replace(viewFormat, "vani-other-view") },
		"unknown field":   func() []byte { return replace(`"version":1`, `"version":1,"extra":true`) },
		"trailing":        func() []byte { return append(append([]byte{}, data...), "{}"...) },
		"names overstate": func() []byte { return signView(t, path, n+1, body) },
		"unsorted":        func() []byte { return signView(t, path, n, unsorted) },
		// 5*names+4 wraps to 3 in int64: refused by a division-safe bound,
		// never a slice panic.
		"names huge": func() []byte { return signView(t, path, 3689348814741910323, body) },
		"meta bits": func() []byte {
			b := []byte(body)
			b[good.meta] = 0x01 // attribute but no kind
			return signView(t, path, n, string(b))
		},
		"bad offset": func() []byte {
			b := []byte(body)
			b[4], b[5], b[6], b[7] = 0xff, 0xff, 0xff, 0x7f
			return signView(t, path, n, string(b))
		},
		"stale": func() []byte {
			y := fixtureIndex(t)
			if err := y.Merge(mustImport(t, "UNITID,INSTNM,CONTROL,DEGGRANT\n9,Elsewhere Polytechnic,1,1\n", "hd9.csv")); err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(t.TempDir(), "index.json")
			if err := y.Save(other); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(ViewPath(other))
			if err != nil {
				t.Fatal(err)
			}
			return b
		},
	}
	sum, size, err := hashIndexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ref := newRef(x.sources)
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(vp, mutate(), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readView(vp, sum, size); err == nil {
				t.Fatal("view accepted")
			} else if name == "stale" && !errors.Is(err, errStaleView) {
				t.Fatalf("stale view: %v", err)
			}
			// LoadView never uses the refused file: it rebuilds from the
			// index, reports why, and writes a good view back.
			v, info, err := LoadView(path)
			if err != nil || !info.Rebuilt || info.Reason == nil || info.WriteErr != nil {
				t.Fatalf("LoadView: %v %+v", err, info)
			}
			assertParity(t, ref, probes(ref.keys), map[string]func(string) Badge{"rebuilt": v.Lookup})
			if _, info, err := LoadView(path); err != nil || info.Rebuilt {
				t.Fatalf("view not written back: %v %+v", err, info)
			}
		})
	}
}

func TestLoadViewMissingViewAndUnwritableView(t *testing.T) {
	x := fixtureIndex(t)
	path, _ := saveAndLoadView(t, x)
	vp := ViewPath(path)
	if err := os.Remove(vp); err != nil {
		t.Fatal(err)
	}
	v, info, err := LoadView(path)
	if err != nil || !info.Rebuilt || !errors.Is(info.Reason, fs.ErrNotExist) || info.WriteErr != nil {
		t.Fatalf("missing view: %v %+v", err, info)
	}
	if !reflect.DeepEqual(v.Lookup("Quillwort Foundation"), x.Lookup("Quillwort Foundation")) {
		t.Fatal("rebuilt view differs")
	}
	// A directory where the view belongs: the lookup still works and the
	// failed write-back is reported, not swallowed.
	if err := os.Remove(vp); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(vp, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	v, info, err = LoadView(path)
	if err != nil || !info.Rebuilt || info.WriteErr == nil || v.Lookup("Quillwort Foundation").Confidence != ConfExact {
		t.Fatalf("unwritable view: %v %+v", err, info)
	}
}

// TestLoadViewRefusesCorruptIndex: with a good view in place, a corrupt index
// file is still refused, because the view is bound to the index's bytes.
func TestLoadViewRefusesCorruptIndex(t *testing.T) {
	x := fixtureIndex(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	if err := x.Save(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	view, _ := os.ReadFile(ViewPath(path))
	for name, mut := range map[string][]byte{
		"truncated": data[:len(data)/2],
		"tampered":  bytes.Replace(data, []byte("QUILLWORT"), []byte("QUILLWORX"), 1),
		"version":   bytes.Replace(data, []byte(`"version":1`), []byte(`"version":9`), 1),
		"trailing":  append(append([]byte{}, data...), []byte("{}")...),
		"unknown":   bytes.Replace(data, []byte(`"version":1`), []byte(`"version":1,"extra":true`), 1),
		"empty":     {},
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, mut, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(ViewPath(path), view, 0o600); err != nil {
				t.Fatal(err)
			}
			if v, _, err := LoadView(path); err == nil || v != nil {
				t.Fatalf("corrupt index served a view: %v", err)
			}
		})
	}
	if _, _, err := LoadView(filepath.Join(dir, "missing.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing index: %v", err)
	}
}

func TestKindNamesSorted(t *testing.T) {
	if !sort.StringsAreSorted(kindNames) || len(kindNames) != 2 || kindsAll != 1<<len(kindNames)-1 {
		t.Fatalf("kindNames %v must be sorted and match kindsAll", kindNames)
	}
	for _, k := range []string{KindIPEDS, KindIRSBMF} {
		if kindBit(k) == 0 {
			t.Fatalf("kind %s has no bit", k)
		}
	}
}

// copyFile copies a real index into a test directory, so deriving its view
// never writes into the state directory it came from.
func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestRealIndexParity checks the view against the reference on every name
// of a real index. Set VANI_EMPLOYER_REAL_INDEX to the index.json path; it is
// copied first and the view derived from the copy.
func TestRealIndexParity(t *testing.T) {
	src := os.Getenv("VANI_EMPLOYER_REAL_INDEX")
	if src == "" {
		t.Skip("set VANI_EMPLOYER_REAL_INDEX to an index.json to run")
	}
	path := filepath.Join(t.TempDir(), "index.json")
	copyFile(t, src, path)
	full, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	ref := newRef(full.sources)
	full.sources = nil // the reference holds what the comparison needs
	// The copy has no view: the first load derives and writes it, and the
	// second must read that file back, which is what a run does.
	if _, info, err := LoadView(path); err != nil || !info.Rebuilt || info.WriteErr != nil {
		t.Fatalf("deriving the view: %v %+v", err, info)
	}
	v, info, err := LoadView(path)
	if err != nil || info.Rebuilt {
		t.Fatalf("reading the view back: %v %+v", err, info)
	}
	t.Logf("view read from file; %d names", v.Len())
	qs := make([]string, 0, 2*len(ref.keys))
	seen := map[string]bool{}
	for _, k := range ref.keys {
		qs = append(qs, k)
		if f := strings.Fields(k); len(f) > 2 {
			if p := strings.Join(f[:2], " "); !seen[p] {
				seen[p] = true
				qs = append(qs, p)
			}
		}
	}
	assertParity(t, ref, qs, map[string]func(string) Badge{"LoadView": v.Lookup})
	t.Logf("%s", fmt.Sprintf("%d queries agree", len(qs)))
}
