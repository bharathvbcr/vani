package employer

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Synthetic files in the documented layouts. Every organisation is invented.
const bmfCSV = "EIN,NAME,ICO,STREET,CITY,STATE,ZIP,GROUP,SUBSECTION,AFFILIATION,CLASSIFICATION,RULING,DEDUCTIBILITY,FOUNDATION,ACTIVITY,ORGANIZATION,STATUS,TAX_PERIOD,ASSET_CD,INCOME_CD,FILING_REQ_CD,PF_FILING_REQ_CD,ACCT_PD,ASSET_AMT,INCOME_AMT,REVENUE_AMT,NTEE_CD,SORT_NAME\n" +
	"000000001,ZYLOPHANE RESEARCH INSTITUTE INC,,1 MAIN ST,SPRINGFIELD,ZZ,00000,0000,03,3,1000,200001,1,15,000000000,1,01,202312,0,0,01,0,12,0,0,0,U20,\n" +
	"000000002,QUILLWORT FOUNDATION,,2 MAIN ST,SPRINGFIELD,ZZ,00000,0000,03,3,1000,200001,1,15,000000000,1,01,202312,0,0,01,0,12,0,0,0,T20,\n" +
	"000000003,Quillwort Foundation,,3 MAIN ST,SHELBYVILLE,ZZ,00000,0000,03,3,1000,200001,1,15,000000000,1,01,202312,0,0,01,0,12,0,0,0,T20,\n" + // duplicate name
	"12345,BAD EIN SOCIETY,,4 MAIN ST,SPRINGFIELD,ZZ,00000,0000,03,3,1000,200001,1,15,000000000,1,01,202312,0,0,01,0,12,0,0,0,T20,\n" +
	"000000005,INC,,5 MAIN ST,SPRINGFIELD,ZZ,00000,0000,03,3,1000,200001,1,15,000000000,1,01,202312,0,0,01,0,12,0,0,0,T20,\n" + // empty after normalising
	"000000006,RAGGED ROW\n" +
	"000000007,STATE UNIVERSITY OF NOWHERE FOUNDATION,,7 MAIN ST,SPRINGFIELD,ZZ,00000,0000,03,3,1000,200001,1,15,000000000,1,01,202312,0,0,01,0,12,0,0,0,B43,\n"

const ipedsCSV = "UNITID,INSTNM,IALIAS,ADDR,CITY,STABBR,CONTROL,DEGGRANT\n" +
	"100001,State University of Nowhere,SUN|Nowhere State U,1 Campus Dr,Nowhere,ZZ,1,1\n" +
	"100002,Private College of Elsewhere,,2 Campus Dr,Elsewhere,ZZ,2,1\n" +
	"100003,Profit Tech Institute LLC,,3 Campus Dr,Elsewhere,ZZ,3,1\n" +
	"100004,Nowhere Beauty Academy,,4 Campus Dr,Nowhere,ZZ,1,2\n" +
	"100005,Unknown Control College,,5 Campus Dr,Nowhere,ZZ,-3,1\n"

func mustImport(t *testing.T, csv, name string) Partial {
	t.Helper()
	p, err := Import(strings.NewReader(csv), name)
	if err != nil {
		t.Fatalf("Import %s: %v", name, err)
	}
	return p
}

func TestImportBMF(t *testing.T) {
	p := mustImport(t, bmfCSV, "eo_zz.csv")
	if p.Source.Kind != KindIRSBMF || p.Source.RowsRead != 7 || p.Source.RowsCounted != 4 {
		t.Fatalf("meta %+v", p.Source)
	}
	want := map[string]int{SkipBadEIN: 1, SkipEmpty: 1, SkipRagged: 1}
	if !reflect.DeepEqual(p.Source.Skipped, want) {
		t.Fatalf("skipped %v, want %v", p.Source.Skipped, want)
	}
	if len(p.Names) != 3 || p.Names["QUILLWORT FOUNDATION"] != bitNonprofit || p.Names["ZYLOPHANE RESEARCH INSTITUTE"] != bitNonprofit {
		t.Fatalf("names %v", p.Names)
	}
	if p.Source.SHA256 == "" {
		t.Fatal("no source hash")
	}
}

