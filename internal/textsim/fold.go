package textsim

import (
	"strings"
	"unicode"
)

// accentGroups lists the Latin letters whose accented forms fold to them. The
// standard library has no Unicode normalisation, and Words would cut a word
// at a combining mark, so a decomposed "Zoe" + U+0308 and a composed "Zoë"
// would otherwise read as two words. The table covers Latin-1 Supplement and
// Latin Extended-A; a letter outside it compares as itself.
var accentGroups = map[rune]string{
	'a': "àáâãäåāăą", 'c': "çćĉċč", 'd': "ďđ", 'e': "èéêëēĕėęě", 'g': "ĝğġģ", 'h': "ĥħ",
	'i': "ìíîïĩīĭįı", 'j': "ĵ", 'k': "ķ", 'l': "ĺļľŀł", 'n': "ñńņňŉ", 'o': "òóôõöøōŏő",
	'r': "ŕŗř", 's': "śŝşš", 't': "ţťŧ", 'u': "ùúûüũūŭůűų", 'w': "ŵ", 'y': "ýÿŷ", 'z': "źżž",
}

var accentBase = func() map[rune]rune {
	m := map[rune]rune{}
	for base, forms := range accentGroups {
		for _, f := range forms {
			m[f] = base
		}
	}
	return m
}()

// Fold lowercases s, maps full-width ASCII to ASCII, drops combining marks and
// folds the accents in accentGroups, so "São Paulo", "SAO PAULO" and a
// decomposed "Sa" + U+0303 + "o Paulo" compare equal. Spacing is left as it is.
func Fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 0xFF01 && r <= 0xFF5E: // full-width ! .. ~
			r -= 0xFEE0
			b.WriteRune(unicode.ToLower(r))
		case unicode.Is(unicode.Mn, r):
		default:
			if base, ok := accentBase[r]; ok {
				r = base
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}
