package lca

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const hdr = "CASE_NUMBER,CASE_STATUS,RECEIVED_DATE,DECISION_DATE,VISA_CLASS,EMPLOYER_NAME\n"

func imp(t *testing.T, csv string) Partial {
	t.Helper()
	p, err := Import(strings.NewReader(csv), "t.csv")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	return p
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"Google LLC":              "GOOGLE",
		"GOOGLE, L.L.C.":          "GOOGLE",
		"Google L L C":            "GOOGLE",
		"The Walt Disney Company": "WALT DISNEY COMPANY",
		"Co-Op Inc":               "CO OP",
		"Acme Co., Inc.":          "ACME",
		"Acme Corporation Ltd":    "ACME",
		"McDonald's Corp":         "MCDONALDS",
		"Smith & Jones, LLP":      "SMITH AND JONES",
		"Foo Bar P.L.L.C.":        "FOO BAR",
		"  spaced   out  GmbH ":   "SPACED OUT",
		"Inc":                     "",
		"LLC, Inc.":               "",
		"The":                     "",
		"":                        "",
		"!!!":                     "",
		"Zürich Insurance PBC":    "ZÜRICH INSURANCE",
		"Corp Services Corp":      "CORP SERVICES",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHeaderAliasesAndOrderIndependence(t *testing.T) {
	csv := "employer_business_name , Status,VISA CLASS,extra,lca_case_submit\n" +
		"Acme Inc,Certified,H-1B,x,2024-10-01\n"
	p := imp(t, csv)
	if got := p.Employers["ACME"][2025].H1B; got != 1 {
		t.Fatalf("ACME FY2025 = %d, want 1 (employers=%v)", got, p.Employers)
	}
}

func TestDecisionDateFallsBackToReceived(t *testing.T) {
	p := imp(t, hdr+"1,Certified,2024-02-01,,H-1B,Acme Inc\n")
	if p.Employers["ACME"][2024].H1B != 1 {
		t.Fatalf("expected fallback to received date: %v", p.Employers)
	}
}

func TestMissingRequiredColumns(t *testing.T) {
	for name, csv := range map[string]string{
		"no employer": "CASE_STATUS,VISA_CLASS,DECISION_DATE\nCertified,H-1B,2024-01-01\n",
		"no status":   "EMPLOYER_NAME,VISA_CLASS,DECISION_DATE\nA,H-1B,2024-01-01\n",
		"no visa":     "EMPLOYER_NAME,CASE_STATUS,DECISION_DATE\nA,Certified,2024-01-01\n",
		"no date":     "EMPLOYER_NAME,CASE_STATUS,VISA_CLASS\nA,Certified,H-1B\n",
		"empty":       "",
	} {
		if _, err := Import(strings.NewReader(csv), "x"); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	_, err := Import(strings.NewReader("EMPLOYER_NAME,CASE_STATUS\n"), "x")
	if err == nil || !strings.Contains(err.Error(), "VISA_CLASS") || !strings.Contains(err.Error(), "DECISION_DATE") {
		t.Errorf("error should name missing columns, got %v", err)
	}
}

func TestBOM(t *testing.T) {
	p := imp(t, "\xEF\xBB\xBF"+hdr+"1,Certified,2024-01-01,2024-01-05,H-1B,Acme Inc\n")
	if p.Employers["ACME"][2024].H1B != 1 {
		t.Fatalf("BOM header not recognised: %v", p.Employers)
	}
}

func TestRaggedRowsSkipped(t *testing.T) {
	csv := hdr +
		"1,Certified,2024-01-01,2024-01-05,H-1B,Acme Inc\n" +
		"2,Certified,2024-01-01,2024-01-05,H-1B\n" +
		"3,Certified,2024-01-01,2024-01-05,H-1B,Acme Inc,extra\n"
	p := imp(t, csv)
	if p.Source.RowsRead != 3 || p.Source.RowsCounted != 1 || p.Source.Skipped[SkipRagged] != 2 {
		t.Fatalf("meta = %+v", p.Source)
	}
}

func TestStatusAndVisaFilters(t *testing.T) {
	csv := hdr +
		"1,Certified,2024-01-01,2024-03-01,H-1B,Acme Inc\n" +
		"2,certified-withdrawn,2024-01-01,2024-03-01,H-1B,Acme Inc\n" +
		"3,Certified - Withdrawn,2024-01-01,2024-03-01,H-1B,Acme Inc\n" +
		"4,Denied,2024-01-01,2024-03-01,H-1B,Acme Inc\n" +
		"5,Withdrawn,2024-01-01,2024-03-01,H-1B,Acme Inc\n" +
		"6,Certified,2024-01-01,2024-03-01,H-1B1 Chile,Acme Inc\n" +
		"7,Certified,2024-01-01,2024-03-01,H-1B1 Singapore,Acme Inc\n" +
		"8,Certified,2024-01-01,2024-03-01,E-3 Australian,Acme Inc\n" +
		"9,Certified,2024-01-01,2024-03-01,Green Card,Acme Inc\n" +
		"10,Certified-Withdrawn,2024-01-01,2024-03-01,E-3 Australian,Acme Inc\n" +
		"11,Certified,,not-a-date,H-1B,Acme Inc\n" +
		"12,Certified,2024-01-01,2024-03-01,H-1B,Inc.\n"
	p := imp(t, csv)
	if got, want := p.Employers["ACME"][2024], (Counts{H1B: 1, H1BWithdrawn: 2, H1B1: 2, E3: 1}); got != want {
		t.Errorf("counts = %+v, want %+v", got, want)
	}
	wantSkip := map[string]int{SkipStatus: 2, SkipVisa: 1, SkipWithdrawnAlt: 1, SkipDate: 1, SkipEmployer: 1}
	if !reflect.DeepEqual(p.Source.Skipped, wantSkip) {
		t.Errorf("skipped = %v, want %v", p.Source.Skipped, wantSkip)
	}
	if p.Source.RowsRead != 12 || p.Source.RowsCounted != 6 {
		t.Errorf("meta = %+v", p.Source)
	}
}

func TestFiscalYearBoundary(t *testing.T) {
	csv := hdr +
		"1,Certified,2024-01-01,2024-09-30,H-1B,Acme Inc\n" +
		"2,Certified,2024-01-01,2024-10-01,H-1B,Acme Inc\n" +
		"3,Certified,2024-01-01,10/1/2024 0:00,H-1B,Acme Inc\n" +
		"4,Certified,2024-01-01,9/30/2024,H-1B,Acme Inc\n" +
		"5,Certified,2024-01-01,2024-12-31 00:00:00,H-1B,Acme Inc\n"
	p := imp(t, csv)
	if got := p.Employers["ACME"]; got[2024].H1B != 2 || got[2025].H1B != 3 {
		t.Fatalf("FY split = %v", got)
	}
}

func TestSourceHashAndMeta(t *testing.T) {
	a := imp(t, hdr+"1,Certified,2024-01-01,2024-01-05,H-1B,Acme Inc\n")
	b := imp(t, hdr+"1,Certified,2024-01-01,2024-01-05,H-1B,Acme Inc\n")
	c := imp(t, hdr+"1,Certified,2024-01-01,2024-01-06,H-1B,Acme Inc\n")
	if len(a.Source.SHA256) != 64 || a.Source.SHA256 != b.Source.SHA256 || a.Source.SHA256 == c.Source.SHA256 {
		t.Fatalf("sha: %s %s %s", a.Source.SHA256, b.Source.SHA256, c.Source.SHA256)
	}
	if a.Source.Name != "t.csv" || a.Source.RowsRead != 1 {
		t.Fatalf("meta = %+v", a.Source)
	}
}

func TestCaps(t *testing.T) {
	two := hdr + "1,Certified,2024-01-01,2024-01-05,H-1B,Alpha Inc\n2,Certified,2024-01-01,2024-01-05,H-1B,Beta Inc\n"
	if _, err := ImportWithLimits(strings.NewReader(two), "x", Limits{MaxRows: 10, MaxEmployers: 1}); err == nil {
		t.Error("employer cap not enforced")
	}
	if _, err := ImportWithLimits(strings.NewReader(two), "x", Limits{MaxRows: 1, MaxEmployers: 10}); err == nil {
		t.Error("row cap not enforced")
	}
	if _, err := ImportWithLimits(strings.NewReader(two), "x", Limits{MaxRows: 2, MaxEmployers: 2}); err != nil {
		t.Errorf("at-cap import should pass: %v", err)
	}
	// ragged rows count against the row cap too
	ragged := hdr + "a\nb\nc\n"
	if _, err := ImportWithLimits(strings.NewReader(ragged), "x", Limits{MaxRows: 2, MaxEmployers: 2}); err == nil {
		t.Error("ragged rows must count toward the row cap")
	}
}

func rows(name string, fy int, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%d,Certified,%d-01-01,%d-02-01,H-1B,%s\n", i, fy, fy, name)
	}
	return b.String()
}

func TestReimportIdempotent(t *testing.T) {
	csv := hdr + rows("Acme Inc", 2024, 3)
	idx := NewIndex()
	for i := 0; i < 3; i++ {
		if err := idx.Merge(imp(t, csv)); err != nil {
			t.Fatal(err)
		}
	}
	if got := idx.Lookup("Acme").Total; got != 3 {
		t.Fatalf("Total = %d, want 3", got)
	}
	if err := idx.Merge(imp(t, hdr+rows("Acme Inc", 2023, 2))); err != nil {
		t.Fatal(err)
	}
	if got := idx.Lookup("Acme").Total; got != 5 || len(idx.Sources()) != 2 {
		t.Fatalf("Total = %d sources=%d", got, len(idx.Sources()))
	}
	if err := idx.Merge(Partial{}); err == nil {
		t.Error("partial without sha must be refused")
	}
}

func buildIndex(t *testing.T) *Index {
	t.Helper()
	idx := NewIndex()
	var b strings.Builder
	b.WriteString(hdr)
	b.WriteString(rows("Google LLC", 2022, 1))
	b.WriteString(rows("Google LLC", 2023, 2))
	b.WriteString(rows("Google LLC", 2024, 3))
	b.WriteString(rows("Google LLC", 2025, 4))
	b.WriteString(rows("Acme Robotics Inc", 2025, 5))
	b.WriteString(rows("Acme Robotics Labs LLC", 2025, 1))
	b.WriteString(rows("Solo Widgets Corp", 2025, 7))
	b.WriteString(rows("Solo Widgets International Corp", 2024, 2))
	b.WriteString(rows("Tesla Motors Inc", 2025, 9))
	b.WriteString(rows("Tesla", 2025, 1))
	if err := idx.Merge(imp(t, b.String())); err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestLookup(t *testing.T) {
	idx := buildIndex(t)
	if got := idx.Coverage(); !reflect.DeepEqual(got, []int{2022, 2023, 2024, 2025}) {
		t.Fatalf("Coverage = %v", got)
	}

	b := idx.Lookup("GOOGLE, L.L.C.")
	if b.Confidence != ConfExact || b.MatchedName != "GOOGLE" || b.Total != 9 ||
		!reflect.DeepEqual(b.FYs, []int{2023, 2024, 2025}) ||
		!reflect.DeepEqual(b.Certified, map[int]int{2023: 2, 2024: 3, 2025: 4}) {
		t.Errorf("exact = %+v", b)
	}
	if got, want := b.String(), "9 certified H-1B LCAs FY2023–FY2025 (exact match)"; got != want {
		t.Errorf("String = %q, want %q", got, want)
	}

	// An exact match wins even when the key is also a prefix of another employer.
	if b := idx.Lookup("Acme Robotics Inc"); b.Confidence != ConfExact || b.MatchedName != "ACME ROBOTICS" || b.Total != 5 {
		t.Errorf("Acme Robotics = %+v", b)
	}

	// Pure prefix cases use a second index with no exact keys.
	idx2 := NewIndex()
	_ = idx2.Merge(imp(t, hdr+rows("Northwind Traders Holdings Inc", 2025, 4)+
		rows("Contoso Labs Europe Ltd", 2025, 1)+rows("Contoso Labs Asia Ltd", 2025, 1)))
	b = idx2.Lookup("Northwind Traders")
	if b.Confidence != ConfUniquePrefix || b.MatchedName != "NORTHWIND TRADERS HOLDINGS" || b.Total != 4 {
		t.Errorf("unique prefix = %+v", b)
	}
	if got, want := b.String(), "4 certified H-1B LCAs FY2025 (unique prefix match: NORTHWIND TRADERS HOLDINGS)"; got != want {
		t.Errorf("String = %q, want %q", got, want)
	}
	if b := idx2.Lookup("Contoso Labs"); b.Confidence != ConfNone || b.MatchedName != "" || b.Total != 0 {
		t.Errorf("ambiguous must be none: %+v", b)
	}
	// single-token company is never prefix matched.
	if b := idx2.Lookup("Northwind"); b.Confidence != ConfNone {
		t.Errorf("short name must be none: %+v", b)
	}
	// token boundary: "Northwin Traders" must not match.
	if b := idx2.Lookup("Northwind Trade"); b.Confidence != ConfNone {
		t.Errorf("partial token must be none: %+v", b)
	}
	if b := idx2.Lookup("Inc"); b.Confidence != ConfNone || b.String() != "no LCA match" {
		t.Errorf("empty normalisation: %+v", b)
	}
	if b := NewIndex().Lookup("Google"); b.Confidence != ConfNone || len(b.FYs) != 0 {
		t.Errorf("empty index: %+v", b)
	}
	// single exact token is allowed.
	if b := idx.Lookup("Tesla"); b.Confidence != ConfExact || b.Total != 1 {
		t.Errorf("single-token exact: %+v", b)
	}
}

func TestFYRangeFormatting(t *testing.T) {
	if got := fyRange([]int{2021, 2023, 2025}); got != "FY2021, FY2023, FY2025" {
		t.Errorf("non-contiguous = %q", got)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	idx := buildIndex(t)
	path := filepath.Join(t.TempDir(), "lca.json")
	if err := idx.Save(path); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Google", "Acme Robotics", "Tesla Motors", "Nobody"} {
		if a, b := idx.Lookup(name), got.Lookup(name); !reflect.DeepEqual(a, b) {
			t.Errorf("%s: %+v != %+v", name, a, b)
		}
	}
	if !reflect.DeepEqual(idx.Coverage(), got.Coverage()) || !reflect.DeepEqual(idx.Sources(), got.Sources()) {
		t.Error("coverage/sources differ after round trip")
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"version":1`) {
		t.Errorf("schema version missing: %.120s", raw)
	}
}

func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lca.json")
	good := buildIndex(t)
	if err := good.Save(path); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)

	// Rename onto a directory fails: the target must be untouched and no temp file left.
	blocked := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := good.Save(blocked); err == nil {
		t.Fatal("expected error saving over a directory")
	}
	// Saving into a missing directory fails cleanly.
	if err := good.Save(filepath.Join(dir, "nope", "x.json")); err == nil {
		t.Fatal("expected error for missing directory")
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
	// Overwrite keeps a valid file at all times and replaces content.
	other := NewIndex()
	if err := other.Merge(imp(t, hdr+rows("Zed Inc", 2025, 1))); err != nil {
		t.Fatal(err)
	}
	if err := other.Save(path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) == string(after) {
		t.Error("overwrite did not change the file")
	}
	if _, err := Load(path); err != nil {
		t.Errorf("overwritten file must load: %v", err)
	}
}

func TestLoadRefusesCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lca.json")
	if err := buildIndex(t).Save(path); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(path)

	mutate := map[string]string{
		"truncated":       string(good[:len(good)/2]),
		"garbage":         "not json at all",
		"empty":           "",
		"trailing":        string(good) + `{"x":1}`,
		"wrong version":   strings.Replace(string(good), `"version":1`, `"version":2`, 1),
		"flipped count":   strings.Replace(string(good), `"GOOGLE":{`, `"GOOGLE":{"2099":[1,0,0,0],`, 1),
		"bad checksum":    strings.Replace(string(good), `"checksum":"`, `"checksum":"00`, 1),
		"unknown field":   strings.Replace(string(good), `"version":1`, `"version":1,"extra":true`, 1),
		"negative count":  strings.Replace(string(good), `"2025":[4,0,0,0]`, `"2025":[-4,0,0,0]`, 1),
		"count wrong len": strings.Replace(string(good), `"2025":[4,0,0,0]`, `"2025":[4,0,0]`, 1),
	}
	for name, content := range mutate {
		if content == string(good) {
			t.Errorf("%s: mutation did not change the file", name)
			continue
		}
		p := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".json")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if idx, err := Load(p); err == nil || idx != nil {
			t.Errorf("%s: Load accepted a corrupt file", name)
		}
	}
	if _, err := Load(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing file must error")
	}
}
