package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// import-tex: one-time migration of the LaTeX CV sources into the bank.
// Every top-level document (Resume.tex, Curriculum_Vitae.tex, Resume_*.tex)
// is expanded the way TeX would (\input, \resumetrue, \cvonly/\resonly/
// \variant, \ifprojectbankonly, \proj*/\pub* macros), then its sections,
// entries, bullets and skills rows are read into bank entries/bullets (one
// copy of each distinct sentence) and one selection file per document.

type texExpander struct {
	cv       string
	resume   bool
	bankonly bool
	macros   map[string]string
	unknown  map[string]bool
	depth    int
}

var reNewcmd = regexp.MustCompile(`^\\newcommand\{\\([A-Za-z]+)\}`)
var reCtl = regexp.MustCompile(`^\\([A-Za-z]+)`)

func (x *texExpander) file(name string) string {
	base := filepath.Base(name)
	if base == "Preamble.tex" || base == "Header.tex" || strings.HasPrefix(base, "Claims") {
		return ""
	}
	p := filepath.Join(x.cv, name)
	if !strings.HasSuffix(p, ".tex") {
		p += ".tex"
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	x.depth++
	defer func() { x.depth-- }()
	if x.depth > 20 {
		return ""
	}
	return x.eval(stripComments(string(b)))
}

func (x *texExpander) eval(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '\\' {
			out.WriteByte(s[i])
			i++
			continue
		}
		if m := reNewcmd.FindStringSubmatch(s[i:]); m != nil {
			k := i + len(m[0])
			if k < len(s) && s[k] == '[' { // macros with arguments are layout, not content
				out.WriteString(m[0])
				i = k
				continue
			}
			body, kk, err := readGroup(s, k)
			if err == nil {
				x.macros[m[1]] = body
				i = kk
				continue
			}
		}
		m := reCtl.FindStringSubmatch(s[i:])
		if m == nil {
			out.WriteString(s[i:min(i+2, len(s))])
			i += 2
			continue
		}
		name, k := m[1], i+len(m[0])
		switch name {
		case "input":
			f, kk, err := readGroup(s, k)
			if err == nil {
				out.WriteString(x.file(f))
				i = kk
				continue
			}
		case "resumetrue":
			x.resume = true
			i = k
			continue
		case "resumefalse":
			x.resume = false
			i = k
			continue
		case "projectbankonlytrue":
			x.bankonly = true
			i = k
			continue
		case "projectbankonlyfalse":
			x.bankonly = false
			i = k
			continue
		case "cvonly", "resonly":
			body, kk, err := readGroup(s, k)
			if err == nil {
				if (name == "cvonly") != x.resume {
					out.WriteString(x.eval(body))
				}
				i = kk
				continue
			}
		case "variant":
			a, kk, err := readArgs(s, k, 2)
			if err == nil {
				if x.resume {
					out.WriteString(x.eval(a[1]))
				} else {
					out.WriteString(x.eval(a[0]))
				}
				i = kk
				continue
			}
		case "ifprojectbankonly":
			rest := s[k:]
			fi := strings.Index(rest, `\fi`)
			if fi >= 0 {
				block := rest[:fi]
				yes, no := block, ""
				if el := strings.Index(block, `\else`); el >= 0 {
					yes, no = block[:el], block[el+len(`\else`):]
				}
				if x.bankonly {
					out.WriteString(x.eval(yes))
				} else {
					out.WriteString(x.eval(no))
				}
				i = k + fi + len(`\fi`)
				continue
			}
		}
		if body, ok := x.macros[name]; ok && (strings.HasPrefix(name, "proj") || strings.HasPrefix(name, "pub")) {
			out.WriteString(x.eval(body))
			i = k
			if strings.HasPrefix(s[i:], "{}") {
				i += 2
			}
			continue
		}
		out.WriteString(m[0])
		i = k
	}
	return out.String()
}

type impSection struct {
	Title   string
	Entries []*impEntry
	Items   []string
	Rows    []Line
}

