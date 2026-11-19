package track

import "testing"

// A built-in term listed with adjacent separators matches its own text and the
// same spellings a single separator would; a term with one separator is unchanged.
func TestBuiltinTermWithAdjacentSeparators(t *testing.T) {
	for _, raw := range []string{"zorb - flux", "zorb  flux", "zorb -- flux", "zorb\u2014 flux", "zorb flux"} {
		c := compile([]string{raw}, nil)
		for in, want := range map[string]bool{
			"zorb - flux":       true,
			"zorb flux":         true,
			"zorb-flux":         true,
			"zorb \u2014 flux": true,
			"zorb -- - flux":    true,
			"a zorb - flux b":   true,
			"zorb - fluxes":     false,
			"xzorb - flux":      false,
			"zorbflux":          false,
			"zorb flu":          false,
			"zorb -":            false,
		} {
			f := foldString(in)
			if terms, _ := c.found(&f); (len(terms) > 0) != want {
				t.Errorf("term %q on %q: found=%v want %v", raw, in, len(terms) > 0, want)
			}
		}
	}
	if tm := newBuiltinTerm("zorb - flux"); len(tm.items) != len("zorb flux") {
		t.Errorf("items %q: separators not collapsed", string(tm.items))
	}
}
