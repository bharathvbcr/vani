package srcutil

import (
	"strings"
	"testing"
)

func TestRelocate(t *testing.T) {
	for name, tc := range map[string]struct {
		stored, listed string
		want           string
		moved          bool
	}{
		"same":                  {"Paris, France", "Paris, France", "Paris, France", false},
		"case and spacing":      {"Paris, France", "  paris,   FRANCE ", "Paris, France", false},
		"moved":                 {"Paris, France", "Lyon, France", "Lyon, France", true},
		"row names none":        {"Paris, France", "", "Paris, France", false},
		"row is blank":          {"Paris, France", " \t\n ", "Paris, France", false},
		"stored empty":          {"", "Lyon, France", "Lyon, France", true},
		"both empty":            {"", "", "", false},
		"oversized row":         {"Paris, France", strings.Repeat("x", MaxRowPlace+1), "Paris, France", false},
		"body-sized row":        {"Paris, France", strings.Repeat("x ", 1<<20), "Paris, France", false},
		"at the bound":          {"Paris", strings.Repeat("x", MaxRowPlace), strings.Repeat("x", MaxRowPlace), true},
		"row's whitespace runs": {"Paris", "Lyon\n\n  France", "Lyon France", true},
	} {
		got, moved := Relocate(tc.stored, tc.listed)
		if got != tc.want || moved != tc.moved {
			t.Errorf("%s: Relocate(%q, %.20q) = %.30q, %v; want %.30q, %v", name, tc.stored, tc.listed, got, moved, tc.want, tc.moved)
		}
	}
}
