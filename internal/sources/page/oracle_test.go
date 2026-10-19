package page

import (
	"html"
	"strings"

	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
)

// oldScanHTML is scanHTML as it was before it stopped lowering a copy of the
// whole document, kept verbatim as the parity oracle.
func oldScanHTML(doc string) scanned {
	var out scanned
	lower := oldASCIILower(doc)
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
		name := lower[i+1 : k]
		attrs, end, ok := oldParseAttrs(doc, lower, k)
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
				if c := strings.Index(lower[end:stop], "</a"); c >= 0 {
					text = doc[end : end+c]
				}
				out.links = append(out.links, link{href: html.UnescapeString(href), text: htmltext.Text(text, 200)})
			}
		case "script", "style", "textarea", "title":
			closeAt := strings.Index(lower[end:], "</"+name)
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
func oldParseAttrs(doc, lower string, pos int) (attrs map[string]string, end int, ok bool) {
	attrs = map[string]string{}
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
		key := lower[start:i]
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
		if _, dup := attrs[key]; !dup {
			attrs[key] = val
		}
	}
	return nil, 0, false
}
func oldASCIILower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
