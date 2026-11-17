package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// docx: a Word file for application systems that parse Word better than PDF
// (Taleo especially). Plain OOXML written with the standard library: one
// column, real text, no tables or text boxes, contact details in the body,
// right-aligned dates on a tab stop, real Word bullets. Same content and
// figures as the PDF; page count is checked with LibreOffice when installed.

type run struct {
	Text         string
	Bold, Italic bool
	Link         string
}

// inlineRuns turns inline markup into Word runs; figures resolve through the
// claims file and print bold like the LaTeX \textbf{\clm..}.
func inlineRuns(s string, R *resolver, bold, italic bool, link string) []run {
	var out []run
	for len(s) > 0 {
		type hit struct {
			i, j int
			kind string
			m    []string
		}
		var best *hit
		try := func(re *regexp.Regexp, kind string) {
			if loc := re.FindStringSubmatchIndex(s); loc != nil && (best == nil || loc[0] < best.i) {
				m := make([]string, len(loc)/2)
				for k := range m {
					if loc[2*k] >= 0 {
						m[k] = s[loc[2*k]:loc[2*k+1]]
					}
				}
				best = &hit{loc[0], loc[1], kind, m}
			}
		}
		try(linkRe, "link")
		try(tokenRe, "token")
		try(boldRe, "bold")
		try(regexp.MustCompile(`(?:^|[^*\w])\*([^*]+?)\*(?:$|[^*\w])`), "italic")
		if best == nil {
			out = append(out, run{s, bold, italic, link})
			break
		}
		start, end := best.i, best.j
		if best.kind == "italic" { // keep the boundary characters outside the run
			inner := best.m[1]
			k := strings.Index(s[start:end], "*"+inner+"*")
			start, end = start+k, start+k+len(inner)+2
		}
		if start > 0 {
			out = append(out, run{s[:start], bold, italic, link})
		}
		switch best.kind {
		case "link":
			out = append(out, inlineRuns(best.m[1], R, bold, italic, best.m[2])...)
		case "token":
			v, _ := R.value(best.m[1])
			out = append(out, run{v, bold || R.bold, italic, link})
		case "bold":
			out = append(out, inlineRuns(best.m[1], R, true, italic, link)...)
		case "italic":
			out = append(out, inlineRuns(s[start+1:end-1], R, bold, true, link)...)
		}
		s = s[end:]
	}
	return out
}

