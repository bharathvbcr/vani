package track

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// This file is the one matcher behind Words.Found and Words.Redact (and so
// Check and every text bound for a provider). Both fold the text the same way,
// find terms with the same span finder, and differ only in what they do with
// the spans, so they cannot disagree about what counts as a term.
//
// Folding. Matching runs on a folded copy of the text; every folded rune keeps
// the byte span it came from, so Redact cuts the original bytes and copies the
// rest unchanged. The fold drops invisible characters (format characters such
// as zero-width space, joiner and soft hyphen; control characters; combining
// marks, which is what makes a decomposed accent equal its precomposed twin),
// maps every kind of space to one space (runs collapse), every dash to '-',
// fullwidth forms, math alphanumerics and circled letters to ASCII, folds case
// by the upper-then-lower mapping (so U+0130, U+0131, U+017F and the Kelvin sign
// land on i, i, s, k), strips the accents of Latin letters from a bounded table,
// and maps the common Cyrillic and Greek look-alikes of Latin letters to the
// Latin letter. golang.org/x/text is not a dependency, so there is no general
// NFC: a precomposed letter outside the table is compared as itself, and only a
// decomposed spelling of one is not caught (the residual named in the tests).
//
// Two kinds of term. A built-in term is a whole word: the folded text on each
// side must not be an ASCII letter. An owner term (the owner's own list, which
// the owner has said must not leave the machine at all) is matched loosely: its
// separators (space, hyphen, underscore, dot, slash) are optional runs of
// separators in the text, at the word joins of the term always and, for a term
// of five letters or more, between any two letters but not by a space ("Zephyr-net"); a
// continuation after the term counts (a plural, a suffix, "...App"), for a term
// of five letters or more; a term of four or fewer letters takes only a plural or
// possessive, or a capital that starts a new word ("MarkApp"), never the start of
// a longer word ("Markdown"); and the
// term may follow a lowercase letter where it starts a capital ("myZephyrnet").
// A short term thus stays a whole word, so a three-letter listed name does not
// eat every word that starts with it.

type folded struct {
	r     []rune // folded runes
	start []int  // byte offset in the original of the rune r[i] came from
	end   []int  // byte offset past it and any dropped characters that follow it
	upper []bool // the source rune was an upper-case letter
}

// placeholder is the folded rune standing for an existing "[redacted]" in the text:
// not a letter, not a separator, so a term next to it sees a boundary and nothing
// matches inside it (a listed "reda" is a prefix of the placeholder's own word).
const placeholder = rune(1)

// foldString folds text; foldTerm folds a listed term, which has no placeholder.
func foldString(s string) folded { return fold(s, true) }
func foldTerm(s string) folded   { return fold(s, false) }

// Blank reports whether s is empty after the matcher's fold: nothing, or only
// spaces and characters the fold drops (zero-width and other format characters,
// control characters, combining marks). It is the one test for "this text is
// empty", so a summary of invisible characters is treated as unset everywhere
// instead of as a target that says nothing.
func Blank(s string) bool {
	for _, r := range foldString(s).r {
		if r != ' ' {
			return false
		}
	}
	return true
}

