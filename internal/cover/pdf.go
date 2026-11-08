// Package cover turns a grounded draft into a one-page cover letter.
// The model writes the body. This file draws that body with the standard PDF
// fonts. A letterhead, when the base selection header can be drawn in that
// font, is stamped here and nowhere else.
package cover

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

// CodeRender is a PDF that could not be drawn. The body text is kept.
const CodeRender = "render"

// Error is a cover failure with a code.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return "cover: " + e.Code
}

func renderErr(format string, a ...any) error {
	return &Error{Code: CodeRender, Msg: fmt.Sprintf("cover: "+format, a...)}
}

const (
	pageW    = 612
	pageH    = 792
	margin   = 72
	bodySize = 11
	headSize = 12
	leading  = 14
	// wrapAt is a conservative line length for Helvetica at bodySize on the
	// text width. A wider line could run off the page.
	wrapAt = 78
)

// Render draws body on one page. letterhead is stamped above it only when
// every line can be drawn; otherwise the page has no letterhead. A character
// in body the font cannot draw, or a body that does not fit the page, fails
// with CodeRender and no PDF.
func Render(body string, letterhead []string) ([]byte, error) {
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	if body == "" {
		return nil, renderErr("the letter has no text")
	}
	head := letterhead
	if !drawable(head) {
		head = nil
	}
	lines, err := layout(body, head)
	if err != nil {
		return nil, err
	}
	return buildPDF(contentStream(lines)), nil
}

type line struct {
	text string
	head bool
}

func drawable(lines []string) bool {
	if len(lines) == 0 {
		return false
	}
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if _, err := encodeWinAnsi(ln); err != nil {
			return false
		}
	}
	return true
}

func layout(body string, head []string) ([]line, error) {
	var lines []line
	for _, ln := range head {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		wrapped, err := wrap(ln)
		if err != nil {
			return nil, err
		}
		for _, w := range wrapped {
			lines = append(lines, line{text: w, head: true})
		}
	}
	if len(lines) > 0 {
		lines = append(lines, line{}) // a blank line under the letterhead
	}
	for _, para := range strings.Split(body, "\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		wrapped, err := wrap(para)
		if err != nil {
			return nil, err
		}
		for _, w := range wrapped {
			lines = append(lines, line{text: w})
		}
	}
	if len(lines) == 0 {
		return nil, renderErr("the letter has no text")
	}
	y := pageH - margin - headSize
	for range lines {
		if y < margin {
			return nil, renderErr("the letter does not fit on one page")
		}
		y -= leading
	}
	return lines, nil
}

func wrap(s string) ([]string, error) {
	if _, err := encodeWinAnsi(s); err != nil {
		return nil, renderErr("a character in the letter cannot be drawn")
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil, nil
	}
	var lines []string
	var cur strings.Builder
	for _, w := range words {
		if cur.Len() == 0 {
			cur.WriteString(w)
			continue
		}
		if cur.Len()+1+len(w) > wrapAt {
			lines = append(lines, cur.String())
			cur.Reset()
			cur.WriteString(w)
			continue
		}
		cur.WriteByte(' ')
		cur.WriteString(w)
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines, nil
}

func contentStream(lines []line) string {
	var b strings.Builder
	b.WriteString("BT\n")
	y := pageH - margin - headSize
	font := ""
	for _, ln := range lines {
		want := "/F1"
		size := bodySize
		if ln.head {
			want = "/F2"
			size = headSize
		}
		if want != font {
			fmt.Fprintf(&b, "%s %d Tf\n", want, size)
			font = want
		}
		fmt.Fprintf(&b, "1 0 0 1 %d %d Tm\n", margin, y)
		if ln.text != "" {
			raw, err := encodeWinAnsi(ln.text)
			if err != nil {
				raw = nil
			}
			b.WriteString(pdfString(raw))
			b.WriteString(" Tj\n")
		}
		y -= leading
	}
	b.WriteString("ET\n")
	return b.String()
}

func pdfString(raw []byte) string {
	var b strings.Builder
	b.WriteByte('(')
	for _, c := range raw {
		switch c {
		case '(', ')', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			if c < 32 || c > 126 {
				fmt.Fprintf(&b, "\\%03o", c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte(')')
	return b.String()
}

func buildPDF(content string) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R /F2 6 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>",
	}
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs)+1)
	for i, obj := range objs {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objs)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return buf.Bytes()
}

// win1252 maps the Unicode characters Windows-1252 assigns to 0x80-0x9F.
// Bytes that Windows-1252 leaves undefined are absent.
var win1252 = map[rune]byte{
	0x20AC: 0x80, 0x201A: 0x82, 0x0192: 0x83, 0x201E: 0x84,
	0x2026: 0x85, 0x2020: 0x86, 0x2021: 0x87, 0x02C6: 0x88,
	0x2030: 0x89, 0x0160: 0x8A, 0x2039: 0x8B, 0x0152: 0x8C,
	0x017D: 0x8E, 0x2018: 0x91, 0x2019: 0x92, 0x201C: 0x93,
	0x201D: 0x94, 0x2022: 0x95, 0x2013: 0x96, 0x2014: 0x97,
	0x02DC: 0x98, 0x2122: 0x99, 0x0161: 0x9A, 0x203A: 0x9B,
	0x0153: 0x9C, 0x017E: 0x9E, 0x0178: 0x9F,
}

func encodeWinAnsi(s string) ([]byte, error) {
	if !utf8.ValidString(s) {
		return nil, renderErr("the letter is not UTF-8")
	}
	var b []byte
	for _, r := range s {
		switch {
		case r == '\t':
			b = append(b, ' ')
		case r >= 32 && r <= 126:
			b = append(b, byte(r))
		case r >= 0xA0 && r <= 0xFF:
			b = append(b, byte(r))
		default:
			c, ok := win1252[r]
			if !ok {
				return nil, renderErr("a character in the letter cannot be drawn")
			}
			b = append(b, c)
		}
	}
	return b, nil
}
