// Package xlsx writes a one-sheet Office Open XML workbook (.xlsx) with the
// standard library: a bold, frozen, filterable header row over rows of text and
// number cells. It writes no formulas: every text cell is an inline string, so
// untrusted text that starts with "=" stays text in every spreadsheet app
// (unlike CSV, where such a cell runs as a formula).
//
// The parts and their order follow ECMA-376 Part 1 (SpreadsheetML): the
// worksheet's children are written in the schema's sequence (sheetViews, cols,
// sheetData, autoFilter), which Excel enforces.
package xlsx

import (
	"archive/zip"
	"bufio"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Limits Excel enforces (Excel specifications and limits).
const (
	MaxRows       = 1 << 20 // rows in a sheet, the header included
	MaxCols       = 1 << 14
	MaxCellRunes  = 32767
	MaxSheetName  = 31
	maxColumnWide = 255
)

// Cell is one worksheet cell: text, a number, or empty.
type Cell struct {
	text   string
	num    float64
	isNum  bool
	filled bool
}

// Text is a text cell. Invalid UTF-8 and characters XML cannot carry become
// U+FFFD, and text past MaxCellRunes is cut and ends with "…".
func Text(s string) Cell {
	if s == "" {
		return Cell{}
	}
	return Cell{text: s, filled: true}
}

// Number is a number cell; NaN and ±Inf, which a sheet cannot hold, are empty.
func Number(f float64) Cell {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return Cell{}
	}
	return Cell{num: f, isNum: true, filled: true}
}

// Sheet is the one worksheet a workbook holds.
type Sheet struct {
	Name   string
	Header []string
	// Widths are column widths in characters; a missing or non-positive one
	// leaves the column at the app's default.
	Widths []float64
	Rows   [][]Cell
}

// Write writes s as a workbook to w. It checks the sheet against Excel's
// limits first and writes nothing when they fail.
func Write(w io.Writer, s Sheet) error {
	if err := check(s); err != nil {
		return err
	}
	z := zip.NewWriter(w)
	parts := []struct {
		name string
		body func(io.Writer) error
	}{
		{"[Content_Types].xml", constant(contentTypes)},
		{"_rels/.rels", constant(rootRels)},
		{"xl/workbook.xml", func(w io.Writer) error { return workbook(w, s) }},
		{"xl/_rels/workbook.xml.rels", constant(workbookRels)},
		{"xl/styles.xml", constant(styles)},
		{"xl/worksheets/sheet1.xml", func(w io.Writer) error { return worksheet(w, s) }},
	}
	for _, p := range parts {
		f, err := z.Create(p.name)
		if err != nil {
			return fmt.Errorf("xlsx: %s: %w", p.name, err)
		}
		if err := p.body(f); err != nil {
			return fmt.Errorf("xlsx: %s: %w", p.name, err)
		}
	}
	if err := z.Close(); err != nil {
		return fmt.Errorf("xlsx: %w", err)
	}
	return nil
}

