package main

import (
	"fmt"
	"html"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	tokenRe  = regexp.MustCompile(`\{\{(clm[A-Za-z]+)\}\}`)
	boldRe   = regexp.MustCompile(`\*\*(.+?)\*\*`)
	italicRe = regexp.MustCompile(`(^|[^*\w])\*([^*]+?)\*($|[^*\w])`)
	linkRe   = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)]+)\)`)
)

// plainText strips inline markup (tokens are left as-is).
func plainText(s string) string {
	s = linkRe.ReplaceAllString(s, "$1")
	s = boldRe.ReplaceAllString(s, "$1")
	s = italicRe.ReplaceAllString(s, "$1$2$3")
	return s
}

// ClaimView is what renderers and lint need from claims.json.
type ClaimView struct {
	Value              string   `json:"value"`
	Kind               string   `json:"kind"`
	Check              string   `json:"check"`
	Source             string   `json:"source"`
	DoubleVerified     bool     `json:"double_verified"`
	ConfirmedSentences []string `json:"confirmed_sentences"`
	Problems           []string `json:"problems"`
	Caution            string   `json:"caution,omitempty"`
	// RefutedSentences: sentence hash -> verdict and evidence, for sentences
	// whose latest blind pass was SCOPE, MISMATCH, UNSUPPORTED or
	// QUOTE_NOT_FOUND. Lint refuses that exact wording even in drafts.
	RefutedSentences map[string]string `json:"refuted_sentences,omitempty"`
}

type StatementView struct {
	TextHash  string `json:"text_hash"`
	SourcesOK bool   `json:"sources_ok"`
	Confirmed bool   `json:"confirmed"`
	Attested  string `json:"attested,omitempty"`
	Problem   string `json:"problem,omitempty"`
	// Refuted: set when the latest statement check of this exact wording was
	// UNSUPPORTED; lint refuses the wording even in drafts.
	Refuted string `json:"refuted,omitempty"`
}

type ClaimsFile struct {
	Generated  string                    `json:"generated"`
	ResumeHash string                    `json:"resume_hash"`
	Selection  string                    `json:"selection"`
	VMReport   map[string]string         `json:"vm_report"`
	Claims     map[string]*ClaimView     `json:"claims"`
	Statements map[string]*StatementView `json:"statements"`
	// Guards: the export records refuted sentences (RefutedSentences,
	// StatementView.Refuted). 0 means an older rkb wrote it and the
	// refuted-wording check cannot run on it.
	Guards int `json:"guards,omitzero"`
}

func loadClaimsFile(p string) *ClaimsFile {
	var cf ClaimsFile
	if err := readJSON(p, &cf); err != nil {
		die("claims file: %v (make it with `rkb claims export --used-in <resume>` where the repos are)", err)
	}
	if cf.Claims == nil {
		cf.Claims = map[string]*ClaimView{}
	}
	if cf.Statements == nil {
		cf.Statements = map[string]*StatementView{}
	}
	return &cf
}

// ---------------------------------------------------------------- resolver

type resolver struct {
	claims     map[string]*ClaimView
	final      bool
	bold       bool
	used       map[string]bool
	unverified map[string]bool
	missing    map[string]bool
}

func newResolver(cf *ClaimsFile, final, bold bool) *resolver {
	return &resolver{claims: cf.Claims, final: final, bold: bold,
		used: map[string]bool{}, unverified: map[string]bool{}, missing: map[string]bool{}}
}

func (r *resolver) value(id string) (string, bool) {
	r.used[id] = true
	c, ok := r.claims[id]
	if !ok {
		r.missing[id] = true
		return "[" + id + "?]", false
	}
	if !c.DoubleVerified {
		r.unverified[id] = true
		return c.Value, false
	}
	return c.Value, true
}

func (r *resolver) text(s string) string {
	s = tokenRe.ReplaceAllStringFunc(s, func(m string) string {
		v, _ := r.value(tokenRe.FindStringSubmatch(m)[1])
		return v
	})
	s = boldRe.ReplaceAllString(s, "$1")
	s = italicRe.ReplaceAllString(s, "$1$2$3")
	s = linkRe.ReplaceAllStringFunc(s, func(m string) string {
		g := linkRe.FindStringSubmatch(m)
		if strings.Contains(g[2], strings.TrimPrefix(strings.TrimPrefix(g[1], "https://"), "http://")) {
			return g[1]
		}
		return g[1] + " (" + g[2] + ")"
	})
	return s
}

func (r *resolver) html(s string) string {
	var b strings.Builder
	last := 0
	for _, loc := range tokenRe.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(html.EscapeString(s[last:loc[0]]))
		id := s[loc[2]:loc[3]]
		v, ok := r.value(id)
		v = html.EscapeString(v)
		if r.bold {
			v = "<b>" + v + "</b>"
		}
		if !ok && !r.final {
			v = `<span class="unverified" title="` + id + `">` + v + `</span>`
		}
		b.WriteString("\x02" + v + "\x03")
		last = loc[1]
	}
	b.WriteString(html.EscapeString(s[last:]))
	h := b.String()
	// markup applies around the protected figures
	h = boldRe.ReplaceAllString(h, "<b>$1</b>")
	h = italicRe.ReplaceAllString(h, "$1<i>$2</i>$3")
	h = linkRe.ReplaceAllString(h, `<a href="$2">$1</a>`)
	h = strings.NewReplacer("\x02", "", "\x03", "").Replace(h)
	return h
}

// ---------------------------------------------------------------- ascii

var asciiRepl = strings.NewReplacer(
	"×", "x", "→", "->", "←", "<-", "–", "-", "—", "-", "−", "-", "“", `"`, "”", `"`,
	"‘", "'", "’", "'", "…", "...", "µ", "u", "≈", "~", "≤", "<=", "≥", ">=", "±", "+/-",
	"·", "-", "•", "-", "Δ", "delta ", "ρ", "rho", "√", "sqrt ", " ", " ", " ", " ",
	"Š", "S", "š", "s", "á", "a", "é", "e", "í", "i", "ó", "o", "ö", "o", "ü", "u", "è", "e",
)

func toASCII(s string) string {
	var b strings.Builder
	for _, r := range s {
		if v, ok := unsupMap[r]; ok {
			b.WriteString("^" + string(v))
			continue
		}
		b.WriteRune(r)
	}
	s = asciiRepl.Replace(b.String())
	s = regexp.MustCompile(`\^(-?)\^?`).ReplaceAllString(s, "^$1")
	var out strings.Builder
	for _, r := range s {
		if r < 128 {
			out.WriteRune(r)
		} else {
			out.WriteRune('?')
		}
	}
	return out.String()
}

// ---------------------------------------------------------------- render

type rendered struct {
	Text, HTML string
	R          *resolver
}

func renderResume(c *Composed, cf *ClaimsFile, final bool) rendered {
	R := newResolver(cf, final, c.boldFigures())
	var txt []string
	var body []string
	hd := c.Header

	txt = append(txt, strings.ToUpper(R.text(hd.Name)))
	if hd.Headline != "" {
		txt = append(txt, R.text(hd.Headline))
	}
	var cl []string
	for _, x := range hd.Contact {
		if x != "" {
			cl = append(cl, R.text(x))
		}
	}
	txt = append(txt, strings.Join(cl, " | "))
	body = append(body, `<header class="top">`, `<h1>`+R.html(hd.Name)+`</h1><div class="namerule"></div>`)
	if hd.Headline != "" {
		body = append(body, `<div class="headline">`+R.html(hd.Headline)+`</div>`)
	}
	var cs strings.Builder
	cs.WriteString(`<div class="contact">`)
	for _, x := range hd.Contact {
		cs.WriteString("<span>" + R.html(x) + "</span>")
	}
	cs.WriteString("</div>")
	body = append(body, cs.String(), "</header>")

	bulletList := func(bs []*Bullet) {
		body = append(body, "<ul>")
		for _, b := range bs {
			txt = append(txt, "- "+R.text(b.Text))
			body = append(body, fmt.Sprintf(`<li data-id="%s">%s</li>`, html.EscapeString(b.ID), R.html(b.Text)))
		}
		body = append(body, "</ul>")
	}

	for _, s := range c.Sections {
		txt = append(txt, "", strings.ToUpper(s.Title))
		body = append(body, fmt.Sprintf(`<section data-kind="%s"><h2>%s</h2>`, s.Kind, html.EscapeString(s.Title)))
		if s.Lead != "" {
			txt = append(txt, R.text(s.Lead))
			body = append(body, `<div class="seclead">`+R.html(s.Lead)+`</div>`)
		}
		switch s.Kind {
		case "bullets":
			bulletList(s.Bullets)
		case "lines":
			if s.Bulleted {
				var bs []*Bullet
				for i, l := range s.Lines {
					bs = append(bs, &Bullet{ID: fmt.Sprintf("%s-%d", slug(s.Title, 20), i+1), Text: l.Text})
				}
				bulletList(bs)
				break
			}
			labeled := false
			for _, l := range s.Lines {
				if l.Label != "" {
					labeled = true
				}
			}
			cls := "lines"
			if labeled {
				cls += " labeled"
			}
			body = append(body, `<div class="`+cls+`">`)
			for _, l := range s.Lines {
				t, h := R.text(l.Text), "<span>"+R.html(l.Text)+"</span>"
				if l.Label != "" {
					t = R.text(l.Label) + ": " + t
					h = `<span class="lbl">` + R.html(l.Label) + `:</span> ` + h
				}
				txt = append(txt, t)
				body = append(body, "<p>"+h+"</p>")
			}
			body = append(body, "</div>")
		default:
			for _, e := range s.Entries {
				stacked := e.Layout == "stacked"
				full := e.Title
				if len(e.Members) > 0 {
					full += ": " + strings.Join(e.Members, ", ")
				}
				right, rcls := e.Dates, "d"
				if right == "" {
					right, rcls = e.Stack, "d stack"
				}
				tl := R.text(full)
				if e.Org != "" && !stacked {
					tl += ", " + R.text(e.Org)
				}
				if right != "" {
					tl += " | " + R.text(right)
				}
				txt = append(txt, "", tl)
				left := `<span class="ttl">` + R.html(full) + `</span>`
				if e.Org != "" && !stacked {
					left += ", " + R.html(e.Org)
				}
				body = append(body, `<div class="entry">`,
					fmt.Sprintf(`<div class="eh"><span class="t">%s</span><span class="%s">%s</span></div>`, left, rcls, R.html(right)))
				var sub []string
				if stacked && e.Org != "" {
					sub = append(sub, e.Org)
				}
				if e.Location != "" {
					sub = append(sub, e.Location)
				}
				if e.Dates != "" && e.Stack != "" {
					sub = append(sub, e.Stack)
				}
				if e.Sub != "" {
					sub = append(sub, e.Sub)
				}
				if len(sub) > 0 {
					var st, sh []string
					for _, x := range sub {
						st = append(st, R.text(x))
						sh = append(sh, R.html(x))
					}
					txt = append(txt, strings.Join(st, " | "))
					body = append(body, `<div class="sub">`+strings.Join(sh, ", ")+`</div>`)
				}
				if e.Lead != "" {
					txt = append(txt, R.text(e.Lead))
					body = append(body, `<div class="lead">`+R.html(e.Lead)+`</div>`)
				}
				if len(e.Bullets) > 0 {
					bulletList(e.Bullets)
				}
				body = append(body, "</div>")
			}
		}
		body = append(body, "</section>")
	}

	paper := c.paper()
	m := cfg.Layout.Margin
	pw := "8.5"
	if paper == "a4" {
		pw = "8.27"
	}
	var sv strings.Builder
	for _, k := range sortedKeys(c.Meta.Style) {
		fmt.Fprintf(&sv, "  %s: %s;\n", k, c.Meta.Style[k])
	}
	f := func(x float64) string { return fmt.Sprintf("%g", x) }
	page := strings.NewReplacer(
		"__TITLE__", html.EscapeString(hd.Name+" — Resume"),
		"__STYLE_VARS__", sv.String(),
		"__PAPER__", paper,
		"__PAPER_W__", pw,
		"__MARGIN_TOP__", f(m["top"]), "__MARGIN_BOTTOM__", f(m["bottom"]),
		"__MARGIN_LEFT__", f(m["left"]), "__MARGIN_RIGHT__", f(m["right"]),
		"__BODY__", strings.Join(body, "\n"),
	).Replace(templateHTML())
	text := strings.TrimSpace(strings.Join(txt, "\n")) + "\n"
	text = regexp.MustCompile(`\n{3,}`).ReplaceAllString(text, "\n\n")
	return rendered{Text: text, HTML: page, R: R}
}

func cmdRender(a *args) {
	sel := a.need(0, "resume (selection or composed)")
	c := loadResume(sel)
	cf := loadClaimsFile(a.get("claims", "claims.json"))
	final := a.bools["final"]
	out := a.get("out-dir", ".")
	if final {
		if errs := finalPreconditions(c, cf, a.get("eco", "")); len(errs) > 0 {
			for _, e := range errs {
				fmt.Println("ERROR " + e)
			}
			die("final render blocked (%d problem(s)); run `rkb lint --final` for detail", len(errs))
		}
	}
	r := renderResume(c, cf, final)
	if len(r.R.missing) > 0 {
		die("unregistered figure token(s): %s", strings.Join(sortedKeys(r.R.missing), ", "))
	}
	base := c.basename(sel)
	writeFile(filepath.Join(out, base+".txt"), r.Text)
	writeFile(filepath.Join(out, base+".ascii.txt"), toASCII(r.Text))
	writeFile(filepath.Join(out, base+".html"), r.HTML)
	msg := fmt.Sprintf("wrote %s.{txt,ascii.txt,html} in %s (%d figures", base, out, len(r.R.used))
	if len(r.R.unverified) > 0 {
		msg += fmt.Sprintf(", %d NOT double-verified: %s", len(r.R.unverified), strings.Join(sortedKeys(r.R.unverified), ", "))
	}
	fmt.Println(msg + ")")
}
