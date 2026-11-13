package track

import (
	"strings"
	"testing"
	"time"
)

// Round two of review: apostrophes, the placeholder, more separators, more
// look-alikes, and the "@" expansion's running time.

func TestApostrophesOfEveryKindAreOneApostropheAndOptional(t *testing.T) {
	w := wordsOf(t, "o'brien")
	for _, in := range []string{
		"Pat O'Brien", U("Pat O<U+2019>Brien"), U("Pat O<U+2018>Brien"), U("Pat O<U+02BC>Brien"), U("Pat O<U+FF07>Brien"),
		"Pat OBrien", "Pat O Brien", "Pat O-Brien", U("Pat O<U+2019>Briens"),
	} {
		out, n := w.Redact(in)
		if n == 0 || len(w.Found(in)) == 0 || len(w.Found(out)) > 0 {
			t.Errorf("%+q: found %v, redacted to %+q (%d)", in, w.Found(in), out, n)
		}
	}
	// The term may be listed with a curly apostrophe too.
	w = wordsOf(t, U("o<U+2019>brien"))
	if out, n := w.Redact("Pat O'Brien"); n == 0 {
		t.Errorf("curly-listed term missed a straight spelling: %q", out)
	}
}

// A term that is a prefix of the placeholder's own text must not match inside
// a placeholder: removal would nest it forever and the check would refuse every
// send.
func TestPlaceholderIsNeverAMatch(t *testing.T) {
	for _, term := range []string{"reda", "redact", "redacted", "edac", "dacted"} {
		w := wordsOf(t, term)
		out, n := w.Redact("met " + term + " today")
		if n == 0 || strings.Count(out, "[") != 1 || out != "met [redacted] today" {
			t.Errorf("term %q: Redact = %q (%d)", term, out, n)
		}
		if f := w.Found(out); len(f) > 0 {
			t.Errorf("term %q: Found finds %v in %q", term, f, out)
		}
		if again, n := w.Redact(out); again != out || n != 0 {
			t.Errorf("term %q: a second Redact changed %q to %q (%d)", term, out, again, n)
		}
		// A placeholder already in the text is left alone.
		in := "a [redacted] b"
		if o, n := w.Redact(in); o != in || n != 0 || len(w.Found(in)) > 0 {
			t.Errorf("term %q: placeholder treated as text: %q (%d)", term, o, n)
		}
	}
	// A term next to a placeholder is still found: the placeholder is a boundary.
	w := wordsOf(t, "kernel", "reda")
	in := "[redacted]kernel[redacted]reda"
	out, n := w.Redact(in)
	if want := "[redacted][redacted][redacted][redacted]"; out != want || n != 2 {
		t.Errorf("adjacent: %q (%d), want %q", out, n, want)
	}
	if f := w.Found(out); len(f) > 0 {
		t.Errorf("adjacent: Found finds %v", f)
	}
}

func TestMoreSeparatorsJoinATerm(t *testing.T) {
	w := wordsOf(t, "555-123-4567", "zephyrnet")
	for _, in := range []string{
		"call (555) 123-4567", "555.123.4567", "555 123 4567", "(555)123-4567",
		U("Zephyr<U+00B7>net"), "zephyr:net", "zephyr+net", U("zephyr<U+2044>net"), U("zephyr<U+2215>net"), "zephyr(net", "zephyr)net",
	} {
		out, n := w.Redact(in)
		if n == 0 || len(w.Found(in)) == 0 || len(w.Found(out)) > 0 {
			t.Errorf("%+q: found %v, redacted to %+q (%d)", in, w.Found(in), out, n)
		}
	}
}

func TestLatinExtendedBAndIPALookalikesFold(t *testing.T) {
	w := wordsOf(t, "zephyrnet")
	for _, in := range []string{
		U("Zephyrn<U+0259>t"), U("<U+01B6>ephyrnet"), U("Zeph<U+01B4>rnet"), U("Zephyrn<U+01DD>t"), U("Zephyrn<U+0247>t"),
		U("<U+0240>ephyrnet"), U("Zephyr<U+0235>et"), U("Zeph<U+024F>rnet"), U("Z<U+0258>phyrnet"),
	} {
		out, n := w.Redact(in)
		if n == 0 || len(w.Found(out)) > 0 {
			t.Errorf("%+q not removed: %+q (%d)", in, out, n)
		}
	}
	// Every letter of the lookalike table folds to an ASCII letter.
	for r, want := range extraLookalikes {
		if got := string(foldString(string(r)).r); got != string(want) {
			t.Errorf("U+%04X folds to %q, want %q", r, got, string(want))
		}
	}
}

// Expanding the "@" token is linear: 1 MB of '@', and 1 MB of short tokens.
func TestAtTokenExpansionIsLinear(t *testing.T) {
	w := BuiltinWords()
	for name, in := range map[string]string{
		"all at signs": strings.Repeat("@", 1<<20),
		"tokens":       strings.Repeat("a@b ", 1<<18),
		"long token":   strings.Repeat("x@", 1<<19),
	} {
		start := time.Now()
		out, n := w.Redact(in)
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("%s: took %v", name, d)
		}
		if len(w.Found(out)) > 0 || n == 0 {
			t.Errorf("%s: %d redactions, still found", name, n)
		}
	}
}
