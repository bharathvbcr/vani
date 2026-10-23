package sources

import (
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

// Unicode letters and digits are board names: jobs.ashbyhq.com serves
// "Zürich Labs" at /Z%C3%BCrich%20Labs, and the name is only ever sent through
// url.PathEscape.
func TestBoardNameAcceptsUnicodeLettersAndDigits(t *testing.T) {
	for _, ok := range []string{
		"Zürich Labs",                        // precomposed Latin
		"Zürich",                             // the example the 2026-10-22 follow-up named
		"东京 科技",                              // CJK, one space
		"データ",                                // katakana
		"Αθήνα Labs",                         // Greek with tonos (precomposed)
		"Москва-2",                           // Cyrillic, hyphen, digit
		"서울",                                 // Hangul syllables (precomposed)
		"٣٤٥ Labs",                           // Arabic-Indic digits are Nd
		"Ａｃｍｅ",                               // fullwidth letters are letters
		strings.Repeat("字", MaxBoardNameLen), // 64 runes, 192 bytes: at both bounds
		"Hippocratic AI",
	} {
		if why := BoardNameProblem(ok); why != "" {
			t.Errorf("%q refused: %s", ok, why)
		}
	}
}

func TestBoardNameRefusesWhatUnicodeMustNotSmuggle(t *testing.T) {
	for name, bad := range map[string]string{
		"RTL override":            "Acme‮Labs",
		"RTL override first":      "‮Acme",
		"LTR mark":                "Acme‎Labs",
		"zero-width joiner":       "Acme‍Labs",
		"zero-width space":        "Acme​Labs",
		"word joiner":             "Acme⁠Labs",
		"BOM":                     "\ufeff" + "Acme",
		"soft hyphen":             "Acme­Labs",
		"combining acute (NFD é)": "Café",
		"lone combining mark":     "́",
		"mark after space":        "Acme ́Labs",
		"variation selector":      "Acme️",
		"non-breaking space":      "Acme Labs",
		"ideographic space":       "Acme　Labs",
		"em space":                "Acme Labs",
		"line separator":          "Acme Labs",
		"NEL":                     "Acme\u0085Labs",
		"C1 control":              "Acme\u009fLabs",
		"DEL":                     "Acme\x7fLabs",
		"NUL":                     "Acme\x00Labs",
		"private use":             "Acme",
		"unassigned":              "Acme\U000e0080",
		"tag character":           "Acme\U000e0041",
		"surrogate":               "Acme\xed\xa0\x80",
		"invalid UTF-8":           "Acme\xff\xfeLabs",
		"truncated UTF-8":         "Z\xc3",
		"overlong slash":          "a\xc0\xafb",
		"replacement char":        "Acme�",
		"fullwidth solidus":       "a／b",
		"fullwidth full stop":     "a．b",
		"CJK full stop":           "东京。科技",
		"fullwidth colon":         "a：b",
		"fullwidth percent":       "a％b",
		"division slash":          "a∕b",
		"emoji":                   "Acme🚀",
		"currency":                "Acme€",
		"leading space":           " Zürich",
		"trailing space":          "Zürich ",
		"double space":            "Zürich  Labs",
		"only a space":            " ",
		"empty":                   "",
		"65 runes":                strings.Repeat("a", MaxBoardNameLen+1),
		"65 CJK runes":            strings.Repeat("字", MaxBoardNameLen+1),
		"over the byte bound":     strings.Repeat("𠀀", MaxBoardNameLen), // 64 runes, 256 bytes
		"ASCII backslash":         `a\b`,
	} {
		if why := BoardNameProblem(bad); why == "" {
			t.Errorf("%s: %q accepted", name, bad)
		}
	}
}

// The bounds are on runes and on bytes, and each can bind alone.
func TestBoardNameBoundsAreRunesAndBytes(t *testing.T) {
	atBoth := strings.Repeat("字", MaxBoardNameLen)
	if n, b := utf8.RuneCountInString(atBoth), len(atBoth); n != MaxBoardNameLen || b != 3*MaxBoardNameLen || b > MaxBoardNameBytes {
		t.Fatalf("fixture drifted: %d runes, %d bytes, byte bound %d", n, b, MaxBoardNameBytes)
	}
	if why := BoardNameProblem(atBoth); why != "" {
		t.Errorf("64 BMP runes refused: %s", why)
	}
	// 64 four-byte runes: within the rune bound, past the byte bound.
	wide := strings.Repeat("𠀀", MaxBoardNameLen)
	if utf8.RuneCountInString(wide) != MaxBoardNameLen || len(wide) <= MaxBoardNameBytes {
		t.Fatalf("fixture drifted: %d runes, %d bytes", utf8.RuneCountInString(wide), len(wide))
	}
	if BoardNameProblem(wide) == "" {
		t.Error("a name past the byte bound was accepted")
	}
	// 65 one-byte runes: within the byte bound, past the rune bound.
	if BoardNameProblem(strings.Repeat("a", MaxBoardNameLen+1)) == "" {
		t.Error("a 65-rune name was accepted")
	}
}

// What the owner pastes from the address bar and what the adapter sends are
// the same bytes: the page URL's decoded path segment is the name, and
// PathEscape of the name is the segment again.
func TestBoardNameRoundTripsWithJobsAshbyhqURLs(t *testing.T) {
	for _, c := range []struct{ name, seg string }{
		{"Zürich Labs", "Z%C3%BCrich%20Labs"},
		{"东京 科技", "%E4%B8%9C%E4%BA%AC%20%E7%A7%91%E6%8A%80"},
		{"Hippocratic AI", "Hippocratic%20AI"},
		{"Москва-2", "%D0%9C%D0%BE%D1%81%D0%BA%D0%B2%D0%B0-2"},
	} {
		if why := BoardNameProblem(c.name); why != "" {
			t.Errorf("%q refused: %s", c.name, why)
			continue
		}
		if got := url.PathEscape(c.name); got != c.seg {
			t.Errorf("PathEscape(%q) = %q, want %q", c.name, got, c.seg)
		}
		for _, page := range []string{
			"https://jobs.ashbyhq.com/" + c.seg + "/abc",
			"https://jobs.ashbyhq.com/" + c.name + "/abc", // pasted with raw UTF-8
		} {
			u, err := url.Parse(page)
			if err != nil {
				t.Errorf("parse %q: %v", page, err)
				continue
			}
			if first := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")[0]; first != c.name {
				t.Errorf("%q: first segment %q, want %q", page, first, c.name)
			}
		}
		// And what goes on the wire carries no raw byte outside %XX or unreserved.
		for i := 0; i < len(c.seg); i++ {
			if c.seg[i] >= 0x80 || c.seg[i] == ' ' || c.seg[i] == '/' || c.seg[i] == '?' || c.seg[i] == '#' {
				t.Errorf("escaped segment %q holds raw byte %#x", c.seg, c.seg[i])
			}
		}
	}
}

// Homoglyphs are not a hazard here: the name is escaped into the path of one
// fixed host and compared there, so a lookalike can only fail to match, never
// reach another host or path. The rule therefore does not try to detect them,
// and this test records that on purpose.
func TestBoardNameHomoglyphsAreOnlyEverARequestPath(t *testing.T) {
	cyrillicAcme := "асме" // looks like "acme"
	if why := BoardNameProblem(cyrillicAcme); why != "" {
		t.Fatalf("homoglyph-only name refused: %s", why)
	}
	esc := url.PathEscape(cyrillicAcme)
	if esc == "acme" || strings.ContainsAny(esc, "/?#.:") {
		t.Errorf("homoglyph escaped to %q", esc)
	}
	if esc != "%D0%B0%D1%81%D0%BC%D0%B5" {
		t.Errorf("homoglyph escaped to %q", esc)
	}
}
