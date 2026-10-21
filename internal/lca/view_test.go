package lca

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/indexutil"
)

// saveIndex merges the sources (each a name -> fy -> H-1B map) into an index
// and saves it, returning the index path.
func saveIndex(t testing.TB, dir string, sources ...map[string]map[int]int) string {
	t.Helper()
	x := NewIndex()
	for i, s := range sources {
		emps := map[string]map[int]Counts{}
		for name, byFY := range s {
			emps[name] = map[int]Counts{}
			for fy, n := range byFY {
				emps[name][fy] = Counts{H1B: n, H1BWithdrawn: 1, H1B1: 2, E3: 3}
			}
		}
		if err := x.Merge(Partial{Source: SourceMeta{Name: "t.csv", SHA256: fmt.Sprintf("%064d", i)}, Employers: emps}); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "index.json")
	if err := x.Save(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func fixtureSources() []map[string]map[int]int {
	return []map[string]map[int]int{
		{
			"GOOGLE":               {2022: 1, 2023: 10, 2024: 20, 2025: 30},
			"GOOGLE CLOUD":         {2024: 5},
			"ACME":                 {2023: 3},
			"ACME WIDGETS":         {2025: 7},
			"WALT DISNEY COMPANY":  {2023: 1, 2025: 2},
			"MCDONALDS":            {2024: 9},
			"ZÜRICH INSURANCE":     {2023: 4, 2024: 4},
			"日本 電気":                {2025: 8},
			"JOHNSON AND JOHNSON":  {2023: 100},
			"JOHNSON AND SOMEONE":  {2024: 1},
			"SMITH AND JONES":      {2022: 2},
			"ONLYONE TOKEN CO":     {2025: 1},
			"UNIQUE PREFIX HOLDER": {2025: 12},
			"A":                    {2025: 1},
		},
		{ // the same employers again from a second file: counts add
			"GOOGLE":       {2023: 5, 2025: 5},
			"ACME":         {2023: 1, 2024: 1},
			"NEW EMPLOYER": {2025: 6},
		},
	}
}

var fixtureQueries = []string{
	// exact and normalised variants
	"Google LLC", "GOOGLE, L.L.C.", "google", "The Google Inc", "  Google   ",
	"Acme Inc", "ACME", "Walt Disney Company", "The Walt Disney Company",
	"McDonald's Corp", "Zürich Insurance PBC", "zürich insurance", "ZÜRICH INSURANCE",
	"日本 電気", "日本　電気 株式会社", "Johnson & Johnson", "Smith & Jones, LLP",
	// unique prefix (>= 2 tokens) and ambiguous prefixes
	"Unique Prefix", "Walt Disney", "Onlyone Token", "Johnson And", "Google Cloud Platform",
	// single-token prefixes must not match
	"Unique", "Walt", "Zürich", "Smith", "Onlyone",
	// misses
	"", "   ", "Inc", "LLC, Inc.", "The", "!!!", "Nonexistent Corp", "Googl", "Google Cloud Extra Long Name Here",
	"A", "A Inc", "New Employer", "NEW", "Acme Widgets Ltd", "Acme Wid",
}

func TestViewLookupParityFixture(t *testing.T) {
	path := saveIndex(t, t.TempDir(), fixtureSources()...)
	x, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	v, info, err := LoadView(path)
	if err != nil || !info.Rebuilt || info.WriteErr != nil {
		t.Fatalf("first LoadView: %+v %v", info, err)
	}
	checkParity(t, x, v, fixtureQueries)
	// And again from the file the first call wrote.
	v2, info, err := LoadView(path)
	if err != nil || info.Rebuilt {
		t.Fatalf("second LoadView: %+v %v", info, err)
	}
	checkParity(t, x, v2, fixtureQueries)
	if v2.Len() != x.Employers() {
		t.Errorf("view has %d employers, index %d", v2.Len(), x.Employers())
	}
	if want := lookupWindow(x.Coverage()); !reflect.DeepEqual(v2.Coverage(), want) {
		t.Errorf("view coverage %v, want %v", v2.Coverage(), want)
	}
	// Multi-FY: the window is the last three covered years, and merged counts add.
	b := v2.Lookup("Google")
	if want := map[int]int{2023: 15, 2024: 20, 2025: 35}; !reflect.DeepEqual(b.Certified, want) || b.Total != 70 {
		t.Errorf("Google badge %+v, want certified %v total 70", b, want)
	}
}

func checkParity(t *testing.T, x *Index, v *View, queries []string) {
	t.Helper()
	for _, q := range queries {
		want, got := x.Lookup(q), v.Lookup(q)
		if !reflect.DeepEqual(want, got) {
			t.Errorf("Lookup(%q): index %+v, view %+v", q, want, got)
		}
		if want.String() != got.String() {
			t.Errorf("Lookup(%q).String(): index %q, view %q", q, want.String(), got.String())
		}
	}
}

// TestViewLookupParityRandomized compares the view against Load on random
// indexes and random queries. The seed is fixed, so a failure replays.
func TestViewLookupParityRandomized(t *testing.T) {
	r := rand.New(rand.NewPCG(20261021, 3))
	vocab := []string{"ALPHA", "BETA", "GAMMA", "DELTA", "OMEGA", "NORTH", "SOUTH", "ÉCOLE", "東京", "AND", "SERVICES", "GROUP", "9", "X"}
	suffix := []string{"", " INC", " LLC", " CORP", " L L C", ", Ltd.", " PBC"}
	for round := 0; round < 40; round++ {
		var sources []map[string]map[int]int
		var names []string
		for s := 0; s < 1+r.IntN(3); s++ {
			src := map[string]map[int]int{}
			for i := r.IntN(60); i > 0; i-- {
				toks := make([]string, 1+r.IntN(4))
				for j := range toks {
					toks[j] = vocab[r.IntN(len(vocab))]
				}
				name := Normalize(strings.Join(toks, " "))
				if name == "" {
					continue
				}
				src[name] = map[int]int{}
				for k := r.IntN(5); k >= 0; k-- {
					src[name][2020+r.IntN(6)] = r.IntN(1000)
				}
				names = append(names, name)
			}
			sources = append(sources, src)
		}
		path := saveIndex(t, t.TempDir(), sources...)
		x, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		v, _, err := LoadView(path)
		if err != nil {
			t.Fatal(err)
		}
		queries := []string{"", "Alpha", "NORTH SOUTH"}
		for i := 0; i < 200; i++ {
			var q string
			switch {
			case len(names) > 0 && r.IntN(3) == 0:
				q = names[r.IntN(len(names))] + suffix[r.IntN(len(suffix))]
			case len(names) > 0 && r.IntN(2) == 0: // a token prefix of an existing name
				f := strings.Fields(names[r.IntN(len(names))])
				q = strings.Join(f[:1+r.IntN(len(f))], " ")
			default:
				toks := make([]string, r.IntN(4))
				for j := range toks {
					toks[j] = vocab[r.IntN(len(vocab))]
				}
				q = strings.Join(toks, " ")
			}
			queries = append(queries, q)
		}
		checkParity(t, x, v, queries)
	}
}

func TestViewEmptyIndex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	if err := NewIndex().Save(path); err != nil {
		t.Fatal(err)
	}
	v, info, err := LoadView(path)
	if err != nil || !info.Rebuilt {
		t.Fatalf("%+v %v", info, err)
	}
	v, info, err = LoadView(path)
	if err != nil || info.Rebuilt || v.Len() != 0 {
		t.Fatalf("second load: %+v %v", info, err)
	}
	x, _ := Load(path)
	checkParity(t, x, v, fixtureQueries)
	if b := v.Lookup("Google"); b.Confidence != ConfNone || len(b.FYs) != 0 {
		t.Errorf("empty view badge %+v", b)
	}
}

func TestViewMissingIndexIsNotExist(t *testing.T) {
	_, _, err := LoadView(filepath.Join(t.TempDir(), "index.json"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestViewMissingFileRebuilds(t *testing.T) {
	path := saveIndex(t, t.TempDir(), fixtureSources()...)
	if _, _, err := LoadView(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ViewPath(path)); err != nil {
		t.Fatal(err)
	}
	v, info, err := LoadView(path)
	if err != nil || !info.Rebuilt || !errors.Is(info.Reason, fs.ErrNotExist) || !info.Routine() {
		t.Fatalf("%+v %v", info, err)
	}
	x, _ := Load(path)
	checkParity(t, x, v, fixtureQueries)
	if _, err := os.Stat(ViewPath(path)); err != nil {
		t.Errorf("view not written back: %v", err)
	}
}

// A view derived from one index must never answer for another, even when the
// replacement index is byte-for-byte the same size.
func TestViewFromDifferentIndexIsNotUsed(t *testing.T) {
	dir := t.TempDir()
	pathA := saveIndex(t, dir, map[string]map[int]int{"ALPHA": {2025: 111}})
	if _, _, err := LoadView(pathA); err != nil {
		t.Fatal(err)
	}
	oldView, err := os.ReadFile(ViewPath(pathA))
	if err != nil {
		t.Fatal(err)
	}
	pathB := saveIndex(t, dir, map[string]map[int]int{"ALPHA": {2025: 222}}) // same size, other bytes
	if err := os.WriteFile(ViewPath(pathB), oldView, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rv(t, ViewPath(pathB), pathB); !errors.Is(err, ErrStaleView) {
		t.Fatalf("readView of a foreign view: %v, want ErrStaleView", err)
	}
	v, info, err := LoadView(pathB)
	if err != nil || !info.Rebuilt || !errors.Is(info.Reason, ErrStaleView) || !info.Routine() {
		t.Fatalf("%+v %v", info, err)
	}
	if got := v.Lookup("Alpha").Total; got != 222 {
		t.Fatalf("served %d, want 222 from the current index", got)
	}
	// The rebuilt view is now bound to the current index.
	if v, info, _ = LoadView(pathB); info.Rebuilt || v.Lookup("Alpha").Total != 222 {
		t.Fatalf("rebuilt view not reused: %+v", info)
	}
}

// rv reads the view at viewPath as bound to the index at indexPath.
func rv(t *testing.T, viewPath, indexPath string) (*View, error) {
	t.Helper()
	sum, size := mustSum(t, indexPath)
	return readView(viewPath, sum, size)
}

func mustSum(t *testing.T, path string) (string, int64) {
	t.Helper()
	s, n, err := hashIndexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return s, n
}

// splitView returns the header and body of a view file.
func splitView(t *testing.T, path string) (viewHeader, []byte, []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line, body, ok := bytes.Cut(data, []byte{'\n'})
	if !ok {
		t.Fatal("no header line")
	}
	var h viewHeader
	if err := json.Unmarshal(line, &h); err != nil {
		t.Fatal(err)
	}
	return h, line, body
}

func joinView(t *testing.T, h viewHeader, body []byte) []byte {
	t.Helper()
	line, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	return append(append(line, '\n'), body...)
}

// every refused view must fall back to a correct rebuild, with a non-routine
// reason, never a panic and never wrong data.
func expectRefusedThenRebuilt(t *testing.T, path string, wantTotal int) {
	t.Helper()
	sum, size := mustSum(t, path)
	if _, err := readView(ViewPath(path), sum, size); err == nil {
		t.Fatal("readView accepted a bad view")
	}
	v, info, err := LoadView(path)
	if err != nil || !info.Rebuilt {
		t.Fatalf("LoadView: %+v %v", info, err)
	}
	if got := v.Lookup("Alpha").Total; got != wantTotal {
		t.Fatalf("fallback served %d, want %d", got, wantTotal)
	}
	if _, err := readView(ViewPath(path), sum, size); err != nil {
		t.Fatalf("view not repaired: %v", err)
	}
}

func TestViewTruncatedAtEveryHeaderBoundary(t *testing.T) {
	path := saveIndex(t, t.TempDir(), map[string]map[int]int{"ALPHA": {2024: 4, 2025: 6}, "BETA GAMMA": {2025: 1}})
	if _, _, err := LoadView(path); err != nil {
		t.Fatal(err)
	}
	full, err := os.ReadFile(ViewPath(path))
	if err != nil {
		t.Fatal(err)
	}
	hdrEnd := bytes.IndexByte(full, '\n') + 1
	// every byte of the header, the newline, and every byte of the body
	for cut := 0; cut < len(full); cut++ {
		if err := os.WriteFile(ViewPath(path), full[:cut], 0o600); err != nil {
			t.Fatal(err)
		}
		sum, size := mustSum(t, path)
		if _, err := readView(ViewPath(path), sum, size); err == nil {
			t.Fatalf("truncation at %d of %d (header ends at %d) was accepted", cut, len(full), hdrEnd)
		}
	}
	// and the fall-back from a truncated view
	if err := os.WriteFile(ViewPath(path), full[:hdrEnd+3], 0o600); err != nil {
		t.Fatal(err)
	}
	expectRefusedThenRebuilt(t, path, 10)
}

func TestViewLengthFieldsBeyondFileSize(t *testing.T) {
	path := saveIndex(t, t.TempDir(), map[string]map[int]int{"ALPHA": {2024: 4, 2025: 6}, "BETA GAMMA": {2025: 1}})
	if _, _, err := LoadView(path); err != nil {
		t.Fatal(err)
	}
	h0, _, body := splitView(t, ViewPath(path))
	mutations := map[string]func(h *viewHeader){
		"employers huge":      func(h *viewHeader) { h.Employers = 1 << 40 },
		"employers max int":   func(h *viewHeader) { h.Employers = int(^uint(0) >> 1) },
		"employers negative":  func(h *viewHeader) { h.Employers = -1 },
		"employers one more":  func(h *viewHeader) { h.Employers++ },
		"body bytes huge":     func(h *viewHeader) { h.BodyBytes = 1 << 62 },
		"body bytes past 1G":  func(h *viewHeader) { h.BodyBytes = maxIndexFileBytes + 1 },
		"body bytes negative": func(h *viewHeader) { h.BodyBytes = -5 },
		"body bytes one more": func(h *viewHeader) { h.BodyBytes++ },
		"body bytes one less": func(h *viewHeader) { h.BodyBytes-- },
		"too many years":      func(h *viewHeader) { h.FYs = []int{2021, 2022, 2023, 2024} },
		"unsorted years":      func(h *viewHeader) { h.FYs = []int{2025, 2024} },
		"year out of range":   func(h *viewHeader) { h.FYs = []int{1, 2025} },
		"wrong format":        func(h *viewHeader) { h.Format = "vani-employer-view" },
		"future version":      func(h *viewHeader) { h.Version = ViewVersion + 1 },
	}
	for name, mut := range mutations {
		t.Run(name, func(t *testing.T) {
			h := h0
			h.FYs = append([]int(nil), h0.FYs...)
			mut(&h)
			if err := os.WriteFile(ViewPath(path), joinView(t, h, body), 0o600); err != nil {
				t.Fatal(err)
			}
			expectRefusedThenRebuilt(t, path, 10)
		})
	}
}

func TestViewCorruptBodyAndHeader(t *testing.T) {
	path := saveIndex(t, t.TempDir(), map[string]map[int]int{"ALPHA": {2024: 4, 2025: 6}, "BETA GAMMA": {2025: 1}})
	if _, _, err := LoadView(path); err != nil {
		t.Fatal(err)
	}
	full, _ := os.ReadFile(ViewPath(path))
	hdrEnd := bytes.IndexByte(full, '\n') + 1
	flip := func(i int) []byte { c := append([]byte(nil), full...); c[i] ^= 0x40; return c }
	cases := map[string][]byte{
		"body byte flipped":    flip(len(full) - 1),
		"offset byte flipped":  flip(hdrEnd),
		"header byte flipped":  flip(5),
		"garbage":              []byte("not a view\n\x00\x01"),
		"no newline":           bytes.Repeat([]byte("x"), 3*maxViewHeaderBytes),
		"empty file":           nil,
		"trailing byte":        append(append([]byte(nil), full...), 0),
		"huge header line":     append(bytes.Repeat([]byte("{"), 2*maxViewHeaderBytes), '\n'),
		"unknown header field": []byte(`{"format":"vani-lca-view","extra":1}` + "\n"),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(ViewPath(path), data, 0o600); err != nil {
				t.Fatal(err)
			}
			expectRefusedThenRebuilt(t, path, 10)
		})
	}
}

// A body whose checksum was recomputed over bad offsets is still refused by
// validate: the checksum guards the file, validate guards the invariants.
func TestViewValidateRefusesBadStructure(t *testing.T) {
	path := saveIndex(t, t.TempDir(), map[string]map[int]int{"ALPHA": {2025: 6}, "BRAVO": {2025: 1}})
	if _, _, err := LoadView(path); err != nil {
		t.Fatal(err)
	}
	h, _, body := splitView(t, ViewPath(path))
	// body: 3 offsets (12) | 2 counts (8) | names; swap the names so they are unsorted
	bad := append([]byte(nil), body...)
	copy(bad[len(bad)-len("ALPHABRAVO"):], "BRAVOALPHA")
	if bytes.Equal(bad, body) {
		t.Fatal("mutation did not change the body")
	}
	h.BodyBytes = int64(len(bad))
	h.BodySHA256 = sumHex(bad)
	if err := os.WriteFile(ViewPath(path), joinView(t, h, bad), 0o600); err != nil {
		t.Fatal(err)
	}
	sum, size := mustSum(t, path)
	if _, err := readView(ViewPath(path), sum, size); err == nil || !strings.Contains(err.Error(), "not strictly sorted") {
		t.Fatalf("readView: %v, want a sort-order refusal", err)
	}
}

func sumHex(b []byte) string { return indexutil.SumHex(b) }

func TestViewDuplicateEmployersAcrossSourcesMergeOnce(t *testing.T) {
	path := saveIndex(t, t.TempDir(),
		map[string]map[int]int{"DUP": {2025: 1}},
		map[string]map[int]int{"DUP": {2025: 2}},
		map[string]map[int]int{"DUP": {2025: 3}})
	v, _, err := LoadView(path)
	if err != nil {
		t.Fatal(err)
	}
	if v.Len() != 1 || v.Lookup("Dup Inc").Total != 6 {
		t.Fatalf("len %d, badge %+v; want one employer totalling 6", v.Len(), v.Lookup("Dup Inc"))
	}
}

func TestViewUnicodeEmployers(t *testing.T) {
	src := map[string]map[int]int{}
	for _, n := range []string{"ÉCOLE POLYTECHNIQUE", "東京 大学", "İSTANBUL TEKNIK", "MÜNCHEN RÜCK", "Ω OMEGA", "ÅLAND"} {
		src[Normalize(n)] = map[int]int{2025: len(n)}
	}
	path := saveIndex(t, t.TempDir(), src)
	x, _ := Load(path)
	v, _, err := LoadView(path)
	if err != nil {
		t.Fatal(err)
	}
	queries := []string{"École Polytechnique", "école", "東京 大学 Inc", "東京", "İstanbul Teknik", "München Rück GmbH", "ω omega", "Åland"}
	checkParity(t, x, v, queries)
	if v.Lookup("École Polytechnique").Confidence != ConfExact {
		t.Errorf("unicode exact match lost: %+v", v.Lookup("École Polytechnique"))
	}
}

func TestViewCountTooLargeIsAnError(t *testing.T) {
	x := NewIndex()
	if err := x.Merge(Partial{Source: SourceMeta{SHA256: "a"}, Employers: map[string]map[int]Counts{"BIG": {2025: {H1B: 1 << 33}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := buildView(x); err == nil {
		t.Fatal("a count beyond uint32 was silently truncated")
	}
}

func TestViewCorruptIndexIsRefusedEvenWithAView(t *testing.T) {
	path := saveIndex(t, t.TempDir(), map[string]map[int]int{"ALPHA": {2025: 6}})
	if _, _, err := LoadView(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	data[len(data)-5] ^= 1 // a counts digit: the index checksum no longer matches
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadView(path); err == nil || !strings.Contains(err.Error(), "corrupt index") {
		t.Fatalf("LoadView of a corrupt index: %v", err)
	}
}

// Readers racing a rebuild see the old view or the new one, never a partial
// file: atomic rename. Every read below must succeed and answer from one of the
// two indexes.
func TestViewConcurrentRebuildNeverPartial(t *testing.T) {
	dir := t.TempDir()
	path := saveIndex(t, dir, map[string]map[int]int{"ALPHA": {2025: 111}, "BETA GAMMA": {2025: 5}})
	if _, _, err := LoadView(path); err != nil {
		t.Fatal(err)
	}
	vp := ViewPath(path)
	sum, size := mustSum(t, path)
	a, err := readView(vp, sum, size)
	if err != nil {
		t.Fatal(err)
	}
	// a second, different view bound to the same index bytes, written over and
	// over while readers read.
	big := map[string]map[int]int{}
	for i := 0; i < 2000; i++ {
		big[fmt.Sprintf("EMPLOYER %05d", i)] = map[int]int{2025: i}
	}
	otherPath := saveIndex(t, t.TempDir(), big)
	if _, _, err := LoadView(otherPath); err != nil {
		t.Fatal(err)
	}
	b, err := rv(t, ViewPath(otherPath), otherPath)
	if err != nil {
		t.Fatal(err)
	}
	var writers, readers sync.WaitGroup
	stop := make(chan struct{})
	for w := 0; w < 2; w++ {
		writers.Add(1)
		go func(w int) {
			defer writers.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				v := a
				if (i+w)%2 == 0 {
					v = b
				}
				if err := writeView(vp, v, sum, size); err != nil {
					t.Errorf("writeView: %v", err)
					return
				}
			}
		}(w)
	}
	for r := 0; r < 4; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for i := 0; i < 200; i++ {
				v, err := readView(vp, sum, size)
				if err != nil {
					t.Errorf("reader saw a refused view: %v", err)
					return
				}
				if n := v.Len(); n != 2 && n != 2000 {
					t.Errorf("reader saw %d employers, want the old (2) or the new (2000) view", n)
					return
				}
			}
		}()
	}
	readers.Wait()
	close(stop)
	writers.Wait()
}

// Two LoadViews with no view file both rebuild and write; both answer
// correctly and the file left behind is intact.
func TestViewConcurrentLoadViewRebuilds(t *testing.T) {
	path := saveIndex(t, t.TempDir(), fixtureSources()...)
	x, _ := Load(path)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, info, err := LoadView(path)
			if err != nil || info.WriteErr != nil {
				t.Errorf("LoadView: %+v %v", info, err)
				return
			}
			for _, q := range fixtureQueries {
				if !reflect.DeepEqual(x.Lookup(q), v.Lookup(q)) {
					t.Errorf("Lookup(%q) differs under concurrent rebuild", q)
					return
				}
			}
		}()
	}
	wg.Wait()
	if _, info, err := LoadView(path); err != nil || info.Rebuilt {
		t.Fatalf("final view not intact: %+v %v", info, err)
	}
	if ents, _ := os.ReadDir(filepath.Dir(path)); len(ents) != 2 {
		t.Errorf("temp files left behind: %v", ents)
	}
}

func TestViewIsALooker(t *testing.T) {
	var _ Looker = (*Index)(nil)
	var _ Looker = (*View)(nil)
}