type impEntry struct {
	Kind                           string
	Title, Org, Dates, Stack, Lead string
	Items                          []string
}

var reStruct = regexp.MustCompile(`\\(section|resumeProfessionalExperience|resumeAcademicProject|resumeSubheading|resumeItem|begin\{tabularx\})`)

func scanStructure(s string, unknown map[string]bool) []*impSection {
	var secs []*impSection
	var cur *impSection
	var ent *impEntry
	md := func(t string) string { return texToMD(t, unknown) }
	for i := 0; i < len(s); {
		loc := reStruct.FindStringSubmatchIndex(s[i:])
		if loc == nil {
			break
		}
		name := s[i+loc[2] : i+loc[3]]
		k := i + loc[1]
		switch name {
		case "section":
			t, kk, err := readGroup(s, k)
			if err != nil {
				i = k
				continue
			}
			cur = &impSection{Title: plainText(md(t))}
			ent = nil
			secs = append(secs, cur)
			i = kk
		case "resumeProfessionalExperience", "resumeAcademicProject", "resumeSubheading":
			n := map[string]int{"resumeProfessionalExperience": 3, "resumeAcademicProject": 2, "resumeSubheading": 4}[name]
			a, kk, err := readArgs(s, k, n)
			if err != nil || cur == nil {
				i = k
				continue
			}
			e := &impEntry{Kind: name}
			switch name {
			case "resumeProfessionalExperience":
				e.Title, e.Org, e.Dates = md(a[0]), md(a[1]), md(a[2])
			case "resumeAcademicProject":
				e.Title, e.Stack = md(a[0]), md(a[1])
			default:
				e.Title, e.Dates, e.Org = md(a[0]), md(a[1]), md(a[2])
			}
			cur.Entries = append(cur.Entries, e)
			ent = e
			i = kk
		case "resumeItem":
			a, kk, err := readGroup(s, k)
			if err != nil || cur == nil {
				i = k
				continue
			}
			t := md(a)
			if t != "" {
				if ent != nil {
					ent.Items = append(ent.Items, t)
				} else {
					cur.Items = append(cur.Items, t)
				}
			}
			i = kk
		default: // tabularx skills rows
			end := strings.Index(s[k:], `\end{tabularx}`)
			if end < 0 || cur == nil {
				i = k
				continue
			}
			body := s[k : k+end]
			if _, kk, err := readArgs(body, 0, 2); err == nil {
				body = body[kk:]
			}
			for _, row := range strings.Split(body, `\\`) {
				amp := -1
				for j := 0; j < len(row); j++ {
					if row[j] == '\\' {
						j++
						continue
					}
					if row[j] == '&' {
						amp = j
						break
					}
				}
				if amp < 0 {
					continue
				}
				parts := []string{row[:amp], row[amp+1:]}
				lbl := strings.TrimSuffix(strings.TrimSpace(plainText(md(parts[0]))), ":")
				cur.Rows = append(cur.Rows, Line{Label: lbl, Text: md(parts[1])})
			}
			i = k + end + len(`\end{tabularx}`)
		}
	}
	return secs
}

func bulletKey(text string) string {
	p := squash(plainText(tokenRe.ReplaceAllString(text, " ")))
	w := strings.Fields(p)
	if len(w) > 6 {
		w = w[:6]
	}
	return slug(strings.Join(w, " "), 48)
}

// texLabel is the output label for a LaTeX source: config.json tex_labels when
// it names the stem, else Master_CV for the full CV, else the stem without a
// "Resume_" prefix.
func texLabel(stem string) string {
	if l := strings.TrimSpace(cfg.TexLabels[stem]); l != "" {
		return l
	}
	if stem == "Curriculum_Vitae" {
		return "Master_CV"
	}
	return strings.TrimPrefix(stem, "Resume_")
}

