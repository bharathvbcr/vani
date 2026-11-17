package main

import (
	"html"
	"math"
	"os"
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

// quoteHasFigure reports whether the quote contains every figure of found with
// the unit found gives it: the figure the validator accepted must be the one
// the quote shows, not some other number in the same sentence. A found figure
// with no unit is matched on value alone; one with a unit needs that unit in
// the quote ("94" in a table next to a "ms" header does not carry it). A
// found value with no number at all has nothing to tie, so it passes.
//
// A figure the verifier computed is exempt: a percentage (or multiple) that is
// the change or ratio of two other figures in found ("470 -> 94 ms (-80%)") is
// printed in no source line. It is exempt only when both of those figures are
// themselves in the quote, so the arithmetic is checked on quoted values.
func quoteHasFigure(quote, found string) bool {
	q := figuresIn(quote)
	fs := figuresIn(found)
	// seen[i] holds the quote's own figures that stand for fs[i]. A found
	// "470 -> 94 ms" writes the unit once, so its 470 carries none, but the
	// quote's "470 ms" does: the operands' kinds are read from the quote.
	seen := make([][]figure, len(fs))
	for i, f := range fs {
		seen[i] = figuresHeld(f, q)
	}
	for i, f := range fs {
		if len(seen[i]) > 0 {
			continue
		}
		derived := false
		for a := range fs {
			for b := range fs {
				if a == i || b == i || a == b {
					continue
				}
				for _, x := range seen[a] {
					for _, y := range seen[b] {
						if derivedFrom(f, x, y) {
							derived = true
						}
					}
				}
			}
		}
		if !derived {
			return false
		}
	}
	return true
}

// figureIn reports whether q holds f: the same quantity, with f's unit when f
// names one.
func figureIn(f figure, q []figure) bool {
	return len(figuresHeld(f, q)) > 0
}

// figuresHeld returns the figures of q that are f: the same quantity, with f's
// unit when f names one.
func figuresHeld(f figure, q []figure) []figure {
	var out []figure
	for _, g := range q {
		if f.Class != "" && g.Class == "" {
			continue
		}
		if sameQuantity(f, g, 1) || sameQuantity(g, f, 1) {
			out = append(out, g)
		}
	}
	return out
}

// derivedFrom reports whether d is the percentage change from `from` to `to`
// (-80% for 470 -> 94), the percentage `to` is of `from`, or the multiple
// `to`/`from` or `from`/`to`, to the precision d is written with. The sign is
// ignored: "-80%" and "80% faster" state the same change. Both operands must
// be real quantities, not percentages themselves, and the same kind of
// quantity: a change between "5 servers" and "20 ms" is no change at all.
// Two unit-less figures share the empty class; times scale through TimeScale.
func derivedFrom(d, from, to figure) bool {
	if from.Class == "pct" || to.Class == "pct" || from.Class != to.Class || (d.Class != "pct" && d.Class != "mult") {
		return false
	}
	x, y := numVal(from.Tok)*from.Mult, numVal(to.Tok)*to.Mult
	if from.Class == "time" {
		x, y = x*from.TimeScale, y*to.TimeScale
	}
	if x == 0 || y == 0 || math.IsNaN(x) || math.IsNaN(y) {
		return false
	}
	dv := math.Abs(numVal(d.Tok))
	tol := precision(d.Tok) + 1e-9
	var cands []float64
	if d.Class == "pct" {
		cands = []float64{math.Abs(y-x) / math.Abs(x) * 100, math.Abs(y) / math.Abs(x) * 100}
	} else {
		cands = []float64{math.Abs(y / x), math.Abs(x / y)}
	}
	for _, c := range cands {
		if math.Abs(dv-c) <= tol {
			return true
		}
	}
	return false
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
		out, err := runCmd(timeoutPoppler, runOpts{}, "pdftotext", "-layout", path, "-")
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
