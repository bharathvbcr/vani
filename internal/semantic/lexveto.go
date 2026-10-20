package semantic

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"
)

// A lexical veto on the "likely" tier. An embedding scores two labels that
// differ by one content word ("First name" / "Last name", 0.9646) above many
// true rewordings, so a cosine alone cannot be trusted to merge. TierPair
// demotes such a pair from "likely" to "possible": a person looks at it, nothing
// is merged. The veto only ever lowers a tier.
//
// The tokens are the Mac app's and extension's own (extension/src/normalize.ts
// tokens()): the same stopwords (which drop url, link, profile, number, handle),
// phrase folding and light stemmer, so a pair this code calls one-word-apart is
// one-word-apart to the concept key too. extension/test/vectors/tokens.json is
// generated from the TypeScript and replayed here and by bun test
// (extension/test/tokens.test.ts); neither side can drift without a test failing.
//
// Known gap: JavaScript's NFKC is approximated by foldCompat (full-width forms,
// the ideographic and no-break spaces, f-ligatures, superscript digits), and
// Go's lower-casing has no context-sensitive final sigma. A label that leans on
// another compatibility character may tokenize differently here than in the
// extension; the veto then sees different tokens, which can only move a pair
// between "likely" and "possible", never merge anything.

var lexStopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`a an the your you yours do does are is be of to in on
		for and or please enter provide what which if any this that
		with at by as me my we our us will would can have has
		url link profile page number handle here`) {
		m[w] = true
	}
	return m
}()

// jsSpace is JavaScript's \s: ASCII whitespace, no-break and Unicode spaces, BOM.
const jsSpace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var (
	requiredMarks = regexp.MustCompile(`(?i)\([` + jsSpace + `]*(required|optional)[` + jsSpace + `]*\)|\*|†`)
	nonWord       = regexp.MustCompile(`[^\p{L}\p{N}]+`)
	phrases       = []struct {
		re *regexp.Regexp
		to string
	}{
		{regexp.MustCompile(`\b(the )?united states( of america)?\b|\bu s a\b|\busa\b|\bthe us\b|\bu s\b`), " usa "},
		{regexp.MustCompile(`\be mail\b`), " email "},
		{regexp.MustCompile(`\bmobile\b|\bcell( phone)?\b`), " phone "},
	}
)

// foldCompat is the part of Unicode NFKC that job-form labels use.
func foldCompat(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 0xff01 && r <= 0xff5e: // full-width ASCII
			b.WriteRune(r - 0xfee0)
		case r == 0xa0 || r == 0x3000 || (r >= 0x2000 && r <= 0x200a) || r == 0x202f || r == 0x205f:
			b.WriteByte(' ')
		case r == 0xfb00:
			b.WriteString("ff")
		case r == 0xfb01:
			b.WriteString("fi")
		case r == 0xfb02:
			b.WriteString("fl")
		case r == 0xfb03:
			b.WriteString("ffi")
		case r == 0xfb04:
			b.WriteString("ffl")
		case r == 0xb9:
			b.WriteByte('1')
		case r == 0xb2:
			b.WriteByte('2')
		case r == 0xb3:
			b.WriteByte('3')
		case r == 0x2070:
			b.WriteByte('0')
		case r >= 0x2074 && r <= 0x2079:
			b.WriteRune('4' + (r - 0x2074))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normalizeLabel is normalize.ts normalizeLabel: marks and punctuation gone,
// lower case, single spaces, at most 200 UTF-16 units.
func normalizeLabel(s string) string {
	s = foldCompat(s)
	s = requiredMarks.ReplaceAllString(s, " ")
	s = strings.Map(func(r rune) rune {
		if r == '’' || r == '\'' {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
	s = nonWord.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	units := 0
	for i, r := range s {
		n := 1
		if r > 0xffff {
			n = 2
		}
		if units+n > 200 {
			return s[:i]
		}
		units += n
	}
	return s
}

// stem is normalize.ts stem: a light plural/verb-ending stem.
func stem(w string) string {
	n := len(utf16.Encode([]rune(w)))
	if n <= 3 {
		return w
	}
	for _, suf := range []string{"ization", "ation", "ship", "ing", "ed", "es", "s"} {
		if strings.HasSuffix(w, suf) && n-len(suf) >= 3 {
			return w[:len(w)-len(suf)]
		}
	}
	return w
}

// labelTokens is normalize.ts tokens(): a label's content words, stemmed.
func labelTokens(s string) map[string]bool {
	n := normalizeLabel(s)
	for _, p := range phrases {
		n = p.re.ReplaceAllString(n, p.to)
	}
	out := map[string]bool{}
	for _, w := range strings.Fields(n) {
		if !lexStopwords[w] {
			out[stem(w)] = true
		}
	}
	return out
}

// contrastGroups are words that turn one question into another: the answers
// differ though the rest of the wording is shared.
var contrastGroups = [][]string{
	{"first", "last"},
	{"current", "previous", "former"},
	{"now", "future"},
	{"home", "work"},
	{"primary", "secondary"},
	{"legal", "preferred"},
	{"residence", "citizenship"},
}

var contrastWords = func() map[string]bool {
	m := map[string]bool{}
	for _, g := range contrastGroups {
		for _, w := range g {
			m[stem(w)] = true
		}
	}
	return m
}()

// LexicalVeto says why two labels must not be merged on a cosine alone, or "".
//
//   - "no content words": either label has no content token left, so nothing
//     says what it asks.
//   - "one-word substitution": the token sets have the same size and differ by
//     exactly one token on each side.
//   - "contrast word": one label has a word from a contrast group (first/last,
//     current/previous/former, now/future, home/work, primary/secondary,
//     legal/preferred, residence/citizenship) that the other lacks.
//
// Two wordings that normalize to the same text are never vetoed.
func LexicalVeto(a, b string) string {
	if normalizeLabel(a) == normalizeLabel(b) {
		return ""
	}
	ta, tb := labelTokens(a), labelTokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return "no content words"
	}
	onlyA, onlyB := 0, 0
	for t := range ta {
		if !tb[t] {
			onlyA++
			if contrastWords[t] {
				return "contrast word"
			}
		}
	}
	for t := range tb {
		if !ta[t] {
			onlyB++
			if contrastWords[t] {
				return "contrast word"
			}
		}
	}
	if len(ta) == len(tb) && onlyA == 1 && onlyB == 1 {
		return "one-word substitution"
	}
	return ""
}

// TierPair is Tier for a pair of labels: the cosine's band, with "likely"
// demoted to "possible" when LexicalVeto objects.
func TierPair(a, b string, cos float64) string {
	t := cosineTier(cos)
	if t == "likely" && LexicalVeto(a, b) != "" {
		return "possible"
	}
	return t
}