func cmdImportTex(a *args) {
	cv := inRoot(cfg.CVDir)
	bp := a.get("bank", inRoot(cfg.Bank))
	rd := a.get("resumes", filepath.Join(filepath.Dir(bp), "resumes"))
	if fileExists(bp) && !a.bools["force"] {
		die("%s already exists: the bank is the source of truth now. Re-importing would overwrite edits; pass --force only if you mean it", bp)
	}
	// The guard is what save compares and swaps against: what is on disk now
	// (nothing, or the bank --force replaces). Without it save had no path.
	_, guard, err := readGuarded(bp)
	if err != nil {
		die("bank: %v", err)
	}
	docs, _ := filepath.Glob(filepath.Join(cv, "*.tex"))
	if len(docs) == 0 { // with --force, importing nothing would replace the bank with an empty one
		die("no .tex files in %s: nothing to import, the bank is left as it is", cv)
	}
	sort.Strings(docs)
	bank := &Bank{guard: guard, About: "Single source for every resume and the CV. Edit a bullet here once; resumes/*.json choose and order them. Imported from the LaTeX sources by `rkb bank import-tex`; generated LaTeX lives in generated/.",
		Headers: map[string]Header{}, Entries: map[string]*BankEntry{}, Bullets: map[string]*Bullet{}, Lines: map[string][]Line{}, path: bp}
	// The header starts blank: the name and contact lines are the user's own,
	// written into bank.json (headers.default) after the import, never compiled in.
	bank.Headers["default"] = Header{}
	unknown := map[string]bool{}
	byText := map[string]string{}
	lineSets := map[string]string{} // content json -> key
	nDocs := 0
	for _, d := range docs {
		base := filepath.Base(d)
		src, _ := os.ReadFile(d)
		if !strings.Contains(stripComments(string(src)), `\begin{document}`) || base == "main.tex" ||
			base == "Preamble.tex" || base == "Header.tex" || strings.HasPrefix(base, "Claims") {
			continue
		}
		x := &texExpander{cv: cv, macros: map[string]string{}, unknown: unknown}
		expanded := x.eval(stripComments(string(src)))
		if k := strings.Index(expanded, `\begin{document}`); k >= 0 {
			expanded = expanded[k:]
		}
		secs := scanStructure(expanded, unknown)
		stem := strings.TrimSuffix(base, ".tex")
		docID := slug(stem, 60)
		label := texLabel(stem)
		var tl []string
		for _, ln := range strings.Split(string(src), "\n") {
			if !strings.HasPrefix(ln, "%") {
				break
			}
			if t := strings.TrimSpace(strings.TrimLeft(ln, "% ")); t != "" {
				tl = append(tl, t)
			}
		}
		target := strings.Join(firstN(tl, 2), " ")
		pages := 2
		if stem == "Curriculum_Vitae" {
			// The full CV's last PDF, whatever name prefix it was built with.
			cvPDF := ""
			if m, _ := filepath.Glob(filepath.Join(cv, "output", "pdf", "*Master_CV.pdf")); len(m) > 0 {
				sort.Strings(m)
				cvPDF = m[0]
			}
			pages = pdfPagesOr(cvPDF, 4)
		}
		sel := map[string]any{"bank": relPath(rd, bp), "header": "default",
			"meta": Meta{Target: target, Pages: pages, Paper: "letter", OutputBasename: label, Origin: base}}
		var ssecs []map[string]any
		for _, s := range secs {
			ss := map[string]any{"title": s.Title}
			switch {
			case len(s.Entries) > 0:
				ss["kind"] = "entries"
				var ents []any
				for _, e := range s.Entries {
					eid := slug(strings.Join(firstN(strings.Fields(plainText(e.Title)), 4), " "), 60)
					if e.Org != "" && e.Kind != "resumeSubheading" {
						eid += "-" + slug(strings.Join(firstN(strings.Fields(plainText(e.Org)), 2), " "), 30)
					}
					be := &BankEntry{Title: e.Title, Org: e.Org, Dates: e.Dates, Stack: e.Stack}
					if e.Kind == "resumeSubheading" {
						be.Layout = "stacked"
					}
					// same title but different header fields -> a variant entry
					base := eid
					for n := 2; ; n++ {
						old, ok := bank.Entries[eid]
						if !ok {
							bank.Entries[eid] = be
							break
						}
						if old.Title == be.Title && old.Org == be.Org && old.Dates == be.Dates && old.Stack == be.Stack && old.Layout == be.Layout {
							be = old
							break
						}
						eid = fmt.Sprintf("%s-v%d", base, n)
					}
					be.Origin = appendUniq(be.Origin, base)
					var ids []string
					prefix := slug(strings.Join(firstN(strings.Fields(plainText(e.Title)), 2), " "), 20)
					for _, t := range e.Items {
						ids = append(ids, bank.addBullet(t, prefix, base+" > "+s.Title+" > "+plainText(e.Title), byText))
					}
					for _, id := range ids {
						be.Bullets = appendUniq(be.Bullets, id)
					}
					ents = append(ents, map[string]any{"entry": eid, "bullets": nonNil(ids)})
				}
				ss["entries"] = ents
			case len(s.Rows) > 0:
				ss["kind"] = "lines"
				ss["lines"] = bank.addLines(slug(s.Title, 20)+"."+docID, s.Rows, lineSets)
			case regexp.MustCompile(`(?i)publication|certific`).MatchString(s.Title):
				ss["kind"] = "lines"
				ss["bulleted"] = true
				var rows []Line
				for _, t := range s.Items {
					rows = append(rows, Line{Text: t})
				}
				ss["lines"] = bank.addLines(slug(s.Title, 20)+"."+docID, rows, lineSets)
			default:
				ss["kind"] = "bullets"
				prefix := "sum"
				if !strings.EqualFold(s.Title, "summary") {
					prefix = slug(s.Title, 12)
				}
				var ids []string
				for _, t := range s.Items {
					ids = append(ids, bank.addBullet(t, prefix, base+" > "+s.Title, byText))
				}
				ss["bullets"] = nonNil(ids)
			}
			ssecs = append(ssecs, ss)
		}
		sel["sections"] = ssecs
		writeJSON(filepath.Join(rd, docID+".json"), sel)
		nDocs++
	}
	bank.save()
	fmt.Printf("imported %d documents: %d entries, %d distinct bullets, %d line sets -> %s and %s/*.json\n",
		nDocs, len(bank.Entries), len(bank.Bullets), len(bank.Lines), bp, rd)
	if len(unknown) > 0 {
		fmt.Printf("commands dropped (kept their text): %s\n", strings.Join(sortedKeys(unknown), ", "))
	}
}