func TestImportIPEDS(t *testing.T) {
	p := mustImport(t, ipedsCSV, "hd2024.csv")
	if p.Source.Kind != KindIPEDS || p.Source.RowsCounted != 2 {
		t.Fatalf("meta %+v", p.Source)
	}
	want := map[string]attr{
		"STATE UNIVERSITY OF NOWHERE":  bitCapExempt,
		"NOWHERE STATE U":              bitCapExempt, // alias
		"PRIVATE COLLEGE OF ELSEWHERE": bitCapExempt | bitNonprofit,
	}
	if !reflect.DeepEqual(p.Names, want) {
		t.Fatalf("names %v, want %v", p.Names, want)
	}
	if p.Source.Skipped[SkipForProfit] != 2 || p.Source.Skipped[SkipNotDegree] != 1 || p.Source.Skipped[SkipBadAliasList] != 1 {
		t.Fatalf("skipped %v (one-token alias SUN must be dropped)", p.Source.Skipped)
	}
}

func TestAliasDelimiters(t *testing.T) {
	csv := "UNITID,INSTNM,IALIAS,CONTROL,DEGGRANT\n" +
		`1,University of Nowhere-East,"NWU East; Nowhere East U|U of N East",1,1` + "\n" +
		`2,Nowhere Tech,"University of Nowhere, West Campus",1,1` + "\n"
	p := mustImport(t, csv, "hd.csv")
	for _, k := range []string{"NWU EAST", "NOWHERE EAST U", "U OF N EAST", "UNIVERSITY OF NOWHERE WEST CAMPUS"} {
		if _, ok := p.Names[k]; !ok {
			t.Errorf("alias %q missing: %v", k, p.Names)
		}
	}
	// A comma is part of a name, never a delimiter: no "WEST CAMPUS" or
	// "UNIVERSITY OF NOWHERE" alias pointing at the wrong institution.
	for _, k := range []string{"WEST CAMPUS", "UNIVERSITY OF NOWHERE"} {
		if _, ok := p.Names[k]; ok {
			t.Errorf("comma split produced alias %q", k)
		}
	}
}

func TestImportLatin1(t *testing.T) {
	csv := "UNITID,INSTNM,CONTROL,DEGGRANT\n1,Universit\xe9 de Nulle Part,1,1\n"
	p := mustImport(t, csv, "hd.csv")
	if _, ok := p.Names["UNIVERSITÉ DE NULLE PART"]; !ok {
		t.Fatalf("latin-1 name not decoded: %v", p.Names)
	}
}

func TestImportRefusals(t *testing.T) {
	cases := map[string]struct {
		csv  string
		lim  Limits
		want string
	}{
		"empty":           {"", DefaultLimits, "empty input"},
		"unknown header":  {"A,B\n1,2\n", DefaultLimits, "unrecognised file"},
		"both kinds":      {"EIN,NAME,SUBSECTION,UNITID,INSTNM\n", DefaultLimits, "both IRS BMF and IPEDS"},
		"ipeds no codes":  {"UNITID,INSTNM\n1,X College\n", DefaultLimits, "CONTROL, DEGGRANT"},
		"nothing counted": {"UNITID,INSTNM,CONTROL,DEGGRANT\n1,X College,7,9\n", DefaultLimits, "none counted"},
		"bad quote":       {"EIN,NAME,SUBSECTION\n000000001,\"A\" B\"\",03\n000000002,\"unterminated,03\n", DefaultLimits, "row"},
		"rows cap":        {bmfCSV, Limits{MaxRows: 3, MaxNames: 100}, "more than 3 rows"},
		"names cap":       {bmfCSV, Limits{MaxRows: 100, MaxNames: 1}, "more than 1 distinct names"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ImportWithLimits(strings.NewReader(c.csv), "f.csv", c.lim)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %q", err, c.want)
			}
		})
	}
}

