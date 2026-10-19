package page

import (
	"html"
	"strings"

	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
)

// link is one <a href> found in a page: the raw, unescaped href and the anchor
// text (a title fallback).
type link struct {
	href string
	text string
}

// scanned is everything one pass over a page yields.
type scanned struct {
	links []link
	// ldJSON are the bodies of <script type="application/ld+json"> blocks.
	ldJSON []string
	// title is the plain text of the first <title>.
	title string
	// bodyStart is the offset just past the <body ...> tag, or 0.
	bodyStart int
	// baseHref is the raw, unescaped href of the first <base> that has one,
	// as HTML uses it: relative links resolve against it, not the page URL.
	baseHref string
	hasBase  bool
}

const (
	maxLDBlocks   = 20
	maxAnchorText = 2000 // bytes of source scanned for one anchor's text
)

// scanHTML is a small forgiving tokenizer, not a parser. It only needs to find
// anchors, JSON-LD blocks and the title in a DOM dump, and to never mistake
// the inside of a script, style or comment for markup. Page content is
// untrusted: nothing here is executed, and an unterminated construct ends the
// scan rather than being guessed at. Work is linear in len(doc).
func scanHTML(doc string) scanned {
	var out scanned
	for i := 0; i < len(doc); {
		j := strings.IndexByte(doc[i:], '<')
		if j < 0 {
			break
		}
		i += j
		if strings.HasPrefix(doc[i:], "<!--") {
			end := strings.Index(doc[i+4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		if i+1 >= len(doc) || !isLetter(doc[i+1]) {
			i++
			continue
		}
		k := i + 1
		for k < len(doc) && isNameByte(doc[k]) {
			k++
		}
		name := htmltext.LowerASCII(doc[i+1 : k])
		attrs, end, ok := parseAttrs(doc, k, name == "a" || name == "base" || name == "script")
		if !ok {
			break // unterminated tag or quote: stop rather than guess
		}
		switch name {
		case "body":
			if out.bodyStart == 0 {
				out.bodyStart = end
			}
		case "base":
			if href, found := attrs["href"]; found && !out.hasBase {
				out.baseHref, out.hasBase = html.UnescapeString(href), true
			}
		case "a":
			if href, found := attrs["href"]; found {
				stop := end + maxAnchorText
				if stop > len(doc) {
					stop = len(doc)
				}
				text := doc[end:stop]
				if c := htmltext.IndexFold(doc[end:stop], "</a"); c >= 0 {
					text = doc[end : end+c]
				}
				out.links = append(out.links, link{href: html.UnescapeString(href), text: htmltext.Text(text, 200)})
			}
		case "script", "style", "textarea", "title":
			closeAt := htmltext.IndexFold(doc[end:], "</"+name)
			if closeAt < 0 {
				return out
			}
			body := doc[end : end+closeAt]
			switch {
			case name == "script" && len(out.ldJSON) < maxLDBlocks &&
				strings.EqualFold(strings.TrimSpace(attrs["type"]), "application/ld+json"):
				out.ldJSON = append(out.ldJSON, body)
			case name == "title" && out.title == "":
				out.title = htmltext.Text(body, 300)
			}
			// Resume at the closing tag so its own ">" is consumed normally.
			end += closeAt
		}
		i = end
	}
	return out
}

// parseAttrs reads attributes from pos up to the tag's ">", returning them by
// lowercase name (first occurrence wins, as in HTML) and the offset just past
// the ">". ok is false when the tag or a quoted value never ends. With keep
// false the tag is only skipped: attrs is nil and nothing is allocated, since
// most tags carry no attribute the scan reads.
func parseAttrs(doc string, pos int, keep bool) (attrs map[string]string, end int, ok bool) {
	if keep {
		attrs = map[string]string{}
	}
	i := pos
	for i < len(doc) {
		for i < len(doc) && (isSpace(doc[i]) || doc[i] == '/') {
			i++
		}
		if i >= len(doc) {
			return nil, 0, false
		}
		if doc[i] == '>' {
			return attrs, i + 1, true
		}
		start := i
		for i < len(doc) && !isSpace(doc[i]) && doc[i] != '=' && doc[i] != '>' && doc[i] != '/' {
			i++
		}
		if i == start { // a stray "=" or similar; step over it
			i++
			continue
		}
		key := doc[start:i]
		for i < len(doc) && isSpace(doc[i]) {
			i++
		}
		val := ""
		if i < len(doc) && doc[i] == '=' {
			i++
			for i < len(doc) && isSpace(doc[i]) {
				i++
			}
			if i >= len(doc) {
				return nil, 0, false
			}
			if q := doc[i]; q == '"' || q == '\'' {
				e := strings.IndexByte(doc[i+1:], q)
				if e < 0 {
					return nil, 0, false
				}
				val = doc[i+1 : i+1+e]
				i += e + 2
			} else {
				s := i
				for i < len(doc) && !isSpace(doc[i]) && doc[i] != '>' {
					i++
				}
				val = doc[s:i]
			}
		}
		if !keep {
			continue
		}
		key = htmltext.LowerASCII(key)
		if _, dup := attrs[key]; !dup {
			attrs[key] = val
		}
	}
	return nil, 0, false
}

func isLetter(c byte) bool { return c|0x20 >= 'a' && c|0x20 <= 'z' }
func isNameByte(c byte) bool {
	return isLetter(c) || c >= '0' && c <= '9' || c == '-' || c == ':' || c == '_'
}
func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }
