package fieldtriage

import (
	"html"
	"regexp"
	"strings"
	"unicode"
)

// tagRE is anything shaped like an HTML tag. Label text is copied from page
// source, and a tag may sit inside a word ("citi<b>zen</b>").
var tagRE = regexp.MustCompile(`</?[A-Za-z!][^<>]*>`)

// maxEntityPasses bounds repeated entity decoding ("&amp;lt;" is two layers).
const maxEntityPasses = 3

// folds normalizes a label for the guard patterns and returns every reading
// the patterns must be tried against. A reading is built from:
//
//   - HTML entities decoded, repeated until stable (at most maxEntityPasses);
//   - fullwidth forms, circled and mathematical letters mapped to ASCII;
//   - every Unicode format character (category Cf: zero-width, bidi marks and
//     embeddings, soft hyphen, tag characters) and every combining mark
//     (category M) dropped, plus a few blank fillers that are not Cf;
//   - precomposed Latin letters with diacritics mapped to their base letter;
//   - any Unicode space collapsed to one ASCII space.
//
// Two choices are not decidable from the text, so both readings are returned:
// an invisible character may sit inside a word ("Ci<ZWSP>tizen") or stand in
// for a space ("to<ZWSP>work"), and a tag may join text ("citi<b>zen") or
// separate it ("a<br>citizen"; the tag is then just punctuation). Four
// readings, each one linear pass: the cost stays linear in the label. Readings
// that came out identical (a label with no invisible character and no tag, the
// usual case) are returned once, so the patterns scan the text once, not four
// times; the dropped copies could not change any pattern's answer.
func folds(label string) []string {
	for i := 0; i < maxEntityPasses; i++ {
		next := html.UnescapeString(label)
		if next == label {
			break
		}
		label = next
	}
	out := make([]string, 0, 4)
	for _, v := range [4][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
		s := fold(label, v[0], v[1])
		dup := false
		for _, o := range out {
			if o == s {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, s)
		}
	}
	return out
}

func fold(label string, invisibleIsSpace, joinTags bool) string {
	var b strings.Builder
	b.Grow(len(label))
	for _, r := range label {
		switch {
		case unicode.IsSpace(r) || r == 0x3000:
			b.WriteByte(' ')
		case unicode.Is(unicode.Cf, r) || isBlankFiller(r):
			if invisibleIsSpace {
				b.WriteByte(' ')
			}
		case unicode.Is(unicode.M, r):
			// attached to its base letter: never a separator
		default:
			b.WriteString(baseLetter(r))
		}
	}
	s := b.String()
	if joinTags {
		s = tagRE.ReplaceAllString(s, "")
	}
	return strings.Join(strings.Fields(s), " ")
}

// isBlankFiller reports characters that render as nothing but are not Cf:
// Hangul fillers and the braille blank.
func isBlankFiller(r rune) bool {
	switch r {
	case 0x115F, 0x1160, 0x3164, 0xFFA0, 0x2800:
		return true
	}
	return false
}

// latin1Upper and latin1Lower give the base letter of U+00C0..U+00DF and
// U+00E0..U+00FF; '-' marks a non-letter (x, division sign) left unchanged.
const (
	latin1Upper = "AAAAAAACEEEEIIIIDNOOOOO-OUUUUYTs"
	latin1Lower = "aaaaaaaceeeeiiiidnooooo-ouuuuyty"
	// latinExtA is U+0100..U+017F, one base letter per code point.
	latinExtA = "AaAaAaCcCcCcCcDdDdEeEeEeEeEeGgGgGgGgHhHhIiIiIiIiIiIiJjKkkLlLlLlLlLlNnNnNnnNnOoOoOoOoRrRrRrSsSsSsSsTtTtTtUuUuUuUuUuUuWwYyYZzZzZzs"
)

// baseLetter maps one rune to the ASCII text the guard patterns read it as,
// or returns it unchanged when it has no ASCII reading.
func baseLetter(r rune) string {
	switch {
	case r < 0x80:
		return string(r)
	case r >= 0xFF01 && r <= 0xFF5E: // fullwidth
		return string(r - 0xFEE0)
	case r == 0xC6:
		return "AE"
	case r == 0xE6:
		return "ae"
	case r == 0xDE:
		return "TH"
	case r == 0xFE:
		return "th"
	case r == 0xDF:
		return "ss"
	case r == 0x152:
		return "OE"
	case r == 0x153:
		return "oe"
	case r == 0x132:
		return "IJ"
	case r == 0x133:
		return "ij"
	case r >= 0xC0 && r <= 0xDF:
		return latinByte(latin1Upper[r-0xC0], r)
	case r >= 0xE0 && r <= 0xFF:
		return latinByte(latin1Lower[r-0xE0], r)
	case r >= 0x100 && r <= 0x17F:
		return string(latinExtA[r-0x100])
	case r == 0x251: // latin small alpha
		return "a"
	case r == 0x261: // latin small script g
		return "g"
	case r >= 0x24B6 && r <= 0x24CF: // circled capitals
		return string('A' + r - 0x24B6)
	case r >= 0x24D0 && r <= 0x24E9: // circled smalls
		return string('a' + r - 0x24D0)
	case r >= 0x1D400 && r <= 0x1D6A3: // mathematical alphanumeric letters
		i := (r - 0x1D400) % 52
		if i < 26 {
			return string('A' + i)
		}
		return string('a' + i - 26)
	case r >= 0x1D7CE && r <= 0x1D7FF: // mathematical digits
		return string('0' + (r-0x1D7CE)%10)
	}
	return string(r)
}

func latinByte(b byte, r rune) string {
	if b == '-' {
		return string(r)
	}
	return string(b)
}

// mixedScript reports whether any word of s mixes Latin letters with letters
// of a script that has Latin lookalikes (Cyrillic, Greek, Armenian, Cherokee,
// Coptic, Georgian). A person types "Gеnder" with a Cyrillic "е" only to get
// past a keyword guard, and a lookalike set this large is unbounded without a
// full confusables table. So the word is not decoded: the label fails closed
// to a manual answer, never a draft. A wrong pin costs one manual answer; a
// miss costs a fabricated answer to a sensitive question. Scripts that mix
// naturally with Latin (CJK, Hangul, Arabic, Hebrew, Indic, Thai) do not count.
func mixedScript(s string) bool {
	var latin, other bool
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Latin, r):
			latin = true
		case unicode.In(r, unicode.Cyrillic, unicode.Greek, unicode.Armenian,
			unicode.Cherokee, unicode.Coptic, unicode.Georgian):
			other = true
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			// a letter of an unrelated script: part of the word, neither kind
		default:
			latin, other = false, false
			continue
		}
		if latin && other {
			return true
		}
	}
	return false
}
