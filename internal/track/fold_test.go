package track

import (
	"strings"
	"testing"
)

func TestFoldHandlesTheSpecificCharacters(t *testing.T) {
	cases := map[string]string{
		"Jose<U+0301>":               "jose",
		"Jos<U+00E9>":                "jose",
		"<U+0130>":                   "i",
		"<U+0131>":                   "i",
		"<U+017F>":                   "s",
		"<U+212A>":                   "k", // Kelvin sign
		"<U+FF21><U+FF22><U+FF03>":   "ab#",
		"<U+FF20>":                   "@",
		"a<U+200B><U+200D><U+00AD>b": "ab",
		"<U+0430><U+0435><U+043E><U+0440><U+0441><U+0443><U+0445>": "aeopcyx",
		"<U+03BF><U+03BD>":         "ov",
		"<U+00DF>":                 "ss",
		"<U+00E6>":                 "ae",
		"<U+FB01>":                 "fi",
		"a <U+00A0>\t\n b":         "a b",
		"<U+0142><U+00F8><U+0111>": "lod",
		"<U+1EA1><U+1EBF><U+1ECB>": "aei",
		"<U+1D7D8>":                "0",
	}
	for in, want := range cases {
		f := foldString(U(in))
		if got := string(f.r); got != want {
			t.Errorf("fold(%q) = %q, want %q", in, got, want)
		}
	}
}

// The accent table maps every precomposed Latin-1 and Latin Extended-A letter
// to ASCII letters, so a decomposed spelling (base + combining mark) and its
// precomposed twin fold alike.
func TestAccentTableCoversLatin1AndExtendedA(t *testing.T) {
	for r := rune(0x00c0); r <= 0x017f; r++ {
		if r == 0x00d7 || r == 0x00f7 { // multiplication and division signs
			continue
		}
		got := string(foldString(string(r)).r)
		if got == "" || strings.IndexFunc(got, func(c rune) bool { return c < 'a' || c > 'z' }) >= 0 {
			t.Errorf("U+%04X folds to %q, want ASCII letters", r, got)
		}
	}
}