func fold(s string, mask bool) folded {
	f := folded{
		r: make([]rune, 0, len(s)), start: make([]int, 0, len(s)),
		end: make([]int, 0, len(s)), upper: make([]bool, 0, len(s)),
	}
	for i := 0; i < len(s); {
		if mask && s[i] == '[' && strings.HasPrefix(s[i:], Redacted) {
			f.r = append(f.r, placeholder)
			f.start = append(f.start, i)
			f.end = append(f.end, i+len(Redacted))
			f.upper = append(f.upper, false)
			i += len(Redacted)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		out, n, up := foldRune(r)
		if n == 0 {
			if k := len(f.r) - 1; k >= 0 {
				f.end[k] = i + size
			}
			i += size
			continue
		}
		for _, o := range out[:n] {
			if k := len(f.r) - 1; o == ' ' && k >= 0 && f.r[k] == ' ' {
				f.end[k] = i + size
				continue
			}
			f.r = append(f.r, o)
			f.start = append(f.start, i)
			f.end = append(f.end, i+size)
			f.upper = append(f.upper, up)
		}
		i += size
	}
	return f
}

// foldRune folds one rune into at most three (n of them in out; n 0 drops it).
// upper says the source was an upper-case letter.
func foldRune(r rune) (out [3]rune, n int, upper bool) {
	one := func(c rune) ([3]rune, int, bool) { return [3]rune{c}, 1, upper }
	switch {
	case r < utf8.RuneSelf:
		switch {
		case r >= 'A' && r <= 'Z':
			return [3]rune{r + 'a' - 'A'}, 1, true
		case r == ' ' || (r >= '\t' && r <= '\r'):
			return [3]rune{' '}, 1, false
		case r < ' ' || r == 0x7f:
			return out, 0, false
		}
		return [3]rune{r}, 1, false
	case unicode.IsSpace(r):
		return [3]rune{' '}, 1, false
	case unicode.In(r, unicode.Cf, unicode.Mn, unicode.Me, unicode.Cc):
		return out, 0, false
	case r == 0x2018 || r == 0x2019 || r == 0x201b || r == 0x2032 || r == 0x02bc || r == 0x02b9 || r == 0x02c8:
		return [3]rune{'\''}, 1, false // every apostrophe and prime is the ASCII one
	case r == 0x2044 || r == 0x2215: // fraction slash, division slash
		return [3]rune{'/'}, 1, false
	case unicode.Is(unicode.Pd, r) || r == 0x2212:
		return [3]rune{'-'}, 1, false
	case r >= 0xff01 && r <= 0xff5e: // fullwidth ASCII
		return foldRune(r - 0xfee0)
	case r >= 0x1d400 && r <= 0x1d6a3: // mathematical letters, 52 to a style
		k := (r - 0x1d400) % 52
		if k < 26 {
			return [3]rune{'a' + k}, 1, true
		}
		return [3]rune{'a' + k - 26}, 1, false
	case r >= 0x1d7ce && r <= 0x1d7ff: // mathematical digits
		return [3]rune{'0' + (r-0x1d7ce)%10}, 1, false
	case r >= 0x24b6 && r <= 0x24cf: // circled capitals
		return [3]rune{'a' + r - 0x24b6}, 1, true
	case r >= 0x24d0 && r <= 0x24e9: // circled small letters
		return [3]rune{'a' + r - 0x24d0}, 1, false
	}
	switch r {
	case 0xdf, 0x1e9e:
		return [3]rune{'s', 's'}, 2, r == 0x1e9e
	case 0xe6, 0xc6:
		return [3]rune{'a', 'e'}, 2, r == 0xc6
	case 0x153, 0x152:
		return [3]rune{'o', 'e'}, 2, r == 0x152
	case 0x133, 0x132:
		return [3]rune{'i', 'j'}, 2, r == 0x132
	case 0xfe, 0xde:
		return [3]rune{'t', 'h'}, 2, r == 0xde
	case 0xfb00:
		return [3]rune{'f', 'f'}, 2, false
	case 0xfb01:
		return [3]rune{'f', 'i'}, 2, false
	case 0xfb02:
		return [3]rune{'f', 'l'}, 2, false
	case 0xfb03:
		return [3]rune{'f', 'f', 'i'}, 3, false
	case 0xfb04:
		return [3]rune{'f', 'f', 'l'}, 3, false
	case 0xfb05, 0xfb06:
		return [3]rune{'s', 't'}, 2, false
	}
	upper = unicode.IsUpper(r)
	c := unicode.ToLower(unicode.ToUpper(r))
	if b, ok := latinBase[c]; ok {
		return one(b)
	}
	if b, ok := extraLookalikes[c]; ok {
		return one(b)
	}
	if b, ok := lookalike[c]; ok {
		return one(b)
	}
	return one(c)
}

// latinBase maps a lower-case precomposed Latin letter (Latin-1 Supplement,
// Latin Extended-A, the common Latin Extended-B and Extended Additional ones,
// Vietnamese included) to its base ASCII letter. It is a bounded table, not
// NFC: a combining mark is dropped by the fold, so a decomposed letter needs no
// entry; this is what makes the precomposed twin fold the same.
var latinBase = func() map[rune]rune {
	m := map[rune]rune{}
	for base, variants := range map[rune]string{
		'a': "àáâãäåāăąǎǟǡǻȁȃȧḁạảấầẩẫậắằẳẵặ",
		'b': "ḃḅḇ",
		'c': "çćĉċčḉ",
		'd': "ďđḋḍḏḑḓ",
		'e': "èéêëēĕėęěȅȇȩḕḗḙḛḝẹẻẽếềểễệ",
		'f': "ḟ",
		'g': "ĝğġģǧǵḡ",
		'h': "ĥħȟḣḥḧḩḫẖ",
		'i': "ìíîïĩīĭįıǐȉȋḭḯỉị",
		'j': "ĵǰ",
		'k': "ķĸǩḱḳḵ",
		'l': "ĺļľŀłḷḹḻḽ",
		'm': "ḿṁṃ",
		'n': "ñńņňŉŋǹṅṇṉṋ",
		'o': "òóôõöøōŏőǒǫǭǿȍȏȫȭȯȱṍṏṑṓọỏốồổỗộớờởỡợ",
		'p': "ṕṗ",
		'r': "ŕŗřȑȓṙṛṝṟ",
		's': "śŝşšſșṡṣṥṧṩ",
		't': "ţťŧțṫṭṯṱẗ",
		'u': "ùúûüũūŭůűųǔǖǘǚǜȕȗṳṵṷṹṻụủứừửữự",
		'v': "ṽṿ",
		'w': "ŵẁẃẅẇẉẘ",
		'x': "ẋẍ",
		'y': "ýÿŷȳẏẙỳỵỷỹ",
		'z': "źżžẑẓẕ",
	} {
		for _, v := range variants {
			m[v] = base
		}
	}
	m['ð'] = 'd'
	return m
}()

// lookalike maps lower-case Cyrillic, Greek and a few other letters that are
// drawn like a Latin letter to that letter.
var lookalike = func() map[rune]rune {
	m := map[rune]rune{}
	for base, variants := range map[rune]string{
		'a': "аαɑ",
		'b': "в",
		'c': "сⅽ",
		'd': "ԁⅾ",
		'e': "еёε",
		'g': "ɡ",
		'h': "һн",
		'i': "іιⅰ",
		'j': "ј",
		'k': "кκ",
		'l': "ӏⅼ",
		'm': "мⅿ",
		'o': "оο",
		'p': "рρ",
		'q': "ԛ",
		's': "ѕ",
		't': "тτ",
		'u': "υ",
		'v': "ν",
		'w': "ԝω",
		'x': "хχ",
		'y': "у",
	} {
		for _, v := range variants {
			m[v] = base
		}
	}
	return m
}()

func isASCIILetter(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }

// isSep reports a folded rune that may stand between the words of an owner term.
func isSep(r rune) bool {
	switch r {
	case ' ', '-', '_', '.', '/', '\'', '(', ')', ':', '+', '·':
		return true
	}
	return false
}

// extraLookalikes maps lower-case Latin Extended-B and IPA letters that are
// plain Latin letters drawn with a bar, hook, tail or turned (U+0180-U+024F and
// the IPA block) to that Latin letter. A bounded table, like latinBase.
var extraLookalikes = map[rune]rune{
	'ƀ': 'b', 'ƃ': 'b', 'ɓ': 'b', 'ƈ': 'c', 'ȼ': 'c', 'ƌ': 'd', 'ɗ': 'd',
	'ǝ': 'e', 'ɇ': 'e', 'ɘ': 'e', 'ə': 'e', 'ƒ': 'f', 'ǥ': 'g', 'ɠ': 'g',
	'ɦ': 'h', 'ɨ': 'i', 'ȷ': 'j', 'ɉ': 'j', 'ƚ': 'l', 'ɫ': 'l', 'ɱ': 'm',
	'ƞ': 'n', 'ȵ': 'n', 'ɲ': 'n', 'ɳ': 'n', 'ɵ': 'o', 'ƥ': 'p', 'ɋ': 'q',
	'ɍ': 'r', 'ɽ': 'r', 'ȿ': 's', 'ȶ': 't', 'ƫ': 't', 'ƭ': 't', 'ʈ': 't',
	'ʉ': 'u', 'ƴ': 'y', 'ɏ': 'y', 'ƶ': 'z', 'ȥ': 'z', 'ɀ': 'z', 'ʐ': 'z', 'ʑ': 'z',
}

// Which separators may stand before an item of an owner term. gapJoin is a word
// join the term itself lists (a space or hyphen in "decode-latency"): any run of
// separators. gapIntra is inside a word of a term of five letters or more
// ("Zephyr-net"): hyphens, underscores, dots and slashes, but not spaces, so
// "met all" is not "metal".
const (
	gapNone uint8 = iota
	gapIntra
	gapJoin
)

// term is one compiled term.
type term struct {
	raw     string  // as listed (lower-cased), for reports
	items   []rune  // folded runes; for an owner term, without separators
	gap     []uint8 // owner term: which separators may precede items[k] (gapNone, gapIntra, gapJoin)
	loose   bool    // an owner term
	letters int     // owner term: count of items
}

// newOwnerTerm compiles one owner term. ok is false when nothing visible is left.
func newOwnerTerm(raw string) (t *term, ok bool) {
	f := foldTerm(raw)
	t = &term{raw: raw, loose: true}
	pending := false
	for _, c := range f.r {
		if isSep(c) {
			pending = true
			continue
		}
		g := uint8(gapNone)
		if pending && len(t.items) > 0 {
			g = gapJoin
		}
		t.items = append(t.items, c)
		t.gap = append(t.gap, g)
		pending = false
	}
	if len(t.items) == 0 {
		return nil, false
	}
	t.letters = len(t.items)
	if t.letters >= 5 {
		for k := 1; k < len(t.gap); k++ {
			if t.gap[k] == gapNone {
				t.gap[k] = gapIntra
			}
		}
	}
	return t, true
}

func newBuiltinTerm(raw string) *term {
	f := foldTerm(raw)
	if len(f.r) == 0 {
		return nil
	}
	return &term{raw: raw, items: f.r}
}

// matchAt returns the index in f.r just past a match of t starting at i, or -1.
func (t *term) matchAt(f *folded, i int) int {
	r := f.r
	if !t.loose {
		if i > 0 && isASCIILetter(r[i-1]) {
			return -1
		}
		j := i
		for _, c := range t.items {
			if (c == ' ' || c == '-') && j < len(r) && (r[j] == ' ' || r[j] == '-') {
				// A separator inside a built-in term is a space or a hyphen
				// (the fold already makes a no-break space and every dash one of
				// those), and a run of them: "green card", "green-card", "green - card".
				for j < len(r) && (r[j] == ' ' || r[j] == '-') {
					j++
				}
				continue
			}
			if j >= len(r) || r[j] != c {
				return -1
			}
			j++
		}
		if j < len(r) && isASCIILetter(r[j]) {
			return -1
		}
		return j
	}
	if isASCIILetter(t.items[0]) && i > 0 && isASCIILetter(r[i-1]) && !(f.upper[i] && !f.upper[i-1]) {
		return -1 // inside a word, except where a capital starts a camelCase word
	}
	j := i
	for k, c := range t.items {
		switch t.gap[k] {
		case gapJoin:
			for j < len(r) && isSep(r[j]) {
				j++
			}
		case gapIntra:
			for j < len(r) && isSep(r[j]) && r[j] != ' ' {
				j++
			}
		}
		if j >= len(r) || r[j] != c {
			return -1
		}
		j++
	}
	if isASCIILetter(t.items[len(t.items)-1]) && j < len(r) && isASCIILetter(r[j]) {
		// A capital starts a word after a lower-case letter ("ZephyrnetApp") or
		// before a lower-case one ("MITLab").
		camel := f.upper[j] && (!f.upper[j-1] || j+1 < len(r) && isASCIILetter(r[j+1]) && !f.upper[j+1])
		plural := r[j] == 's' && (j+1 == len(r) || !isASCIILetter(r[j+1])) ||
			r[j] == 'e' && j+2 <= len(r) && r[j+1] == 's' && (j+2 == len(r) || !isASCIILetter(r[j+2]))
		if !camel && !plural && t.letters < 5 {
			return -1
		}
	}
	return j
}

// compiled is a word list ready to scan.
type compiled struct {
	terms   []*term
	byFirst map[rune][]*term
	at      bool // the built-in "@": a token holding one goes whole
}

func compile(builtin, owner []string) *compiled {
	c := &compiled{byFirst: map[rune][]*term{}}
	add := func(t *term) {
		c.terms = append(c.terms, t)
		c.byFirst[t.items[0]] = append(c.byFirst[t.items[0]], t)
	}
	for _, b := range builtin {
		if b == "@" {
			c.at = true
			continue
		}
		if t := newBuiltinTerm(b); t != nil {
			add(t)
		}
	}
	for _, o := range owner {
		if t, ok := newOwnerTerm(o); ok {
			add(t)
		}
	}
	return c
}

type span struct{ a, b int } // byte offsets in the original text

// found returns the raw terms occurring in f, each once, in list order, and
// whether an '@' occurs.
func (c *compiled) found(f *folded) (terms []string, at bool) {
	hit := map[*term]bool{}
	for i, r := range f.r {
		if r == '@' {
			at = true
		}
		for _, t := range c.byFirst[r] {
			if !hit[t] && t.matchAt(f, i) > 0 {
				hit[t] = true
			}
		}
	}
	for _, t := range c.terms {
		if hit[t] {
			terms = append(terms, t.raw)
		}
	}
	return terms, at && c.at
}

// spans returns the byte spans of s to remove: at each position the longest
// term match, then the whole whitespace-delimited token around each '@' when the
// list has the built-in "@"; overlapping spans are merged.
func (c *compiled) spans(s string, f *folded) []span {
	var out []span
	for i := 0; i < len(f.r); {
		best := -1
		for _, t := range c.byFirst[f.r[i]] {
			if e := t.matchAt(f, i); e > best {
				best = e
			}
		}
		if best > i {
			out = append(out, span{f.start[i], f.end[best-1]})
			i = best
			continue
		}
		i++
	}
	if c.at {
		tokEnd := 0 // each token is expanded once: an "@" inside the last one is covered
		for k, r := range f.r {
			if r != '@' || f.start[k] < tokEnd {
				continue
			}
			a, b := f.start[k], f.end[k]
			for a > 0 {
				p, size := utf8.DecodeLastRuneInString(s[:a])
				if unicode.IsSpace(p) {
					break
				}
				a -= size
			}
			for b < len(s) {
				n, size := utf8.DecodeRuneInString(s[b:])
				if unicode.IsSpace(n) {
					break
				}
				b += size
			}
			tokEnd = b
			out = append(out, span{a, b})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].a < out[j].a })
	merged := out[:0]
	for _, sp := range out {
		if k := len(merged) - 1; k >= 0 && sp.a < merged[k].b {
			merged[k].b = max(merged[k].b, sp.b)
			continue
		}
		merged = append(merged, sp)
	}
	return merged
}

// redactOnce replaces each span of s with Redacted.
func (c *compiled) redactOnce(s string) (string, int) {
	f := foldString(s)
	sp := c.spans(s, &f)
	if len(sp) == 0 {
		return s, 0
	}
	var b strings.Builder
	pos := 0
	for _, x := range sp {
		b.WriteString(s[pos:x.a])
		b.WriteString(Redacted)
		pos = x.b
	}
	b.WriteString(s[pos:])
	return b.String(), len(sp)
}

// maxRedactPasses bounds the repeat Redact makes when removing a term brings
// its neighbours together into a new one ("zeph" + term + "yrnet").
const maxRedactPasses = 4