func (b *Bank) addBullet(text, prefix, origin string, byText map[string]string) string {
	if id, ok := byText[text]; ok {
		b.Bullets[id].Origin = appendUniq(b.Bullets[id].Origin, origin)
		return id
	}
	base := prefix + "-" + bulletKey(text)
	id := base
	for n := 2; b.Bullets[id] != nil; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	b.Bullets[id] = &Bullet{ID: id, Text: text, Author: "import", Origin: []string{origin}}
	byText[text] = id
	return id
}

func (b *Bank) addLines(key string, rows []Line, seen map[string]string) string {
	j, _ := marshalNoEscape(rows)
	if k, ok := seen[string(j)]; ok {
		return k
	}
	b.Lines[key] = rows
	seen[string(j)] = key
	return key
}

func appendUniq(xs []string, x string) []string {
	if contains(xs, x) {
		return xs
	}
	return append(xs, x)
}

func firstN(xs []string, n int) []string {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func relPath(fromDir, to string) string {
	r, err := filepath.Rel(fromDir, to)
	if err != nil {
		return to
	}
	return r
}

func pdfPagesOr(pdf string, def int) int {
	out, err := runCmd(timeoutPoppler, runOpts{}, "pdfinfo", pdf)
	if err != nil {
		return def
	}
	if m := regexp.MustCompile(`Pages:\s+(\d+)`).FindSubmatch(out); m != nil {
		if n, err := strconv.Atoi(string(m[1])); err == nil {
			return n
		}
	}
	return def // pdfinfo gave no usable page count
}
