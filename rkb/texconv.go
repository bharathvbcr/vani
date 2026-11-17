package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Conversion between the LaTeX CV sources and the bank's inline markup:
//   **bold**  *italic*  [text](https://url)  {{clmName}}
// texToMD is used by `rkb bank import-tex`; mdToTeX by `rkb latex`.

func stripComments(tex string) string {
	lines := strings.Split(tex, "\n")
	for i, l := range lines {
		for j := 0; j < len(l); j++ {
			if l[j] == '\\' {
				j++
				continue
			}
			if l[j] == '%' {
				lines[i] = l[:j]
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

// readGroup: s[i] (after spaces) must be '{'; returns inner text and index after '}'.
func readGroup(s string, i int) (string, int, error) {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	if i >= len(s) || s[i] != '{' {
		return "", i, fmt.Errorf("expected '{' at %d: %q", i, s[i:min(len(s), i+30)])
	}
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[i+1 : j], j + 1, nil
			}
		}
	}
	return "", i, fmt.Errorf("unbalanced braces")
}

func readArgs(s string, i, n int) ([]string, int, error) {
	var out []string
	for k := 0; k < n; k++ {
		a, j, err := readGroup(s, i)
		if err != nil {
			return nil, i, err
		}
		out = append(out, a)
		i = j
	}
	return out, i, nil
}

var mathMap = [][2]string{
	{`\rightarrow`, "→"}, {`\to`, "→"}, {`\leftarrow`, "←"}, {`\times`, "×"}, {`\sim`, "~"},
	{`\approx`, "≈"}, {`\pm`, "±"}, {`\leq`, "≤"}, {`\geq`, "≥"}, {`\le`, "≤"}, {`\ge`, "≥"},
	{`\lt`, "<"}, {`\gt`, ">"}, {`\textless`, "<"}, {`\textgreater`, ">"},
	{`\mu`, "µ"}, {`\rho`, "ρ"}, {`\Delta`, "Δ"}, {`\alpha`, "α"}, {`\beta`, "β"}, {`\tau`, "τ"},
	{`\cdot`, "·"}, {`\circ`, "°"}, {`\%`, "%"},
}
var accents = map[string]string{"vS": "Š", "vs": "š", "'a": "á", "'e": "é", "'i": "í", "'o": "ó",
	`"o`: "ö", `"u`: "ü", "`e": "è"}

func mathToText(m string) string {
	m = regexp.MustCompile(`\\sqrt\{([^}]*)\}`).ReplaceAllString(m, "√$1")
	m = regexp.MustCompile(`\\sqrt\s*(\w)`).ReplaceAllString(m, "√$1")
	for _, p := range mathMap {
		m = strings.ReplaceAll(m, p[0], p[1])
	}
	m = regexp.MustCompile(`\^\{([^}]*)\}`).ReplaceAllStringFunc(m, func(x string) string {
		return toSup(x[2 : len(x)-1])
	})
	m = regexp.MustCompile(`\^(\S)`).ReplaceAllStringFunc(m, func(x string) string { return toSup(x[1:]) })
	m = regexp.MustCompile(`_\{([^}]*)\}`).ReplaceAllString(m, "_$1")
	return strings.NewReplacer("{", "", "}", "").Replace(m)
}

var reCmd = regexp.MustCompile(`^\\([A-Za-z]+)\*?`)
var reClmCmd = regexp.MustCompile(`^\\(clm[A-Za-z]+)(\{\})?`)

func texToMD(s string, unknown map[string]bool) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '$':
			j := strings.IndexByte(s[i+1:], '$')
			if j < 0 {
				j = len(s) - i - 1
			}
			out.WriteString(mathToText(s[i+1 : i+1+j]))
			i = i + 2 + j
			continue
		case c == '\\':
			m := reCmd.FindStringSubmatch(s[i:])
			if m == nil {
				if i+1 >= len(s) {
					i++
					continue
				}
				nx := s[i+1]
				if (nx == '\'' || nx == '"' || nx == '`') && i+2 < len(s) {
					if s[i+2] == '{' {
						inner, k, _ := readGroup(s, i+2)
						out.WriteString(orStr(accents[string(nx)+inner], inner))
						i = k
					} else {
						out.WriteString(orStr(accents[string(nx)+string(s[i+2])], string(s[i+2])))
						i += 3
					}
					continue
				}
				switch nx {
				case ',':
					out.WriteString(" ")
				case '\\', ' ':
					out.WriteString(" ")
				default:
					out.WriteByte(nx)
				}
				i += 2
				continue
			}
			name := m[1]
			k := i + len(m[0])
			if strings.HasPrefix(name, "clm") {
				if cm := reClmCmd.FindStringSubmatch(s[i:]); cm != nil {
					out.WriteString("{{" + cm[1] + "}}")
					i += len(cm[0])
					continue
				}
				// a bare \clm names no claim macro: keep it as plain text
			}
			switch name {
			case "textbf", "textit", "emph", "texttt", "text", "mbox", "textsc", "underline":
				inner, kk, err := readGroup(s, k)
				if err != nil {
					i = k
					continue
				}
				body := texToMD(inner, unknown)
				switch name {
				case "textbf":
					if regexp.MustCompile(`^\s*\{\{clm[A-Za-z]+\}\}\s*$`).MatchString(body) {
						out.WriteString(body) // figures print bold anyway
					} else {
						out.WriteString("**" + strings.ReplaceAll(body, "**", "") + "**")
					}
				case "textit", "emph":
					out.WriteString("*" + body + "*")
				default:
					out.WriteString(body)
				}
				i = kk
			case "href":
				a, kk, err := readArgs(s, k, 2)
				if err != nil {
					i = k
					continue
				}
				label := strings.NewReplacer("**", "", "*", "").Replace(texToMD(a[1], unknown))
				out.WriteString("[" + label + "](" + a[0] + ")")
				i = kk
			case "v":
				inner, kk, _ := readGroup(s, k)
				out.WriteString(orStr(accents["v"+inner], inner))
				i = kk
			case "needspace", "vspace", "hspace", "label":
				_, kk, err := readGroup(s, k)
				if err != nil {
					kk = k
				}
				i = kk
			case "normalfont", "small", "footnotesize", "scriptsize", "large", "Large", "centering",
				"noindent", "par", "newline", "linebreak", "relax":
				i = k
			case "ldots":
				out.WriteString("…")
				i = k
			case "textless":
				out.WriteString("<")
				i = k
			case "textgreater":
				out.WriteString(">")
				i = k
			default:
				unknown[name] = true
				i = k
			}
			continue
		case strings.HasPrefix(s[i:], "---"):
			out.WriteString("—")
			i += 3
			continue
		case strings.HasPrefix(s[i:], "--"):
			out.WriteString("–")
			i += 2
			continue
		case strings.HasPrefix(s[i:], "``"):
			out.WriteString("“")
			i += 2
			continue
		case strings.HasPrefix(s[i:], "''"):
			out.WriteString("”")
			i += 2
			continue
		case c == '~':
			out.WriteString(" ")
		case c == '{' || c == '}':
		default:
			out.WriteByte(c)
		}
		i++
	}
	t := squash(out.String())
	t = strings.NewReplacer("** **", " ", "****", "").Replace(t)
	return t
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---------------------------------------------------------------- md -> TeX

