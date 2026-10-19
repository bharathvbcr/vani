package htmltext

import (
	"html"
	"strings"
)

// oldText is Text as it was before it stopped lowering a copy of the whole
// input, kept verbatim as the parity oracle.
func oldText(s string, max int) string {
	if max <= 0 {
		return ""
	}
	// Entities only shrink text, but whitespace collapsing and unescaping
	// run after this loop, so keep a margin and stop reading once it is full.
	budget := 4*max + 64
	lower := oldASCIILower(s)
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
				i = oldHandleTag(s, lower, i, end, &b, &space)
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
func oldHandleTag(s, lower string, start, end int, b *strings.Builder, space *bool) int {
	next := end + 1
	if strings.HasPrefix(lower[start:], "<!--") {
		// A comment ends at the first "-->", which may sit before this '>'
		// would have if the comment contains '>' itself.
		if k := strings.Index(lower[start+4:], "-->"); k >= 0 {
			return start + 4 + k + 3
		}
		return len(s)
	}
	name, closing := oldTagName(lower[start+1 : end])
	switch name {
	case "script", "style":
		if closing {
			return next
		}
		// Skip to the matching close tag; an unclosed element drops the rest.
		if k := strings.Index(lower[next:], "</"+name); k >= 0 {
			if e := strings.IndexByte(lower[next+k:], '>'); e >= 0 {
				return next + k + e + 1
			}
		}
		return len(s)
	case "td", "th":
		*space = true
	case "br", "li", "p", "div", "ul", "ol", "dl", "dt", "dd", "tr", "table",
		"h1", "h2", "h3", "h4", "h5", "h6", "section", "article", "header",
		"footer", "blockquote", "pre", "hr", "nav", "aside", "form", "fieldset",
		"figure", "figcaption", "main", "address", "details", "summary":
		// One break per run of block tags, and none at the start: nested
		// lists and adjacent paragraphs must not stack blank lines.
		if n := b.Len(); n > 0 && b.String()[n-1] != '\n' {
			b.WriteByte('\n')
		}
		*space = false
	}
	return next
}

// tagName returns the lower-case element name of the tag body (the text
// between '<' and '>') and whether it is a closing tag.
func oldTagName(body string) (name string, closing bool) {
	if strings.HasPrefix(body, "/") {
		closing = true
		body = body[1:]
	}
	n := 0
	for n < len(body) && (oldIsAlnum(body[n])) {
		n++
	}
	return body[:n], closing
}
func oldIsAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}
func oldASCIILower(s string) string {
	buf := []byte(s)
	for i, c := range buf {
		if c >= 'A' && c <= 'Z' {
			buf[i] = c + 'a' - 'A'
		}
	}
	return string(buf)
}
