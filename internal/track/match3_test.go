package track

import "testing"

// Four-letter owner terms are whole words with a plural or possessive: never
// the start of a longer word. A capital that starts a new word (MarkApp) is a
// join and does count.
func TestFourLetterTermsMatchOnlyTheWholeWordAndItsPlural(t *testing.T) {
	w := wordsOf(t, "data", "mark")
	for _, in := range []string{
		"database", "Databricks", "metadata", "Marketing", "Markdown", "marking", "datasets", "MARKETING", "DATABASE", "benchmark",
	} {
		if out, n := w.Redact(in); out != in || n != 0 {
			t.Errorf("Redact(%q) = %q (%d), want it unchanged", in, out, n)
		}
		if f := w.Found(in); len(f) > 0 {
			t.Errorf("Found(%q) = %v, want nothing", in, f)
		}
	}
	for _, in := range []string{
		"Data", "datas", "data's", "Mark", "Marks", "Mark's", "MARK", "MARKS", "the data.", "Mark-up", U("Mark<U+2019>s"),
		"MarkApp", "DataLake", "myMark", "mark_x", "Mark.",
	} {
		out, n := w.Redact(in)
		if n == 0 || len(w.Found(in)) == 0 || len(w.Found(out)) > 0 {
			t.Errorf("%q: Found %v, Redact %q (%d)", in, w.Found(in), out, n)
		}
	}
	// Check and Redact agree.
	for _, in := range []string{"database", "Marks", "MarkApp", "Markdown", "data's", "Marketing and Mark"} {
		out, n := w.Redact(in)
		if (len(w.Found(in)) > 0) != (n > 0) || len(w.Found(out)) > 0 {
			t.Errorf("%q: Found and Redact disagree (%d, %q)", in, n, out)
		}
	}
}

// Five letters and up keep the loose rule: any suffix.
func TestFiveLetterTermsStillTakeASuffix(t *testing.T) {
	w := wordsOf(t, "alpha")
	for _, in := range []string{"alphabet", "Alphas", "alphaApp"} {
		if len(w.Found(in)) == 0 {
			t.Errorf("Found(%q) found nothing", in)
		}
	}
}
