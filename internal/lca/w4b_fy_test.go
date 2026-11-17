package lca

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func w4bRows(n int, fyDate, employer string) string {
	var b strings.Builder
	b.WriteString(hdr)
	for i := 0; i < n; i++ {
		b.WriteString("A,Certified," + fyDate + "," + fyDate + ",H-1B," + employer + "\n")
	}
	return b.String()
}

func w4bImport(t *testing.T, path, csv, name string) []SourceMeta {
	t.Helper()
	p, err := Import(strings.NewReader(csv), name)
	if err != nil {
		t.Fatal(err)
	}
	_, replaced, err := Update(path, p)
	if err != nil {
		t.Fatal(err)
	}
	return replaced
}

func w4bTotal(t *testing.T, path, company string) int {
	t.Helper()
	x, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return x.merged[Normalize(company)][2024].H1B
}

// A corrected file for the same fiscal year replaces the first one: before,
// both stayed as sources and the year's counts were summed.
func TestW4bCorrectedFileReplacesItsFiscalYear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	w4bImport(t, path, w4bRows(5, "2024-03-01", "Acme Inc"), "fy2024-q4.csv")
	if got := w4bTotal(t, path, "Acme"); got != 5 {
		t.Fatalf("first import: %d, want 5", got)
	}
	replaced := w4bImport(t, path, w4bRows(7, "2024-03-01", "Acme Inc"), "fy2024-q4-corrected.csv")
	if got := w4bTotal(t, path, "Acme"); got != 7 {
		t.Fatalf("after the corrected file: %d, want 7 (not 12)", got)
	}
	if len(replaced) != 1 || replaced[0].Name != "fy2024-q4.csv" {
		t.Fatalf("replaced = %+v, want the first file", replaced)
	}
	x, _ := Load(path)
	if n := len(x.Sources()); n != 1 {
		t.Fatalf("%d sources, want 1", n)
	}
}

// Another fiscal year is another source; an identical re-import changes nothing.
func TestW4bOtherYearsAndIdenticalReimportAreKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	fy24 := w4bRows(3, "2024-03-01", "Acme Inc")
	fy23 := w4bRows(2, "2023-03-01", "Acme Inc")
	w4bImport(t, path, fy24, "a.csv")
	w4bImport(t, path, fy23, "b.csv")
	first, _ := os.ReadFile(path)
	if r := w4bImport(t, path, fy24, "a.csv"); len(r) != 0 {
		t.Fatalf("re-importing the same file replaced %+v", r)
	}
	again, _ := os.ReadFile(path)
	if string(first) != string(again) {
		t.Fatal("an unchanged re-import changed the index bytes")
	}
	x, _ := Load(path)
	if len(x.Sources()) != 2 {
		t.Fatalf("%d sources, want 2", len(x.Sources()))
	}
}

// A few rows of a neighbouring year inside a file must not wipe that year's own
// file: only the file's main year counts.
func TestW4bSpillRowsDoNotReplaceTheNeighbouringYear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	w4bImport(t, path, w4bRows(4, "2023-03-01", "Beta Inc"), "fy2023.csv")
	spill := w4bRows(10, "2024-03-01", "Acme Inc") + "A,Certified,2023-03-01,2023-03-01,H-1B,Acme Inc\n"
	if r := w4bImport(t, path, spill, "fy2024.csv"); len(r) != 0 {
		t.Fatalf("a file mostly FY2024 replaced %+v", r)
	}
	x, _ := Load(path)
	if len(x.Sources()) != 2 || x.merged["BETA"][2023].H1B != 4 {
		t.Fatalf("FY2023 file lost: %d sources, BETA %+v", len(x.Sources()), x.merged["BETA"])
	}
}

// Merge keeps adding sources; only the import path replaces by year, so
// callers that build an index by hand are unchanged.
func TestW4bMergeStaysAdditive(t *testing.T) {
	x := NewIndex()
	for _, sha := range []string{"a", "b"} {
		if err := x.Merge(Partial{Source: SourceMeta{Name: sha, SHA256: sha}, Employers: map[string]map[int]Counts{"ACME": {2024: {H1B: 1}}}}); err != nil {
			t.Fatal(err)
		}
	}
	if got := x.merged["ACME"][2024].H1B; got != 2 {
		t.Fatalf("Merge total = %d, want 2", got)
	}
}

// An index written by the binary before this change may hold two sources for
// one fiscal year: it still loads, and the next import for that year tidies it.
func TestW4bLegacyIndexWithTwoSourcesOfOneYear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	old := NewIndex()
	for _, sha := range []string{"a", "b"} {
		if err := old.Merge(Partial{Source: SourceMeta{Name: sha, SHA256: sha}, Employers: map[string]map[int]Counts{"ACME": {2024: {H1B: 1}}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Save(path); err != nil {
		t.Fatal(err)
	}
	if got := w4bTotal(t, path, "Acme"); got != 2 {
		t.Fatalf("legacy load: %d, want 2", got)
	}
	replaced := w4bImport(t, path, w4bRows(3, "2024-03-01", "Acme Inc"), "fixed.csv")
	if len(replaced) != 2 || w4bTotal(t, path, "Acme") != 3 {
		t.Fatalf("replaced %d, total %d, want 2 and 3", len(replaced), w4bTotal(t, path, "Acme"))
	}
}

func TestW4bPrimaryFY(t *testing.T) {
	cases := map[string]struct {
		emps map[string]map[int]Counts
		want int
	}{
		"empty":   {nil, 0},
		"zeroes":  {map[string]map[int]Counts{"A": {2024: {}}}, 0},
		"one":     {map[string]map[int]Counts{"A": {2024: {H1B: 1}}}, 2024},
		"tie low": {map[string]map[int]Counts{"A": {2024: {H1B: 1}, 2023: {E3: 1}}}, 2023},
		"summed":  {map[string]map[int]Counts{"A": {2024: {H1B: 1}}, "B": {2024: {H1B: 1}, 2023: {H1B: 1}}}, 2024},
	}
	for name, c := range cases {
		if got := PrimaryFY(Partial{Employers: c.emps}); got != c.want {
			t.Errorf("%s: PrimaryFY = %d, want %d", name, got, c.want)
		}
	}
}

// Golden: the bytes Save writes for a fixed index. The index format is
// unchanged, so an index imported before this change reads and rewrites to the
// same bytes.
func TestW4bIndexBytesGolden(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	p := Partial{
		Source:    SourceMeta{Name: "g.csv", SHA256: "abc", RowsRead: 3, RowsCounted: 2, Skipped: map[string]int{SkipStatus: 1}},
		Employers: map[string]map[int]Counts{"ACME": {2024: {H1B: 2}}, "BETA": {2023: {E3: 1}}},
	}
	if _, _, err := Update(path, p); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	const want = `{"version":1,"checksum":"c8b20e112818fe52fbac954c3802322ea38223706f0f2835d016d14d7ced895d","sources":[{"name":"g.csv","sha256":"abc","rows_read":3,"rows_counted":2,"skipped":{"status_not_certified":1},"employers":{"ACME":{"2024":[2,0,0,0]},"BETA":{"2023":[0,0,0,1]}}}]}`
	if string(got) != want {
		t.Fatalf("index bytes:\n%s", got)
	}
}