func check(s Sheet) error {
	switch {
	case s.Name == "" || utf8.RuneCountInString(s.Name) > MaxSheetName || !utf8.ValidString(s.Name):
		return fmt.Errorf("xlsx: sheet name must be 1-%d characters of UTF-8", MaxSheetName)
	case strings.ContainsAny(s.Name, `[]:*?/\`) || strings.HasPrefix(s.Name, "'") || strings.HasSuffix(s.Name, "'"):
		return errors.New(`xlsx: sheet name must not contain []:*?/\ or start or end with '`)
	case len(s.Header) == 0 || len(s.Header) > MaxCols:
		return fmt.Errorf("xlsx: header must have 1-%d columns", MaxCols)
	case len(s.Rows)+1 > MaxRows:
		return fmt.Errorf("xlsx: %d rows is more than a sheet holds (%d)", len(s.Rows), MaxRows-1)
	}
	for i, r := range s.Rows {
		if len(r) > len(s.Header) {
			return fmt.Errorf("xlsx: row %d has %d cells, more than the %d header columns", i+1, len(r), len(s.Header))
		}
	}
	return nil
}

func constant(s string) func(io.Writer) error {
	return func(w io.Writer) error {
		_, err := io.WriteString(w, s)
		return err
	}
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

const contentTypes = xmlHeader + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
	`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
	`<Default Extension="xml" ContentType="application/xml"/>` +
	`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
	`<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` +
	`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` +
	`</Types>`

const rootRels = xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
	`</Relationships>`

const workbookRels = xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>` +
	`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>` +
	`</Relationships>`

// styles has two cell formats: 0 is the default, 1 is bold (the header). The
// two fills are the pair every SpreadsheetML styles part starts with.
const styles = xmlHeader + `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
	`<fonts count="2"><font><sz val="11"/><name val="Calibri"/><family val="2"/></font>` +
	`<font><b/><sz val="11"/><name val="Calibri"/><family val="2"/></font></fonts>` +
	`<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>` +
	`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
	`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
	`<cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>` +
	`<xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/></cellXfs>` +
	`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>` +
	`</styleSheet>`

// filterRange is the header and every row, for the autoFilter and the defined
// name Excel keeps it under.
func filterRange(s Sheet) string {
	return "A1:" + Column(len(s.Header)-1) + strconv.Itoa(len(s.Rows)+1)
}

func workbook(w io.Writer, s Sheet) error {
	b := bufio.NewWriter(w)
	b.WriteString(xmlHeader)
	b.WriteString(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">`)
	b.WriteString(`<sheets><sheet name="`)
	escape(b, s.Name)
	b.WriteString(`" sheetId="1" r:id="rId1"/></sheets>`)
	// A sheet name is quoted in a reference, with ' doubled.
	ref := "'" + strings.ReplaceAll(s.Name, "'", "''") + "'!" + absolute(filterRange(s))
	b.WriteString(`<definedNames><definedName name="_xlnm._FilterDatabase" localSheetId="0" hidden="1">`)
	escape(b, ref)
	b.WriteString(`</definedName></definedNames></workbook>`)
	return b.Flush()
}

// absolute turns "A1:Z9" into "$A$1:$Z$9".
func absolute(r string) string {
	var out strings.Builder
	for _, end := range strings.Split(r, ":") {
		if out.Len() > 0 {
			out.WriteByte(':')
		}
		i := strings.IndexAny(end, "0123456789")
		out.WriteString("$" + end[:i] + "$" + end[i:])
	}
	return out.String()
}

func worksheet(w io.Writer, s Sheet) error {
	b := bufio.NewWriter(w)
	b.WriteString(xmlHeader)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">`)
	b.WriteString(`<sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/>` +
		`<selection pane="bottomLeft" activeCell="A2" sqref="A2"/></sheetView></sheetViews>`)
	cols := false
	for i, wd := range s.Widths {
		if i >= len(s.Header) || !(wd > 0) {
			continue
		}
		if !cols {
			b.WriteString(`<cols>`)
			cols = true
		}
		wd = math.Min(wd, maxColumnWide)
		fmt.Fprintf(b, `<col min="%d" max="%d" width="%s" customWidth="1"/>`, i+1, i+1, strconv.FormatFloat(wd, 'f', -1, 64))
	}
	if cols {
		b.WriteString(`</cols>`)
	}
	b.WriteString(`<sheetData>`)
	header := make([]Cell, len(s.Header))
	for i, h := range s.Header {
		header[i] = Text(h)
	}
	row(b, 1, header, ` s="1"`)
	for i, r := range s.Rows {
		row(b, i+2, r, "")
	}
	b.WriteString(`</sheetData>`)
	b.WriteString(`<autoFilter ref="` + filterRange(s) + `"/>`)
	b.WriteString(`</worksheet>`)
	return b.Flush()
}

func row(b *bufio.Writer, n int, cells []Cell, style string) {
	num := strconv.Itoa(n)
	b.WriteString(`<row r="` + num + `">`)
	for i, c := range cells {
		if !c.filled {
			continue
		}
		ref := Column(i) + num
		if c.isNum {
			b.WriteString(`<c r="` + ref + `"` + style + `><v>` + strconv.FormatFloat(c.num, 'g', -1, 64) + `</v></c>`)
			continue
		}
		b.WriteString(`<c r="` + ref + `"` + style + ` t="inlineStr"><is><t xml:space="preserve">`)
		escape(b, cellText(c.text))
		b.WriteString(`</t></is></c>`)
	}
	b.WriteString(`</row>`)
}

// Column is the letters of the 0-based column i: 0 is A, 25 Z, 26 AA.
func Column(i int) string {
	var buf [8]byte
	n := len(buf)
	for i++; i > 0; i = (i - 1) / 26 {
		n--
		buf[n] = byte('A' + (i-1)%26)
	}
	return string(buf[n:])
}

// xstringEscape matches what SpreadsheetML reads as an escaped character
// (ST_Xstring "_xHHHH_") after its leading underscore.
var xstringEscape = regexp.MustCompile(`^x[0-9A-Fa-f]{4}_`)

// guardXstring escapes, as _x005F_, every underscore that starts an
// _xHHHH_ sequence, so the text reads back as written. Sequences can share
// an underscore ("_x005F_x0041_"), so each underscore is tested on its own
// rather than by non-overlapping matches.
func guardXstring(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '_' && xstringEscape.MatchString(s[i+1:]) {
			b.WriteString("_x005F_")
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// cellText makes s fit a cell: valid XML characters only, the _xHHHH_ escape
// neutralised, and at most MaxCellRunes runes.
func cellText(s string) string {
	s = strings.Map(func(r rune) rune {
		if !xmlChar(r) {
			return '�'
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
	if strings.Contains(s, "_x") {
		s = guardXstring(s)
	}
	// Cut after escaping, so the cell holds at most MaxCellRunes as written. A
	// cut through an escape leaves plain text: "…" cannot finish one.
	if utf8.RuneCountInString(s) > MaxCellRunes {
		s = string([]rune(s)[:MaxCellRunes-1]) + "…"
	}
	return s
}

// xmlChar reports whether XML 1.0 can carry r (its Char production).
func xmlChar(r rune) bool {
	return r == 0x09 || r == 0x0A || r == 0x0D ||
		r >= 0x20 && r <= 0xD7FF ||
		r >= 0xE000 && r <= 0xFFFD ||
		r >= 0x10000 && r <= 0x10FFFF
}

func escape(b *bufio.Writer, s string) {
	// xml.EscapeText escapes &, <, >, quotes, \t, \n and \r; a write error
	// surfaces at Flush.
	_ = xml.EscapeText(b, []byte(s))
}
