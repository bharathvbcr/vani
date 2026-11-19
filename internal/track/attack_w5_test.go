package track

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// oldMatchAt is the built-in matcher before 064be79: every item matches one
// folded rune exactly.
func oldBuiltinFound(t *term, f *folded) bool {
	r := f.r
	for i := range r {
		if i > 0 && isASCIILetter(r[i-1]) {
			continue
		}
		j := i
		ok := true
		for _, c := range t.items {
			if j >= len(r) || r[j] != c {
				ok = false
				break
			}
			j++
		}
		if ok && !(j < len(r) && isASCIILetter(r[j])) {
			return true
		}
	}
	return false
}

// Differential: for every built-in term, any text the old matcher found, the
// new one finds (a hit the old matcher had must never become a miss).
func TestAttackBuiltinNeverNarrowerThanBefore(t *testing.T) {
	alphabet := []string{" ", "-", "\u2010", "\u2015", "\u2212", "\u00a0", "a", "x", "1", "_", "\u200b", "\u0301"}
	for _, raw := range builtinWords {
		if raw == "@" {
			continue
		}
		tm := newBuiltinTerm(raw)
		c := compile([]string{raw}, nil)
		var gen func(prefix string, depth int)
		pieces := strings.Fields(strings.NewReplacer("-", " - ").Replace(raw))
		_ = pieces
		check := func(s string) {
			f := foldString(s)
			old := oldBuiltinFound(tm, &f)
			terms, _ := c.found(&f)
			if old && len(terms) == 0 {
				t.Errorf("term %q: old found, new missed in %q", raw, s)
			}
		}
		gen = func(prefix string, depth int) {
			check(prefix)
			if depth == 0 {
				return
			}
			for _, a := range alphabet {
				gen(prefix+a, depth-1)
			}
		}
		// seeds: the term with each separator char replaced, wrapped in noise
		for _, seed := range []string{raw, strings.ReplaceAll(raw, " ", "-"), strings.ReplaceAll(raw, "-", " "),
			strings.ReplaceAll(raw, " ", "\u00a0"), strings.ReplaceAll(raw, "-", "\u2011")} {
			for _, a := range alphabet {
				for _, b := range alphabet {
					check(a + seed + b)
					check(seed + a + b)
				}
			}
		}
		gen("", 3)
	}
}

// Redact over multi-byte dashes and long separator runs: the whole span goes,
// nothing leaks, output stays valid UTF-8.
func TestAttackRedactMultiByteSeparatorRuns(t *testing.T) {
	w := BuiltinWords()
	seps := []string{"\u2010", "\u2011", "\u2012", "\u2013", "\u2014", "\u2015", "\u2212", "\u00a0", " \u2014 ", "-\u2010 \u2212-", strings.Repeat("\u2014", 50), strings.Repeat(" -", 200)}
	for _, term := range [][2]string{{"green", "card"}, {"permanent", "resident"}, {"cap", "exempt"}, {"work", "authorization"}} {
		for _, sep := range seps {
			for _, pre := range []string{"", "x ", "\u2014"} {
				for _, post := range []string{"", " y", "\u2014", "-cards", "s"} {
					text := pre + term[0] + sep + term[1] + post
					out, n := w.Redact(text)
					if !utf8.ValidString(out) {
						t.Fatalf("invalid utf8 for %q: %q", text, out)
					}
					if n > 0 && len(w.Found(out)) != 0 {
						t.Errorf("leak %q -> %q", text, out)
					}
					if n > 0 && (strings.Contains(out, term[0]+"\u2014") || strings.Contains(out, "\u2015"+term[1])) {
						t.Errorf("tail leak %q -> %q", text, out)
					}
					if post == "" && len(w.Found(text)) == 0 {
						t.Errorf("not found %q", text)
					}
				}
			}
		}
	}
}

// Boundary semantics: unchanged for the whole-word rule.
func TestAttackBuiltinBoundaries(t *testing.T) {
	w := BuiltinWords()
	for in, want := range map[string]bool{
		"green card":          true,
		"green cards":         false,
		"green-cards":         false,
		"green-card-":         true,
		"-green-card":         true,
		"xgreen card":         false,
		"green card\u0301":    true,
		"green card\u200bs":   false,
		"green\u200b-card":    true,
		"cap-exempt":          true,
		"cap-exempts":         false,
		"cap - exempt":        true,
		"cap -- exempt":       true,
		"cap -\u2014- exempt": true,
		"cap_exempt":          false,
		"green-":              false,
		"-card":               false,
		"[redacted]card":      false,
	} {
		if got := len(w.Found(in)) > 0; got != want {
			t.Errorf("Found(%q)=%v want %v", in, got, want)
		}
	}
}

// Pathological separator runs stay linear.
func TestAttackSeparatorRunsLinear(t *testing.T) {
	w := BuiltinWords()
	cases := map[string]string{
		"hyphens":     "green" + strings.Repeat("-", 200000) + "card",
		"alternating": "green" + strings.Repeat(" -", 100000) + "card",
		"nomatch":     "green" + strings.Repeat("-", 200000) + "cart",
		"manystarts":  strings.Repeat("green-", 50000),
		"capsruns":    strings.Repeat("cap"+strings.Repeat("-", 40)+"x", 4000),
		"mbdash":      "green" + strings.Repeat("\u2014", 100000) + "card",
	}
	for name, s := range cases {
		start := time.Now()
		w.Found(s)
		w.Redact(s)
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("%s: %v", name, d)
		}
	}
}

// LoadWords BOM handling: a comment never becomes an owner term, so a file with
// no real term is not "Personal".
func TestAttackLoadWordsBOMVariants(t *testing.T) {
	const bom = "\ufeff"
	for name, tc := range map[string]struct {
		body     string
		personal bool
	}{
		"bom mid-file comment":    {"# a\n" + bom + "# b\n", false},
		"bom line2 then term":     {"# a\n" + bom + "zorblax\n", true},
		"two boms comment":        {bom + bom + "# c\n", false},
		"two boms term":           {bom + bom + "zorblax\n", true},
		"zwsp comment":            {"\u200b# c\n", false},
		"bom then spaces comment": {bom + "  # c\n", false},
		"utf16le comment":         {"\xff\xfe#\x00 \x00c\x00\n\x00", false},
		"crlf bom comment":        {bom + "# c\r\n# d\r\n", false},
		"bom after blank line":    {"\n" + bom + "# c\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeTrack(t, dir, WordsFile, tc.body)
			w, err := LoadWords(dir)
			if err != nil {
				t.Fatalf("error %v", err)
			}
			if w.Personal != tc.personal {
				t.Fatalf("Personal=%v want %v (owner %q)", w.Personal, tc.personal, w.owner)
			}
		})
	}
}

func TestAttackLoadWordsErrorsNameNoContent(t *testing.T) {
	dir := t.TempDir()
	writeTrack(t, dir, WordsFile, "\ufeffzorblax-secret\n\u200b\n"+strings.Repeat("q", maxWordBytes+1)+"\n")
	_, err := LoadWords(dir)
	if err == nil || strings.Contains(err.Error(), "zorblax") || strings.Contains(err.Error(), "qqqq") {
		t.Fatalf("err %v", err)
	}
}