var texEsc = map[rune]string{'&': `\&`, '%': `\%`, '$': `\$`, '#': `\#`, '_': `\_`, '{': `\{`, '}': `\}`,
	'^': `\^{}`, '\\': `\textbackslash{}`, '<': `$<$`, '>': `$>$`, '→': `$\rightarrow$`, '←': `$\leftarrow$`, '×': `$\times$`,
	'~': `$\sim$`, '≈': `$\approx$`, '±': `$\pm$`, '≤': `$\leq$`, '≥': `$\geq$`, 'µ': `$\mu$`, 'ρ': `$\rho$`,
	'Δ': `$\Delta$`, 'α': `$\alpha$`, 'τ': `$\tau$`, '·': `$\cdot$`, '–': "--", '—': "---", '“': "``",
	'”': "''", '’': "'", '‘': "`", '…': `\ldots{}`, ' ': "~", ' ': `\,`, 'Š': `\v{S}`, 'š': `\v{s}`,
	'á': `\'a`, 'é': `\'e`, 'í': `\'i`, 'ó': `\'o`, 'ö': `\"o`, 'ü': `\"u`, 'è': "\\`e", '√': `$\surd$`, '°': `$^\circ$`}

func escPlain(t string) string {
	var b strings.Builder
	rs := []rune(t)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if _, ok := unsupMap[r]; ok {
			j := i
			var sup strings.Builder
			for j < len(rs) {
				v, ok := unsupMap[rs[j]]
				if !ok {
					break
				}
				sup.WriteRune(v)
				j++
			}
			b.WriteString("$^{" + sup.String() + "}$")
			i = j - 1
			continue
		}
		if e, ok := texEsc[r]; ok {
			b.WriteString(e)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var mdSplit = regexp.MustCompile(`\*\*.+?\*\*|\x00\d+\x00|\*[^*]+?\*`)

func mdToTeX(s string, boldFigures bool) string {
	var holders []string
	hold := func(x string) string {
		holders = append(holders, x)
		return fmt.Sprintf("\x00%d\x00", len(holders)-1)
	}
	s = linkRe.ReplaceAllStringFunc(s, func(m string) string {
		g := linkRe.FindStringSubmatch(m)
		return hold(`\href{` + g[2] + `}{` + mdToTeX(g[1], boldFigures) + `}`)
	})
	s = tokenRe.ReplaceAllStringFunc(s, func(m string) string {
		id := tokenRe.FindStringSubmatch(m)[1]
		if boldFigures {
			return hold(`\textbf{\` + id + `}`)
		}
		return hold(`\` + id + `{}`)
	})
	res := texSegments(s)
	return regexp.MustCompile("\x00(\\d+)\x00").ReplaceAllStringFunc(res, func(m string) string {
		k, err := strconv.Atoi(strings.Trim(m, "\x00"))
		if err != nil || k < 0 || k >= len(holders) {
			die("latex: internal placeholder %q has no held text", strings.Trim(m, "\x00"))
		}
		return holders[k]
	})
}

// texSegments converts bold/italic markup and escapes plain text, leaving
// placeholders for links and figures untouched (resolved by mdToTeX).
func texSegments(s string) string {
	var out strings.Builder
	last := 0
	for _, loc := range mdSplit.FindAllStringIndex(s, -1) {
		out.WriteString(escPlain(s[last:loc[0]]))
		p := s[loc[0]:loc[1]]
		switch {
		case strings.HasPrefix(p, "**"):
			out.WriteString(`\textbf{` + texSegments(p[2:len(p)-2]) + `}`)
		case strings.HasPrefix(p, "\x00"):
			out.WriteString(p)
		default:
			out.WriteString(`\textit{` + texSegments(p[1:len(p)-1]) + `}`)
		}
		last = loc[1]
	}
	out.WriteString(escPlain(s[last:]))
	return out.String()
}
