package main

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- TeX value -> text

var supMap = map[rune]rune{'0': '⁰', '1': '¹', '2': '²', '3': '³', '4': '⁴', '5': '⁵', '6': '⁶', '7': '⁷', '8': '⁸', '9': '⁹', '-': '⁻', '+': '⁺'}
var unsupMap = func() map[rune]rune {
	m := map[rune]rune{}
	for k, v := range supMap {
		m[v] = k
	}
	return m
}()

func toSup(s string) string {
	var b strings.Builder
	for _, r := range s {
		if v, ok := supMap[r]; ok {
			b.WriteRune(v)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var texSymbols = [][2]string{
	{`$\rightarrow$`, "→"}, {`\rightarrow`, "→"}, {`$\to$`, "→"}, {`$\leftarrow$`, "←"},
	{`$\times$`, "×"}, {`\times`, "×"}, {`$\sim$`, "~"}, {`\sim`, "~"}, {`$\approx$`, "≈"},
	{`\approx`, "≈"}, {`$\pm$`, "±"}, {`\pm`, "±"}, {`$\leq$`, "≤"}, {`\leq`, "≤"},
	{`$\geq$`, "≥"}, {`\geq`, "≥"}, {`$\le$`, "≤"}, {`$\ge$`, "≥"}, {`$\lt$`, "<"}, {`\lt`, "<"},
	{`$\gt$`, ">"}, {`\gt`, ">"}, {`\textless`, "<"}, {`\textgreater`, ">"}, {`$\mu$`, "µ"}, {`\mu`, "µ"},
	{`$\Delta$`, "Δ"}, {`\Delta`, "Δ"}, {`$\rho$`, "ρ"}, {`\rho`, "ρ"},
	{`\%`, "%"}, {`\&`, "&"}, {`\$`, "$"}, {`\#`, "#"}, {`\_`, "_"},
	{`\textasciitilde`, "~"}, {"``", "“"}, {"''", "”"}, {"---", "—"}, {"--", "–"},
	{`\,`, " "}, {`\ `, " "},
}

var (
	reSupBr  = regexp.MustCompile(`\$?\^\{([^}]*)\}\$?`)
	reSupTxt = regexp.MustCompile(`\\textsuperscript\{([^}]*)\}`)
	reFmt    = regexp.MustCompile(`\\(?:textbf|textit|emph|text|mathrm|texttt)\{([^{}]*)\}`)
	reBraces = regexp.MustCompile(`\{([^{}]*)\}`)
)

// detex renders a Claims.tex value as plain Unicode text.
func detex(s string) string {
	out := strings.ReplaceAll(s, "~", " ") // bare TeX tilde = non-breaking space
	// An escaped dollar is a currency sign; park it so the math-delimiter
	// strip below cannot remove it.
	const dollar = "\uE000"
	out = strings.ReplaceAll(out, `\$`, dollar)
	out = reSupBr.ReplaceAllStringFunc(out, func(m string) string {
		return toSup(reSupBr.FindStringSubmatch(m)[1])
	})
	out = reSupTxt.ReplaceAllStringFunc(out, func(m string) string {
		return toSup(reSupTxt.FindStringSubmatch(m)[1])
	})
	for _, p := range texSymbols {
		out = strings.ReplaceAll(out, p[0], p[1])
	}
	for i := 0; i < 3; i++ {
		out = reFmt.ReplaceAllString(out, "$1")
	}
	out = strings.ReplaceAll(out, "$", "")
	out = reBraces.ReplaceAllString(out, "$1")
	out = strings.ReplaceAll(out, dollar, "$")
	return strings.TrimSpace(out)
}

// ---------------------------------------------------------------- numbers

var (
	reSci  = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(?:×|x|\*)\s*10\s*\^?\s*([-+]?\d+)|(\d+(?:\.\d+)?)[eE]([-+]?\d+)`)
	reNum  = regexp.MustCompile(`(\d+(?:\.\d+)?E-?\d+)|\d{1,3}(?:,\d{3})+(?:\.\d+)?|\d+(?:\.\d+)?`)
	reBrSp = regexp.MustCompile(`\^\{([^}]*)\}`)
)

func normalizeNums(s string) string {
	var b strings.Builder
	for _, r := range s {
		if v, ok := unsupMap[r]; ok {
			b.WriteRune(v)
		} else {
			b.WriteRune(r)
		}
	}
	s = strings.ReplaceAll(b.String(), "−", "-")
	s = strings.ReplaceAll(s, "$", "")
	s = reBrSp.ReplaceAllString(s, "^$1")
	return reSci.ReplaceAllStringFunc(s, func(m string) string {
		g := reSci.FindStringSubmatch(m)
		mant, exp := g[1], g[2]
		if mant == "" {
			mant, exp = g[3], g[4]
		}
		return mant + "E" + strings.TrimPrefix(exp, "+")
	})
}

// numbersIn returns the numeric tokens in s: commas only as thousands
// separators ("F(2,30)" is 2 and 30), 3.1×10⁻⁵ / 3.07e-5 as one number, and
// a minus sign only when it is not a range dash ("2.5-12").
func numbersIn(s string) []string {
	s = normalizeNums(s)
	var out []string
	for _, loc := range reNum.FindAllStringIndex(s, -1) {
		tok := strings.ReplaceAll(s[loc[0]:loc[1]], ",", "")
		i := loc[0]
		if i > 0 && s[i-1] == '-' && (i == 1 || s[i-2] < '0' || s[i-2] > '9') {
			tok = "-" + tok
		}
		out = append(out, tok)
	}
	return out
}

func numVal(tok string) float64 {
	f, err := strconv.ParseFloat(strings.Replace(tok, "E", "e", 1), 64)
	if err != nil {
		return math.NaN()
	}
	return f
}

// precision is half a unit in the last written place: "57" -> 0.5, "3.1E-5" -> 0.05e-5.
func precision(tok string) float64 {
	mant, exp := tok, ""
	if k := strings.Index(strings.ToUpper(tok), "E"); k >= 0 {
		mant, exp = tok[:k], tok[k+1:]
	}
	dec := 0
	if k := strings.Index(mant, "."); k >= 0 {
		dec = len(mant) - k - 1
	}
	p := 0.5 * math.Pow(10, -float64(dec))
	if dec == 0 && exp == "" {
		// a whole number written with trailing zeros ("13,000", "270") is
		// rounded at that place, but never by more than 5% of its value
		digits := strings.ReplaceAll(mant, ",", "")
		z := len(digits) - len(strings.TrimRight(digits, "0"))
		if z > 0 && z < len(digits) {
			v, _ := strconv.ParseFloat(digits, 64)
			p = math.Min(0.5*math.Pow(10, float64(z)), math.Max(0.5, 0.05*math.Abs(v)))
		}
	}
	if exp != "" {
		e, _ := strconv.Atoi(exp)
		p *= math.Pow(10, float64(e))
	}
	return p
}

func numEq(x, y string, scale float64) bool {
	fx, fy := numVal(x), numVal(y)*scale
	// "5,200" for 5,246: a whole number with trailing zeros may also be the
	// value truncated at that place (rounded down, never up past the unit),
	// within the same 5% cap as precision().
	if !strings.ContainsAny(x, ".eE") {
		digits := strings.TrimLeft(strings.ReplaceAll(x, ",", ""), "+-")
		if z := len(digits) - len(strings.TrimRight(digits, "0")); z > 0 && z < len(digits) {
			// rounding up would inflate ("40" for 39 sites), so a rounded
			// whole number must not exceed its source
			unit := math.Min(math.Pow(10, float64(z)), math.Max(1, 0.05*math.Abs(fx)))
			return fy >= fx-1e-12*math.Max(1, math.Abs(fx)) && fy < fx+unit
		}
	}
	return math.Abs(fx-fy) <= precision(x)+1e-12*math.Max(1, math.Abs(fx))
}

// sameNumbers: every number in the claim is the matching source number
// rounded to the claim's precision (a claim may round, never inflate). When
// the source carries extra numbers, the claim's numbers must still appear in
// order. A percentage may be stated as a fraction in the source.
func sameNumbers(claim, source string) bool {
	a, b := numbersIn(claim), numbersIn(source)
	if len(a) == 0 || len(b) == 0 {
		return len(a) == 0 && len(b) == 0
	}
	match := func(scale float64) bool {
		if len(a) == len(b) {
			ok := true
			for i := range a {
				if !numEq(a[i], b[i], scale) {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
		j := 0
		for _, x := range a {
			for j < len(b) && !numEq(x, b[j], scale) {
				j++
			}
			if j == len(b) {
				return false
			}
			j++
		}
		return true
	}
	if match(1) {
		return true
	}
	if strings.Contains(claim, "%") {
		for _, y := range b {
			if math.Abs(numVal(y)) > 1 {
				return false
			}
		}
		return match(100)
	}
	return false
}

// maskValueDigits hides, inside a hint, every number that equals one of the
// claim's numbers, so a "blind" verifier is not handed the answer.
func maskValueDigits(hint, value string) string {
	vals := numbersIn(value)
	if len(vals) == 0 {
		return hint
	}
	n := normalizeNums(hint)
	return reNum.ReplaceAllStringFunc(n, func(tok string) string {
		t := strings.ReplaceAll(tok, ",", "")
		for _, v := range vals {
			v = strings.TrimPrefix(v, "-")
			if numEq(v, t, 1) || numEq(v, t, 100) {
				return "#"
			}
		}
		return tok
	})
}

// figureKind classifies a claim value: pct, mult, time, rate, or count.
// Percentages, multiples, durations and rates are "impact" figures.
var (
	rePct  = regexp.MustCompile(`%|\bpp\b|percentage points?|\bpoints?\b`)
	reMult = regexp.MustCompile(`×|\d\s?x\b|\bfaster\b|\bslower\b`)
	reRate = regexp.MustCompile(`(?i)/s\b|per second|tok/s|tokens/s|GFLOP/s|GB/s|MFU`)
	reTime = regexp.MustCompile(`(?i)\d\s?(ms|µs|us|ns|s|sec|seconds?|min|minutes?|h|hours?|days?|weeks?)\b`)
)

var reFrac = regexp.MustCompile(`^\s*\d+/\d+`)

func figureKind(v string) string {
	switch {
	case reFrac.MatchString(v): // 18/25: a success rate
		return "pct"
	case reRate.MatchString(v):
		return "rate"
	case rePct.MatchString(v):
		return "pct"
	case reMult.MatchString(v):
		return "mult"
	case reTime.MatchString(v):
		return "time"
	}
	return "count"
}
