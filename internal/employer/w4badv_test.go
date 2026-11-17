package employer

import (
	"strings"
	"testing"
)

// Line numbers with CRLF, a BOM, and quoted fields holding quotes and newlines.
func TestW4badvSkippedLinesCRLFBOMQuotes(t *testing.T) {
	csv := "\xEF\xBB\xBFEIN,NAME,SUBSECTION\r\n" +
		"000000001,\"Say \"\"hi\"\"\r\nSociety\",03\r\n" + // lines 2-3
		"000000002,THE THE,03\r\n" + // line 4
		"000000003,\"Multi\nline\",03\n" + // lines 5-6
		"000000004,\"THE\nTHE\",03\r\n" + // lines 7-8, name field starts at 7
		"000000005,Green Society,03\r\n"
	p := mustImport(t, csv, "bmf.csv")
	var got []int
	for _, r := range p.SkippedRows {
		got = append(got, r.Line)
	}
	if len(got) != 2 || got[0] != 4 || got[1] != 7 {
		t.Fatalf("skipped lines = %v, want [4 7]", got)
	}
}

// An all-skipped IPEDS file whose only odd rows are aliases still counts.
func TestW4badvAllRowsEmptyNameRefused(t *testing.T) {
	if _, err := Import(strings.NewReader("EIN,NAME,SUBSECTION\n000000001,THE THE,03\n"), "f.csv"); err == nil {
		t.Fatal("an import where every row was skipped succeeded")
	}
}
