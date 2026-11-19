package track

import "testing"

// A built-in multi-word term is one idea however it is written: spaced,
// hyphenated, or with a no-break space between the words.
func TestBuiltinMultiWordTermSeparators(t *testing.T) {
	w := BuiltinWords()
	for _, term := range []string{"green card", "permanent resident", "work authorization", "cap exempt", "cap-exempt"} {
		for _, sep := range []string{" ", "-", "\u00a0", "\u2011", "\u2013", "  ", " - ", "\u00a0-\u00a0", "\t"} {
			words := splitTerm(term)
			text := "I hold " + words[0] + sep + words[1] + " today"
			if got := w.Found(text); len(got) == 0 {
				t.Errorf("%q not found in %q", term, text)
			}
			out, n := w.Redact(text)
			if n == 0 || len(w.Found(out)) != 0 {
				t.Errorf("Redact(%q) = %q, %d; terms left %v", text, out, n, w.Found(out))
			}
		}
	}
}

func splitTerm(s string) [2]string {
	for i, r := range s {
		if r == ' ' || r == '-' {
			return [2]string{s[:i], s[i+1:]}
		}
	}
	return [2]string{s, ""}
}

// Whole-word boundaries still hold, and unrelated hyphenation is not a match.
func TestBuiltinMultiWordTermBoundaries(t *testing.T) {
	c := compile([]string{"zorb flux", "zorb-glim"}, nil)
	f := func(s string) int {
		f := foldString(s)
		terms, _ := c.found(&f)
		return len(terms)
	}
	for in, want := range map[string]int{
		"zorb flux":           1,
		"zorb-flux":           1,
		"zorb\u00a0flux":      1,
		"zorb glim":           1,
		"zorb-glim":           1,
		"Zorb - Flux":         1,
		"xzorb-flux":          0, // inside a word
		"zorb-fluxes":         0,
		"zorbflux":            0, // no separator at all
		"zorb_flux":           0,
		"zorb":                0,
		"zorb-":               0,
		"zorb\u200bflux":      0, // joined by an invisible character only: unchanged behaviour
		"":                    0,
		"zorb flux zorb-flux": 1, // each term once
	} {
		if got := f(in); got != want {
			t.Errorf("found(%q) = %d terms, want %d", in, got, want)
		}
	}
}