func xmlEsc(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

type docxWriter struct {
	body      strings.Builder
	rels      []string
	fs        float64 // body font size, pt
	lh        float64
	textTwips int
	R         *resolver
}

func (d *docxWriter) hp(mult float64) int { return int(d.fs*mult*2 + 0.5) } // half-points

func (d *docxWriter) runXML(r run, size int, caps bool) string {
	var rp strings.Builder
	rp.WriteString(`<w:rPr><w:rFonts w:ascii="Times New Roman" w:hAnsi="Times New Roman" w:cs="Times New Roman"/>`)
	if r.Bold {
		rp.WriteString(`<w:b/>`)
	}
	if r.Italic {
		rp.WriteString(`<w:i/>`)
	}
	if caps {
		rp.WriteString(`<w:caps/>`)
	}
	fmt.Fprintf(&rp, `<w:sz w:val="%d"/><w:szCs w:val="%d"/></w:rPr>`, size, size)
	t := fmt.Sprintf(`<w:r>%s<w:t xml:space="preserve">%s</w:t></w:r>`, rp.String(), xmlEsc(r.Text))
	if r.Link != "" {
		id := fmt.Sprintf("rIdL%d", len(d.rels)+1)
		d.rels = append(d.rels, fmt.Sprintf(`<Relationship Id="%s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="%s" TargetMode="External"/>`, id, xmlEsc(r.Link)))
		return fmt.Sprintf(`<w:hyperlink r:id="%s">%s</w:hyperlink>`, id, t)
	}
	return t
}

type pOpts struct {
	align      string
	before     int // twips
	border     bool
	bullet     bool
	tabRight   bool
	hanging    int
	size       float64
	caps, bold bool
	italic     bool
	keepNext   bool
}

func (d *docxWriter) para(runs []run, o pOpts) {
	if o.size == 0 {
		o.size = 1
	}
	var pp strings.Builder
	pp.WriteString("<w:pPr>")
	if o.keepNext {
		pp.WriteString(`<w:keepNext/>`)
	}
	if o.bullet {
		pp.WriteString(`<w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr>`)
	}
	if o.border {
		pp.WriteString(`<w:pBdr><w:bottom w:val="single" w:sz="6" w:space="1" w:color="000000"/></w:pBdr>`)
	}
	if o.tabRight {
		fmt.Fprintf(&pp, `<w:tabs><w:tab w:val="right" w:pos="%d"/></w:tabs>`, d.textTwips)
	}
	fmt.Fprintf(&pp, `<w:spacing w:before="%d" w:after="0" w:line="%d" w:lineRule="auto"/>`, o.before, int(d.lh*240))
	if o.bullet {
		pp.WriteString(`<w:ind w:left="460" w:hanging="230"/>`)
	} else if o.hanging > 0 {
		fmt.Fprintf(&pp, `<w:ind w:left="%d" w:hanging="%d"/>`, o.hanging, o.hanging)
	}
	if o.align != "" {
		pp.WriteString(`<w:jc w:val="` + o.align + `"/>`)
	}
	pp.WriteString("</w:pPr>")
	d.body.WriteString("<w:p>" + pp.String())
	for _, r := range runs {
		r.Bold = r.Bold || o.bold
		r.Italic = r.Italic || o.italic
		if r.Text == "\t" {
			d.body.WriteString(`<w:r><w:tab/></w:r>`)
			continue
		}
		d.body.WriteString(d.runXML(r, d.hp(o.size), o.caps))
	}
	d.body.WriteString("</w:p>")
}

func buildDocx(c *Composed, cf *ClaimsFile, fs float64) []byte {
	paper := c.paper()
	wIn, hIn := paperIn(paper)
	m := cfg.Layout.Margin
	d := &docxWriter{fs: fs, lh: 1.12, R: newResolver(cf, true, c.boldFigures())}
	d.textTwips = int((wIn - m["left"] - m["right"]) * 1440)
	R := d.R
	ir := func(s string) []run { return inlineRuns(s, R, false, false, "") }

	d.para(ir(c.Header.Name), pOpts{align: "center", size: 2.1, bold: true, caps: true, border: true})
	if c.Header.Headline != "" {
		d.para(ir(c.Header.Headline), pOpts{align: "center"})
	}
	var contact []run
	for _, x := range c.Header.Contact {
		if x == "" {
			continue
		}
		if len(contact) > 0 {
			contact = append(contact, run{Text: "  |  "})
		}
		contact = append(contact, ir(x)...)
	}
	d.para(contact, pOpts{align: "center", size: 0.92, before: 40})

	secGap := int(7 * 20)
	for _, s := range c.Sections {
		d.para([]run{{Text: s.Title}}, pOpts{before: secGap, border: true, bold: true, caps: true, size: 0.95, keepNext: true})
		if s.Lead != "" {
			d.para(ir(s.Lead), pOpts{italic: true, keepNext: true})
		}
		switch s.Kind {
		case "bullets":
			for _, b := range s.Bullets {
				d.para(ir(b.Text), pOpts{bullet: true, align: "both"})
			}
		case "lines":
			for _, l := range s.Lines {
				if s.Bulleted {
					d.para(ir(l.Text), pOpts{bullet: true, align: "both"})
					continue
				}
				var rs []run
				if l.Label != "" {
					rs = append(rs, run{Text: plainText(R.text(l.Label)) + ": ", Bold: true})
				}
				d.para(append(rs, ir(l.Text)...), pOpts{hanging: 0})
			}
		default:
			for i, e := range s.Entries {
				stacked := e.Layout == "stacked"
				full := e.Title
				if len(e.Members) > 0 {
					full += ": " + strings.Join(e.Members, ", ")
				}
				rs := inlineRuns(full, R, true, false, "")
				if e.Org != "" && !stacked {
					rs = append(rs, run{Text: ", "})
					rs = append(rs, ir(e.Org)...)
				}
				right, rItalic := e.Dates, false
				if right == "" {
					right, rItalic = e.Stack, true
				}
				if right != "" {
					rs = append(rs, run{Text: "\t"})
					for _, r := range ir(right) {
						r.Bold, r.Italic = !rItalic, rItalic
						rs = append(rs, r)
					}
				}
				before := 60
				if i == 0 {
					before = 20
				}
				d.para(rs, pOpts{tabRight: true, before: before, keepNext: true})
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
					d.para(ir(strings.Join(sub, ", ")), pOpts{keepNext: len(e.Bullets) > 0})
				}
				if e.Lead != "" {
					d.para(ir(e.Lead), pOpts{italic: true, keepNext: true})
				}
				for _, b := range e.Bullets {
					d.para(ir(b.Text), pOpts{bullet: true, align: "both"})
				}
			}
		}
	}

	pw, ph := int(wIn*1440), int(hIn*1440)
	sect := fmt.Sprintf(`<w:sectPr><w:pgSz w:w="%d" w:h="%d"/><w:pgMar w:top="%d" w:right="%d" w:bottom="%d" w:left="%d" w:header="0" w:footer="0" w:gutter="0"/></w:sectPr>`,
		pw, ph, int(m["top"]*1440), int(m["right"]*1440), int(m["bottom"]*1440), int(m["left"]*1440))
	doc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body>` +
		d.body.String() + sect + `</w:body></w:document>`
	styles := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Times New Roman" w:hAnsi="Times New Roman" w:cs="Times New Roman" w:eastAsia="Times New Roman"/><w:sz w:val="%d"/><w:szCs w:val="%d"/><w:lang w:val="en-US"/></w:rPr></w:rPrDefault><w:pPrDefault><w:pPr><w:spacing w:after="0" w:line="240" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults><w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/><w:qFormat/></w:style></w:styles>`, d.hp(1), d.hp(1))
	numbering := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:abstractNum w:abstractNumId="0"><w:multiLevelType w:val="singleLevel"/><w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="bullet"/><w:lvlText w:val="•"/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="460" w:hanging="230"/></w:pPr><w:rPr><w:rFonts w:ascii="Times New Roman" w:hAnsi="Times New Roman"/><w:sz w:val="14"/></w:rPr></w:lvl></w:abstractNum><w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num></w:numbering>`
	rels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rIdS" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/><Relationship Id="rIdN" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/>` +
		strings.Join(d.rels, "") + `</Relationships>`
	ct := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/><Override PartName="/word/numbering.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.numbering+xml"/><Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/></Types>`
	root := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/></Relationships>`
	core := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>%s</dc:title><dc:creator>%s</dc:creator></cp:coreProperties>`, xmlEsc(c.Header.Name+" — Resume"), xmlEsc(c.Header.Name))

	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for _, f := range [][2]string{{"[Content_Types].xml", ct}, {"_rels/.rels", root}, {"word/document.xml", doc},
		{"word/styles.xml", styles}, {"word/numbering.xml", numbering}, {"word/_rels/document.xml.rels", rels},
		{"docProps/core.xml", core}} {
		w, err := z.Create(f[0])
		if err != nil {
			die("docx: %s: %v", f[0], err)
		}
		if _, err := w.Write([]byte(f[1])); err != nil {
			die("docx: %s: %v", f[0], err)
		}
	}
	if err := z.Close(); err != nil {
		die("docx: %v", err)
	}
	return buf.Bytes()
}

// docxPages converts with LibreOffice (if present) and counts pages.
func docxPages(docx string) int {
	so := ""
	for _, n := range []string{"soffice", "libreoffice", "/Applications/LibreOffice.app/Contents/MacOS/soffice"} {
		if p, err := exec.LookPath(n); err == nil {
			so = p
			break
		}
	}
	if so == "" {
		return -1
	}
	tmp, err := os.MkdirTemp("", "rkb-docx-")
	if err != nil {
		return -1
	}
	defer func() { _ = os.RemoveAll(tmp) }() // best-effort temp cleanup
	if _, err := runCmd(timeoutOffice, runOpts{Env: append(os.Environ(), "HOME="+tmp)}, so, "--headless", "--convert-to", "pdf", "--outdir", tmp, docx); err != nil {
		return -1
	}
	pdf := filepath.Join(tmp, strings.TrimSuffix(filepath.Base(docx), ".docx")+".pdf")
	out, err := runCmd(timeoutPoppler, runOpts{}, "pdfinfo", pdf)
	if err != nil {
		return -1
	}
	if m := regexp.MustCompile(`Pages:\s+(\d+)`).FindSubmatch(out); m != nil {
		n, _ := strconv.Atoi(string(m[1]))
		return n
	}
	return -1
}

// exportDocx writes the .docx, stepping the font down (to 9.5pt) if Word
// layout runs past the target page count. It does not gate on figure
// verification: `rkb docx` does (cmdDocx), finalize through lint --final.
func exportDocx(c *Composed, cf *ClaimsFile, out string, pages int, fs float64) (float64, int) {
	if fs == 0 {
		fs = 10
	}
	n := -1
	for ; fs >= 9.5; fs -= 0.1 {
		writeFile(out, string(buildDocx(c, cf, fs)))
		n = docxPages(out)
		if n < 0 || n <= pages {
			break
		}
	}
	return fs, n
}

func cmdDocx(a *args) {
	sel := a.need(0, "resume")
	c := loadResume(sel)
	cf := loadClaimsFile(a.get("claims", "claims.json"))
	pages := a.intFlag("pages", max(c.Meta.Pages, 1), 1, 20)
	fs := a.floatFlag("font-pt", 10, 6, 16)
	out := a.get("out", filepath.Join(a.get("out-dir", "."), c.basename(sel)+".docx"))
	// The Word file prints figures bold with no unverified marker, so a
	// standalone export is refused while any rendered figure is unverified,
	// by the same check as render --final.
	if probs := renderResume(c, cf, true).R.coverage(cf); len(probs) > 0 {
		for _, p := range probs {
			fmt.Println("ERROR " + p)
		}
		die("docx blocked: %d rendered figure(s) are not double-verified in the text they appear in (use `rkb build` for a draft)", len(probs))
	}
	fs, n := exportDocx(c, cf, out, pages, fs)
	msg := fmt.Sprintf("wrote %s (Times New Roman %.1fpt", out, fs)
	if n >= 0 {
		msg += fmt.Sprintf(", %d page(s) in LibreOffice", n)
	} else {
		msg += ", page count not checked: LibreOffice not found"
	}
	fmt.Println(msg + ")")
}
