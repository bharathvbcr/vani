package track

import "testing"

// Blank is the one answer to "is this text empty": empty after the matcher's
// fold, which drops invisible characters and collapses every kind of space.
func TestBlank(t *testing.T) {
	for in, want := range map[string]bool{
		"":                         true,
		"   \t\n":                  true,
		"\u200b":                   true, // zero-width space
		"\u200b\u200d\u2060\ufeff": true,
		"\u00a0\u2003\u3000":       true, // no-break, em and ideographic spaces
		" \u200b \u00ad ":          true, // soft hyphen
		"\u0301":                   true, // a lone combining mark
		"\x00\x01":                 true,
		"\u2028\u2029":             true,
		"roles":                    false,
		"\u200bx":                  false,
		"-":                        false,
		"[redacted]":               false,
	} {
		if got := Blank(in); got != want {
			t.Errorf("Blank(%q) = %v, want %v", in, got, want)
		}
	}
}
