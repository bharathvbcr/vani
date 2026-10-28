package srcutil

import (
	"strings"

	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
)

// Section is one part of a posting's body as a board serves it: an HTML
// fragment, optionally under a plain-text heading the board names it by
// ("What you'll do", "Your mission").
type Section struct {
	Heading string
	HTML    string
}

// Sections reduces a board's body parts to plain text of at most room bytes:
// each part's HTML through htmltext.Text, its heading (also reduced as HTML,
// to one line) on the line above, and a blank line between parts. A
// part whose HTML reduces to nothing is dropped with its heading, so a board
// that serves an empty "Benefits" part does not leave a bare title. Reading
// stops once room is full.
func Sections(room int, parts ...Section) string {
	var b strings.Builder
	for _, p := range parts {
		if b.Len() >= room {
			break
		}
		text := htmltext.Text(p.HTML, room)
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if h := Title(htmltext.Text(p.Heading, MaxTitle)); h != "" {
			b.WriteString(h)
			b.WriteByte('\n')
		}
		b.WriteString(text)
	}
	return htmltext.Plain(b.String(), room)
}

// JoinDistinct joins the non-blank parts with sep, trimmed, each once
// (compared without case), in first-seen order.
func JoinDistinct(sep string, parts ...string) string {
	var out []string
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.Join(strings.Fields(p), " ")
		if k := strings.ToLower(p); p != "" && !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}
