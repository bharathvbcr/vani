package textsim

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFold(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"São Paulo", "sao paulo"},
		{"SÃO PAULO", "sao paulo"},
		{"São Paulo", "sao paulo"}, // decomposed: a + combining tilde
		{"Ilhéus, Bahia", "ilheus, bahia"},
		{"Maceió", "maceio"},
		{"Zoë Łódź Çağrı", "zoe lodz cagri"},
		{"ＡＣＭＥ　Labs", "acme　labs"}, // full-width letters fold; the ideographic space is left
		{"  two  spaces ", "  two  spaces "},
		{"straße", "straße"},       // outside the table: compares as itself
		{"東京 Москва", "東京 москва"}, // other scripts: only lowercased
	} {
		if got := Fold(tc.in); got != tc.want {
			t.Errorf("Fold(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Different places stay different once folded.
	for _, p := range [][2]string{{"Maceió", "Paripueira"}, {"São Paulo", "São Luís"}, {"Ilhéus", "Ilheu"}} {
		if Fold(p[0]) == Fold(p[1]) {
			t.Errorf("Fold(%q) == Fold(%q)", p[0], p[1])
		}
	}
}

func TestFoldMalformedAndLargeInput(t *testing.T) {
	if got := Fold("a\xffé"); !utf8.ValidString(got) || !strings.HasPrefix(got, "a") || !strings.HasSuffix(got, "e") {
		t.Errorf("Fold of invalid UTF-8 = %q", got)
	}
	big := strings.Repeat("É", 1<<16)
	if got := Fold(big); got != strings.Repeat("e", 1<<16) {
		t.Errorf("Fold of %d accented letters: %d bytes back", 1<<16, len(got))
	}
}
