// Package htmltext reduces untrusted HTML to bounded plain text. It is not an
// HTML parser and does not need to be: it only has to keep the words, mark
// where the block structure broke lines, and never let markup, scripts or
// control characters through. Output is never interpreted as markup again.
package htmltext

import (
	"html"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Text reduces HTML to plain text of at most max bytes.
//
//   - script and style elements are dropped with their content, and so are
//     comments; an unclosed one drops the rest of the input.
//   - block tags, <br> and <li> become line breaks, table cells a space.
//   - other tags are dropped, keeping their text.
//   - entities are unescaped once, after tag removal, so "&lt;b&gt;" stays the
//     literal text "<b>" rather than being re-read as markup.
//   - whitespace is collapsed and control characters are removed.
//
// A "<" that does not open a tag, or a tag with no closing ">", is kept as
// literal text. A max of zero or less yields "".
func Text(s string, max int) string {
	if max <= 0 {
		return ""
	}
	// Entities only shrink text, but whitespace collapsing and unescaping
	// run after this loop, so keep a margin and stop reading once it is full.
	budget := 4*max + 64
	var b strings.Builder
	noGT := false
	space := false // a pending single space between words
	for i := 0; i < len(s) && b.Len() < budget; {
		c := s[i]
		if c == '<' && !noGT && i+1 < len(s) && isTagStart(s[i+1]) {
			end := strings.IndexByte(s[i:], '>')
			if end < 0 {
				noGT = true // no ">" anywhere after i, so none after any later "<"
			} else {
				end += i
				i = handleTag(s, i, end, &b, &space)
				continue
			}
		}
		if isSpaceByte(c) {
			space = true
			i++
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteByte(c)
		i++
	}
	return Plain(html.UnescapeString(b.String()), max)
}

// Plain normalises text that is already plain: it drops invalid UTF-8 and
// control characters, collapses whitespace within lines, limits blank lines to
// one, trims, and caps the result at max bytes on a rune boundary.
func Plain(s string, max int) string {
	if max <= 0 {
		return ""
	}
	s = strings.ToValidUTF8(s, "")
	var out strings.Builder
	blank := 0
	for _, line := range strings.Split(s, "\n") {
		line = strings.Join(strings.Fields(strings.Map(dropControl, line)), " ")
		if line == "" {
			if out.Len() > 0 {
				blank++
			}
			continue
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
			if blank > 0 {
				out.WriteByte('\n')
			}
		}
		blank = 0
		out.WriteString(line)
		if out.Len() >= max {
			break
		}
	}
	return Truncate(out.String(), max)
}

// Truncate cuts s to at most max bytes without splitting a rune.
func Truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRightFunc(s[:cut], unicode.IsSpace)
}

// handleTag consumes the tag spanning s[start..end] (end is the '>') and
// returns the index to continue from.
func handleTag(s string, start, end int, b *strings.Builder, space *bool) int {
	next := end + 1
	if strings.HasPrefix(s[start:], "<!--") {
		// A comment ends at the first "-->", which may sit before this '>'
		// would have if the comment contains '>' itself.
		if k := strings.Index(s[start+4:], "-->"); k >= 0 {
			return start + 4 + k + 3
		}
		return len(s)
	}
	t, closing := tagOf(s[start+1 : end])
	switch t.kind {
	case dropContent:
		if closing {
			return next
		}
		// Skip to the matching close tag; an unclosed element drops the rest.
		if k := IndexFold(s[next:], t.close); k >= 0 {
			if e := strings.IndexByte(s[next+k:], '>'); e >= 0 {
				return next + k + e + 1
			}
		}
		return len(s)
	case cell:
		*space = true
	case block:
		// One break per run of block tags, and none at the start: nested
		// lists and adjacent paragraphs must not stack blank lines.
		if n := b.Len(); n > 0 && b.String()[n-1] != '\n' {
			b.WriteByte('\n')
		}
		*space = false
	}
	return next
}

// tagKind is what Text does with a tag; other tags are dropped, keeping
// their text.
type tagKind uint8

const (
	other       tagKind = iota
	dropContent         // script, style: dropped with everything up to the close tag
	cell                // td, th: a space
	block               // a line break
)

type tagInfo struct {
	kind  tagKind
	close string // for dropContent: the lower-case close-tag prefix ("</script")
}

// tags is every element Text acts on, by lower-case name.
var tags = func() map[string]tagInfo {
	m := map[string]tagInfo{
		"script": {dropContent, "</script"},
		"style":  {dropContent, "</style"},
		"td":     {kind: cell},
		"th":     {kind: cell},
	}
	for _, n := range []string{"br", "li", "p", "div", "ul", "ol", "dl", "dt", "dd", "tr", "table",
		"h1", "h2", "h3", "h4", "h5", "h6", "section", "article", "header",
		"footer", "blockquote", "pre", "hr", "nav", "aside", "form", "fieldset",
		"figure", "figcaption", "main", "address", "details", "summary"} {
		m[n] = tagInfo{kind: block}
	}
	return m
}()

// maxTagName is the longest name in tags ("blockquote", "figcaption").
const maxTagName = 10

// tagOf looks up the element named by the tag body (the text between '<' and
// '>') and reports whether it is a closing tag. The name is ASCII-folded in a
// stack buffer, so an upper-case tag costs no allocation.
func tagOf(body string) (tagInfo, bool) {
	closing := strings.HasPrefix(body, "/")
	if closing {
		body = body[1:]
	}
	n := 0
	for n < len(body) && isAlnum(lowerByte(body[n])) {
		n++
	}
	if n > maxTagName {
		return tagInfo{}, closing
	}
	var buf [maxTagName]byte
	for i := 0; i < n; i++ {
		buf[i] = lowerByte(body[i])
	}
	return tags[string(buf[:n])], closing
}

func isTagStart(c byte) bool {
	return c == '/' || c == '!' || c == '?' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

func lowerByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// LowerASCII lower-cases ASCII letters only, so byte offsets match the input
// (a Unicode-aware ToLower can change the length of a string). It is meant for
// short names (a tag, an attribute); a string with no upper-case letter is
// returned as is, without a copy.
func LowerASCII(s string) string {
	i := 0
	for i < len(s) && (s[i] < 'A' || s[i] > 'Z') {
		i++
	}
	if i == len(s) {
		return s
	}
	buf := []byte(s)
	for ; i < len(buf); i++ {
		buf[i] = lowerByte(buf[i])
	}
	return string(buf)
}

// IndexFold is strings.Index(LowerASCII(s), sub) for a lower-case sub,
// without copying s: a match ignores ASCII case only, as the markup scanners
// need, so a document's offsets are the offsets it returns. A sub that starts
// with a non-letter ("</script", every caller's case) is found with
// strings.IndexByte.
func IndexFold(s, sub string) int {
	n := len(sub)
	if n == 0 {
		return 0
	}
	c0 := sub[0]
	letter := c0 >= 'a' && c0 <= 'z'
	for i := 0; i+n <= len(s); i++ {
		if letter {
			if lowerByte(s[i]) != c0 {
				continue
			}
		} else {
			j := strings.IndexByte(s[i:len(s)-n+1], c0)
			if j < 0 {
				return -1
			}
			i += j
		}
		if equalFold(s[i:i+n], sub) {
			return i
		}
	}
	return -1
}

// equalFold reports whether a, ASCII-lowered, equals the lower-case b.
func equalFold(a, b string) bool {
	for k := 0; k < len(b); k++ {
		if lowerByte(a[k]) != b[k] {
			return false
		}
	}
	return true
}

func dropControl(r rune) rune {
	if r == '\t' {
		return ' '
	}
	if unicode.IsControl(r) {
		return -1
	}
	return r
}
