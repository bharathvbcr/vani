package track

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wordsOf loads a words file holding lines, on top of the built-in list.
func wordsOf(t *testing.T, lines ...string) Words {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, WordsFile), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := LoadWords(dir)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestRedactRemovesEveryTermFoundFinds(t *testing.T) {
	w := wordsOf(t, "zephyrkit", "gemma", "gemma-metal", "quill mark", "zürich lab")
	cases := []struct{ in, want string }{
		{"Built Zephyrkit in Go", "Built [redacted] in Go"},
		{"ZEPHYRKIT, zephyrkit; ZephyrKit.", "[redacted], [redacted]; [redacted]."},
		// Longest first: the hyphen join goes whole, no "-metal" fragment.
		{"ported gemma-metal kernels", "ported [redacted] kernels"},
		{"gemma and gemma-metal", "[redacted] and [redacted]"},
		// Possessive, hyphen join to another word, brackets, digits.
		{"zephyrkit's scheduler", "[redacted]'s scheduler"},
		{"zephyrkit-cli (zephyrkit) zephyrkit2", "[redacted]-cli ([redacted]) [redacted]2"},
		// A phrase term.
		{"the Quill Mark editor", "the [redacted] editor"},
		// Unicode term, case-folded.
		{"at the ZÜRICH LAB", "at the [redacted]"},
		// Not inside an ordinary word, either side.
		{"zephyrkits rezephyrkit", "zephyrkits rezephyrkit"},
		{"optimize the kernel", "optimize the kernel"},
		// The built-in list too.
		{"code on GitHub, visa: none", "code on [redacted], [redacted]: none"},
		// "@" takes the whole token holding it.
		{"write to a.b@example.com today", "write to [redacted] today"},
		{"", ""},
	}
	for _, c := range cases {
		got, n := w.Redact(c.in)
		if got != c.want {
			t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
		}
		if f := w.Found(got); len(f) > 0 {
			t.Errorf("Redact(%q) left %d term(s) Found finds", c.in, len(f))
		}
		if (n > 0) != (got != c.in) {
			t.Errorf("Redact(%q) count %d disagrees with the change", c.in, n)
		}
	}
}

func TestRedactLeavesUnrelatedTextByteForByte(t *testing.T) {
	w := wordsOf(t, "zephyrkit")
	in := "Cut decode latency 3×  with fused attention kernels — in Metal\n\t* naïve café résumé"
	if got, n := w.Redact(in); got != in || n != 0 {
		t.Fatalf("changed text with no term: %q (%d)", got, n)
	}
}

func TestRedactIsBoundedOnLargeInput(t *testing.T) {
	terms := make([]string, 0, maxWords)
	for i := range maxWords {
		terms = append(terms, "term"+strings.Repeat("x", i%40)+string(rune('a'+i%26)))
	}
	w := wordsOf(t, terms...)
	in := strings.Repeat("the quick brown fox termxa jumps ", 400) // ~13 KiB, past MaxDigestBytes
	out, _ := w.Redact(in)
	if f := w.Found(out); len(f) > 0 {
		t.Fatalf("%d term(s) survived", len(f))
	}
}

func TestLoadWordsSaysWhenTheFileIsMissing(t *testing.T) {
	w, err := LoadWords(t.TempDir())
	if err != nil || !w.Missing || w.Personal {
		t.Fatalf("missing file: %+v %v", w, err)
	}
	w = wordsOf(t, "# only a comment")
	if w.Missing || w.Personal {
		t.Fatalf("comment-only file: %+v", w)
	}
	w = wordsOf(t, "zephyrkit")
	if w.Missing || !w.Personal {
		t.Fatalf("loaded file: %+v", w)
	}
}
