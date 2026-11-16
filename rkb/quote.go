package main

import (
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Quote checking. A blind verifier's quote must occur in the file it cites.
// Comparison ignores case, whitespace, markdown/table markup, HTML entities
// and typographic variants (× x, – — − -, curly quotes, µ u, superscripts), and
// an ellipsis may join fragments that occur in order. At least one fragment
// must be substantial (minQuoteFrag characters after normalisation), so "the"
// or "ms ... 41" cannot pass as evidence.

const minQuoteFrag = 12

var quoteStrip = regexp.MustCompile("[*`|_>#~\\\\]+")
var quoteSpace = regexp.MustCompile(`\s+`)

var quoteRepl = strings.NewReplacer(
	"×", "x", "–", "-", "—", "-", "−", "-", "‐", "-", "‑", "-", "‘", "'", "’", "'", "“", "\"", "”", "\"",
	"µ", "u", "μ", "u", " ", " ", " ", " ", " ", " ", "\u200b", "",
	"→", "->", "⇒", "->", "≥", ">=", "≤", "<=", "≈", "~", "±", "+/-", "…", "...",
	"⁰", "0", "¹", "1", "²", "2", "³", "3", "⁴", "4", "⁵", "5", "⁶", "6", "⁷", "7", "⁸", "8", "⁹", "9", "⁻", "-", "⁺", "+",
)

func normQuote(s string) string {
	s = html.UnescapeString(s)
	s = strings.ToLower(quoteRepl.Replace(s))
	s = quoteStrip.ReplaceAllString(s, " ")
	return strings.TrimSpace(quoteSpace.ReplaceAllString(s, " "))
}

// quoteInText reports whether quote occurs in text under normalisation.
// Fragments split by "..." or "…" must each occur, in order; fragments
// shorter than 4 characters are ignored (an ellipsis at the edges).
func quoteInText(text, quote string) bool {
	return quoteInNorm(normQuote(text), quote)
}

func quoteInNorm(t, quote string) bool {
	q := quoteRepl.Replace(quote)
	parts := strings.Split(q, "...")
	pos, longest := 0, 0
	for _, p := range parts {
		np := normQuote(p)
		if len(np) < 4 {
			continue
		}
		i := strings.Index(t[pos:], np)
		if i < 0 {
			return false
		}
		pos += i + len(np)
		longest = max(longest, len([]rune(np)))
	}
	return longest >= minQuoteFrag
}

const maxQuoteFile = 64 << 20

// quoteInFile checks a quote against a file's text (PDFs through pdftotext).
func quoteInFile(path, quote string) bool {
	t, ok := fileText(path)
	return ok && quoteInText(t, quote)
}

func fileText(path string) (string, bool) {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() || st.Size() > maxQuoteFile {
		return "", false
	}
	if strings.EqualFold(filepath.Ext(path), ".pdf") {
		out, err := exec.Command("pdftotext", "-layout", path, "-").Output()
		if err != nil {
			return "", false
		}
		return string(out), true
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	s := string(b)
	if strings.EqualFold(filepath.Ext(path), ".json") || strings.EqualFold(filepath.Ext(path), ".ipynb") {
		// a quote of a JSON string value, as the verifier read it
		s += "\n" + strings.NewReplacer(`\n`, "\n", `\t`, " ", `\"`, `"`, `\u00d7`, "×", `\u2192`, "→", `\u2013`, "–", `\u2014`, "—", `\u00b5`, "µ").Replace(s)
	}
	return s, true
}
