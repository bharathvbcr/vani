package kbsync

import "strings"

// rkb writes titles, orgs and stacks in a light markdown: links "[text](url)",
// "**bold**" and "*italic*". DevPrism prints a title or org as a plain literal
// (career_match/render.rs entry), so the markup reached the PDF raw:
// "[Chronicle](https://github…" did. PlainText removes it, dropping a link's
// url: a heading has no room for one.
//
// Bullets are not handled here. ResolveText renders them as rkb's text
// renderer does, keeping a link's url in parentheses.
//
// Every pass is linear in the input, whatever its nesting.

// PlainText is s without links and emphasis, whitespace collapsed.
func PlainText(s string) string {
	s = stripBold(stripItalic(stripLinks(s)))
	return strings.Join(strings.Fields(s), " ")
}

// stripLinks replaces "[text](url)" and "![alt](src)" with text or alt. The
// url may hold balanced parentheses; the text may hold other links (an image
// inside a link), which are stripped too. Anything that does not close is
// kept as written.
func stripLinks(s string) string {
	closeSq := matchPairs(s, '[', ']')
	closePar := matchPairs(s, '(', ')')
	type jump struct{ from, to int }
	var pending []jump // innermost last; a link's text lies inside its parent's
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if n := len(pending); n > 0 && i == pending[n-1].from {
			i = pending[n-1].to
			pending = pending[:n-1]
			continue
		}
		open := i
		if s[i] == '!' && i+1 < len(s) && s[i+1] == '[' {
			open = i + 1
		}
		if s[open] == '[' {
			if cl := closeSq[open]; cl > 0 && cl+1 < len(s) && s[cl+1] == '(' {
				// A link inside another link's text must end inside that text;
				// crossed pairs, as in "[a [b](c ] d)](e)", are not links.
				if end := closePar[cl+1]; end > 0 && (len(pending) == 0 || end < pending[len(pending)-1].from) {
					pending = append(pending, jump{from: cl, to: end + 1})
					i = open + 1
					continue
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// matchPairs maps the index of each opening delimiter to the index of the one
// that closes it (0 when none does). A closer with no opener is ignored.
func matchPairs(s string, open, close byte) map[int]int {
	m := map[int]int{}
	var stack []int
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case open:
			stack = append(stack, i)
		case close:
			if n := len(stack); n > 0 {
				m[stack[n-1]] = i
				stack = stack[:n-1]
			}
		}
	}
	return m
}

// starRun is the length of the run of '*' starting at i.
func starRun(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == '*' {
		n++
	}
	return n
}

// stripItalic replaces "*x*" with x. A run of two or more stars is left alone
// for stripBold, so "**bold**" is not split. As in markdown, the opener
// must be followed and the closer preceded by a non-space, which keeps
// "5 * 3 * 2" as written.
func stripItalic(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '*' {
			b.WriteByte(s[i])
			i++
			continue
		}
		run := starRun(s, i)
		if run > 1 {
			b.WriteString(s[i : i+run])
			i += run
			continue
		}
		next := strings.IndexByte(s[i+1:], '*')
		if next > 0 {
			p := i + 1 + next
			if starRun(s, p) == 1 && s[i+1] != ' ' && s[p-1] != ' ' {
				b.WriteString(s[i+1 : p])
				i = p + 1
				continue
			}
		}
		b.WriteByte('*')
		i++
	}
	return b.String()
}

// stripBold replaces "**x**" with x, for text DevPrism prints as a literal.
// An unmatched "**" is kept.
func stripBold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "**") {
			if end := strings.Index(s[i+2:], "**"); end > 0 {
				b.WriteString(s[i+2 : i+2+end])
				i += 2 + end + 2
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
