package llmbackend

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MarkerRemoved replaces anything Unmark takes for a frame marker.
const MarkerRemoved = "[marker removed]"

// markerRE is a frame marker as a reader sees it, once fold has run: two or more
// hyphens, begin or end, "untrusted content", two or more hyphens. It is matched
// on folded text only, so \s here is the one space fold writes.
var markerRE = regexp.MustCompile(`(?i)-{2,}\s*(begin|end)\s+untrusted\s+content\s*-{2,}`)

// foldRune is how a reader (or a model) reads r when it decides whether text is
// a frame marker. keep is false for characters that draw nothing: format
// characters (zero width space and joiners, soft hyphen, BOM, bidi controls,
// tags), combining marks and non-whitespace control characters. Any Unicode space
// or separator is one space, the full-width forms are their ASCII originals, and
// the dash family is a hyphen. Letters are left alone: markerRE's (?i) does the
// case folding, and look-alike letters of other scripts are not folded.
func foldRune(r rune) (out rune, keep bool) {
	switch {
	case unicode.IsSpace(r) || unicode.Is(unicode.Z, r):
		return ' ', true
	case unicode.Is(unicode.Cf, r), unicode.Is(unicode.Mn, r), unicode.Is(unicode.Me, r), unicode.Is(unicode.Cc, r):
		return 0, false
	case r >= 0xFF01 && r <= 0xFF5E:
		return foldDash(r - 0xFEE0), true
	}
	return foldDash(r), true
}

// foldDash maps the dashes and minus signs that draw like a hyphen to '-'.
func foldDash(r rune) rune {
	switch {
	case r >= 0x2010 && r <= 0x2015, r == 0x2212, r == 0xFE58, r == 0xFE63, r == 0xFF0D:
		return '-'
	}
	return r
}

// Unmark neutralises anything in s that could pass for a frame marker
// (BeginMarker or EndMarker): the markers themselves in any case, with any run
// of Unicode spaces, invisible characters, full-width forms or dash variants
// between and around their words. Each one becomes MarkerRemoved. Matching is on
// the folded text but the replaced span is the original one, so everything that
// is not a marker is kept byte for byte. It is the one owner of that rule: the
// backend's frame and every prompt builder that wraps page text in the frame
// call it.
func Unmark(s string) string {
	var folded strings.Builder
	folded.Grow(len(s))
	// For each byte of folded, the original byte range of the rune it came from.
	starts := make([]int, 0, len(s))
	ends := make([]int, 0, len(s))
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		f, keep := foldRune(r)
		if !keep {
			continue
		}
		before := folded.Len()
		folded.WriteRune(f)
		for j := before; j < folded.Len(); j++ {
			starts = append(starts, i-n)
			ends = append(ends, i)
		}
	}
	matches := markerRE.FindAllStringIndex(folded.String(), -1)
	if len(matches) == 0 {
		return s
	}
	var out strings.Builder
	last := 0
	for _, m := range matches {
		out.WriteString(s[last:starts[m[0]]])
		out.WriteString(MarkerRemoved)
		last = ends[m[1]-1]
	}
	out.WriteString(s[last:])
	return out.String()
}