func TestImportOversizedField(t *testing.T) {
	// A huge single cell is read (csv has no per-field cap) but bounded by
	// the caller's file, and normalises to one key: no crash, one name.
	big := strings.Repeat("A", 1<<20)
	p := mustImport(t, "EIN,NAME,SUBSECTION\n000000001,"+big+" SOCIETY,03\n", "big.csv")
	if len(p.Names) != 1 {
		t.Fatalf("names %d", len(p.Names))
	}
}

func buildIndex(t *testing.T) *Index {
	t.Helper()
	x := NewIndex()
	for name, csv := range map[string]string{"eo_zz.csv": bmfCSV, "hd2024.csv": ipedsCSV} {
		if err := x.Merge(mustImport(t, csv, name)); err != nil {
			t.Fatal(err)
		}
	}
	return x
}

func TestMergeIsIdempotentAndCombinesSources(t *testing.T) {
	x := buildIndex(t)
	n := x.Names()
	if err := x.Merge(mustImport(t, bmfCSV, "again.csv")); err != nil {
		t.Fatal(err)
	}
	if x.Names() != n || len(x.Sources()) != 2 {
		t.Fatalf("re-import changed the index: %d names, %d sources", x.Names(), len(x.Sources()))
	}
	if err := x.Merge(Partial{Source: SourceMeta{SHA256: "x", Kind: "nope"}}); err == nil {
		t.Error("unknown kind merged")
	}
	if err := x.Merge(Partial{Source: SourceMeta{Kind: KindIPEDS}}); err == nil {
		t.Error("partial without sha merged")
	}
	if err := x.Merge(Partial{Source: SourceMeta{SHA256: "y", Kind: KindIPEDS}, Names: map[string]attr{"not normal, inc": bitNonprofit}}); err == nil {
		t.Error("unnormalised key merged")
	}
	if err := x.Merge(Partial{Source: SourceMeta{SHA256: "z", Kind: KindIPEDS}, Names: map[string]attr{"OK": 0x80}}); err == nil {
		t.Error("unknown attribute bits merged")
	}
}

func TestLookup(t *testing.T) {
	x := buildIndex(t)
	cases := []struct {
		company, conf, matched string
		attrs                  []string
		confident              bool
	}{
		{"Quillwort Foundation", ConfExact, "QUILLWORT FOUNDATION", []string{AttrNonprofit}, true},
		{"The Zylophane Research Institute, Inc.", ConfExact, "ZYLOPHANE RESEARCH INSTITUTE", []string{AttrNonprofit}, true},
		{"Private College of Elsewhere", ConfExact, "PRIVATE COLLEGE OF ELSEWHERE", []string{AttrCapExemptLikely, AttrNonprofit}, true},
		{"Nowhere State U", ConfExact, "NOWHERE STATE U", []string{AttrCapExemptLikely}, true},
		// "STATE UNIVERSITY OF NOWHERE" (IPEDS) is a prefix of the BMF
		// foundation's name too, but it matches exactly first.
		{"State University of Nowhere", ConfExact, "STATE UNIVERSITY OF NOWHERE", []string{AttrCapExemptLikely}, true},
		{"Zylophane Research", ConfUniquePrefix, "ZYLOPHANE RESEARCH INSTITUTE", []string{AttrNonprofit}, false},
		{"Profit Tech Institute", ConfNone, "", nil, false},
		{"Quillwort", ConfNone, "", nil, false}, // one token never prefix-matches
		{"", ConfNone, "", nil, false},
	}
	for _, c := range cases {
		b := x.Lookup(c.company)
		if b.Confidence != c.conf || b.MatchedName != c.matched || !reflect.DeepEqual(b.Attributes, c.attrs) || b.Confident() != c.confident {
			t.Errorf("Lookup(%q) = %+v confident=%v", c.company, b, b.Confident())
		}
		s := b.String()
		if c.conf != ConfNone && !strings.Contains(s, "likely") {
			t.Errorf("badge %q must read likely", s)
		}
		if strings.Contains(strings.ToLower(s), "is cap-exempt") || strings.Contains(strings.ToLower(s), "determin") {
			t.Errorf("badge %q reads as a determination", s)
		}
	}
	if s := x.Lookup("Private College of Elsewhere").String(); !strings.Contains(s, "cap-exempt") || !strings.Contains(s, "IPEDS") {
		t.Errorf("badge %q", s)
	}
	if s := x.Lookup("Zylophane Research").String(); !strings.Contains(s, "not used for rank") {
		t.Errorf("prefix badge %q", s)
	}
}

