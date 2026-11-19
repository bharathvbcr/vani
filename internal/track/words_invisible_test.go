package track

import "testing"

// Control characters and combining marks the matcher's fold drops must not turn a
// comment into a term (the file would report as configured, fail-open).
func TestAttackW6ControlBeforeHash(t *testing.T) {
	for name, body := range map[string]string{
		"nul":       "\x00# a\n",
		"ctrl":      "\x01# a\n",
		"combining": "\u0301# a\n",
		"vt":        "\x0b# a\n",
		"cf":        "\u200b# a\n",
		"mixed":     "\x01\u0301\u200b# a\n",
	} {
		w, err := w6load(t, body)
		if err != nil {
			t.Errorf("%s: comment line refused: %v", name, err)
			continue
		}
		if w.Personal {
			t.Errorf("%s: comment-only file reports Personal, owner=%q", name, w.owner)
		}
	}
}

// A line of nothing the fold keeps is refused by line number, as a line of
// format characters always was, and the error names no content.
func TestWordsLineInvisibleOnlyRefused(t *testing.T) {
	for name, body := range map[string]string{
		"ctrl":      "# ok\n\x01\n",
		"combining": "# ok\n\u0301\n",
		"nul":       "# ok\n\x00\x00\n",
	} {
		_, err := w6load(t, body)
		if err == nil {
			t.Errorf("%s: line with no visible characters loaded", name)
		}
	}
}

// Invisible characters inside a term are not stored, and the term still screens.
func TestWordsTermDropsInvisible(t *testing.T) {
	w, err := w6load(t, "zor\x01bl\u0301ax\n")
	if err != nil {
		t.Fatal(err)
	}
	if !w.Personal || len(w.Found("Zorblax")) == 0 {
		t.Errorf("term not screened: owner=%q", w.owner)
	}
	for _, o := range w.owner {
		if o != "zorblax" {
			t.Errorf("stored term %q holds more than what is visible", o)
		}
	}
}
