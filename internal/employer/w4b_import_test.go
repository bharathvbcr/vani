package employer

import (
	"fmt"
	"strings"
	"testing"
)

// One row whose name normalises to nothing on a second pass ("THE THE" ->
// "THE" -> "") used to make the index refuse the whole import.
func TestW4bOddNameRowIsSkippedWithItsLine(t *testing.T) {
	csv := "EIN,NAME,SUBSECTION\n" +
		"000000001,Red Cross,03\n" +
		"000000002,THE THE,03\n" +
		"000000003,Green Society,03\n"
	p := mustImport(t, csv, "bmf.csv")
	if p.Source.RowsCounted != 2 || p.Source.Skipped[SkipEmpty] != 1 {
		t.Fatalf("counted %d skipped %v, want 2 counted and one empty_name", p.Source.RowsCounted, p.Source.Skipped)
	}
	if len(p.SkippedRows) != 1 || p.SkippedRows[0].Line != 3 || p.SkippedRows[0].Reason != SkipEmpty {
		t.Fatalf("SkippedRows = %+v, want one at line 3", p.SkippedRows)
	}
	idx := NewIndex()
	if err := idx.Merge(p); err != nil {
		t.Fatalf("the index refused the import: %v", err)
	}
	if idx.Names() != 2 {
		t.Fatalf("names = %d, want 2", idx.Names())
	}
}

func TestW4bOddAliasIsDropped(t *testing.T) {
	csv := "UNITID,INSTNM,CONTROL,DEGGRANT,IALIAS\n1,Big University,1,1,THE THE|Big U Campus\n"
	p := mustImport(t, csv, "hd.csv")
	if _, ok := p.Names["THE"]; ok {
		t.Fatalf("a non-normalised alias key entered the partial: %v", p.Names)
	}
	if err := NewIndex().Merge(p); err != nil {
		t.Fatalf("the index refused the import: %v", err)
	}
}

func TestW4bSkippedRowsAreBoundedAndQuotedLinesCount(t *testing.T) {
	var b strings.Builder
	b.WriteString("EIN,NAME,SUBSECTION\n")
	b.WriteString("000000001,\"Multi\nLine Society\",03\n") // lines 2-3
	for i := 0; i < maxSkippedRows+5; i++ {
		fmt.Fprintf(&b, "%09d,THE THE,03\n", i+2)
	}
	p := mustImport(t, b.String(), "bmf.csv")
	if len(p.SkippedRows) != maxSkippedRows || p.Source.Skipped[SkipEmpty] != maxSkippedRows+5 {
		t.Fatalf("rows listed %d, counted %d: the list is capped, the count is not", len(p.SkippedRows), p.Source.Skipped[SkipEmpty])
	}
	if p.SkippedRows[0].Line != 4 {
		t.Fatalf("first skipped line = %d, want 4 (a quoted newline spans lines 2-3)", p.SkippedRows[0].Line)
	}
}

func TestW4bHeaderOnlyIsRefused(t *testing.T) {
	for name, csv := range map[string]string{
		"bmf":   "EIN,NAME,SUBSECTION\n",
		"ipeds": "UNITID,INSTNM,CONTROL,DEGGRANT\n",
		"bom":   "\xEF\xBB\xBFEIN,NAME,SUBSECTION\r\n",
	} {
		_, err := Import(strings.NewReader(csv), "f.csv")
		if err == nil || !strings.Contains(err.Error(), "no data rows") {
			t.Errorf("%s: err = %v, want a no data rows refusal", name, err)
		}
	}
}