func TestOneWordIRSMatchNeverConfident(t *testing.T) {
	x := NewIndex()
	bmf := "EIN,NAME,SUBSECTION\n000000001,ZORP INC,03\n000000002,QUILLWORT FOUNDATION,03\n"
	ipeds := "UNITID,INSTNM,CONTROL,DEGGRANT\n1,Nowhereversity,2,1\n"
	for name, csv := range map[string]string{"eo.csv": bmf, "hd.csv": ipeds} {
		if err := x.Merge(mustImport(t, csv, name)); err != nil {
			t.Fatal(err)
		}
	}
	// "Zorp Inc." (a company) normalises to the one word "ZORP", which only
	// the IRS list holds: shown, never confident.
	b := x.Lookup("Zorp Inc.")
	if b.Confidence != ConfExactOneWord || b.Confident() || !strings.Contains(b.String(), "not used for rank") {
		t.Fatalf("one-word IRS match: %+v %q", b, b.String())
	}
	// A one-word IPEDS institution and a two-word IRS name stay confident.
	if b := x.Lookup("Nowhereversity"); b.Confidence != ConfExact || !b.Confident() {
		t.Fatalf("one-word IPEDS match: %+v", b)
	}
	if b := x.Lookup("Quillwort Foundation"); !b.Confident() {
		t.Fatalf("two-word IRS match: %+v", b)
	}
}

func TestSaveLoadRoundTripAndCorruption(t *testing.T) {
	x := buildIndex(t)
	path := filepath.Join(t.TempDir(), "index.json")
	if err := x.Save(path); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", st.Mode(), err)
	}
	y, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if y.Names() != x.Names() || !reflect.DeepEqual(y.Sources(), x.Sources()) ||
		!reflect.DeepEqual(y.Lookup("Private College of Elsewhere"), x.Lookup("Private College of Elsewhere")) {
		t.Fatal("round trip changed the index")
	}
	data, _ := os.ReadFile(path)
	for name, mut := range map[string][]byte{
		"truncated": data[:len(data)/2],
		"tampered":  bytes.Replace(data, []byte("QUILLWORT"), []byte("QUILLWORX"), 1),
		"version":   bytes.Replace(data, []byte(`"version":1`), []byte(`"version":9`), 1),
		"trailing":  append(append([]byte{}, data...), []byte("{}")...),
		"unknown":   bytes.Replace(data, []byte(`"version":1`), []byte(`"version":1,"extra":true`), 1),
	} {
		p := filepath.Join(t.TempDir(), name+".json")
		if err := os.WriteFile(p, mut, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil {
			t.Errorf("%s index loaded", name)
		}
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); !os.IsNotExist(unwrapAll(err)) {
		t.Errorf("missing index: %v", err)
	}
}

func unwrapAll(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
}

func TestConcurrentLookupAndMerge(t *testing.T) {
	x := buildIndex(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_ = x.Merge(mustImport(t, ipedsCSV, "hd.csv"))
		}
	}()
	for i := 0; i < 500; i++ {
		_ = x.Lookup("Quillwort Foundation")
	}
	<-done
}
