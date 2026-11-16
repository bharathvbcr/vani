package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// fit: fill the target number of pages to the bottom, then export the PDF.
//
// One density knob t in [0,1] drives every spacing variable the template
// exposes (ranges in config). Page count only grows with t, so a binary search
// finds the loosest setting that still fits; each page is then measured from
// the PDF itself (lowest word, via pdftotext -bbox). Chrome renders; poppler
// measures; nothing else is needed.
//
// Exit codes — the fix is content, not CSS:
//   0 fits, pages full    2 OVERFLOW (cut ~N lines)    3 SHORT (add ~N lines)
//   4 GAP (an inner page ends early)    5 STRICT (orphan last line / too long)

const ptPerIn = 72.0

func paperIn(p string) (float64, float64) {
	if p == "a4" {
		return 8.27, 11.69
	}
	return 8.5, 11
}

func hasChrome() bool {
	if v := os.Getenv("RKB_CHROME"); v != "" {
		return fileExists(v)
	}
	for _, p := range cfg.ChromePaths {
		if fileExists(p) {
			return true
		}
	}
	for _, n := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if _, err := exec.LookPath(n); err == nil {
			return true
		}
	}
	return false
}

func findChrome() string {
	p, err := chromePath()
	if err != nil {
		die("%v", err)
	}
	return p
}

func chromePath() (string, error) {
	if v := os.Getenv("RKB_CHROME"); v != "" {
		if !fileExists(v) {
			return "", fmt.Errorf("RKB_CHROME=%s does not exist", v)
		}
		return v, nil
	}
	for _, p := range cfg.ChromePaths {
		if fileExists(p) {
			return p, nil
		}
	}
	for _, n := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(n); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no Chrome/Chromium found; install Google Chrome or set RKB_CHROME to its binary")
}

func chromeArgs(extra ...string) []string {
	a := []string{"--headless=new", "--disable-gpu", "--hide-scrollbars", "--no-first-run",
		"--no-default-browser-check", "--disable-extensions", "--run-all-compositor-stages-before-draw",
		"--virtual-time-budget=4000", "--disable-background-networking", "--disable-component-update",
		"--disable-sync", "--disable-default-apps", "--no-pings", "--metrics-recording-only", "--disable-domain-reliability"}
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		a = append(a, "--no-sandbox")
	}
	return append(a, extra...)
}

func runChrome(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	prof, err := os.MkdirTemp("", "rkb-chrome-")
	if err != nil {
		return nil, fmt.Errorf("chrome profile dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(prof) }() // best-effort temp cleanup
	cmd := exec.CommandContext(ctx, findChrome(), chromeArgs(append([]string{"--user-data-dir=" + prof}, args...)...)...)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = nil
	err = cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("chrome did not finish within 90 s")
	}
	return []byte(out.String()), err
}

func printPDF(htmlPath, pdfPath string) error {
	_ = os.Remove(pdfPath)
	abs, _ := filepath.Abs(htmlPath)
	if _, err := runChrome("--no-pdf-header-footer", "--print-to-pdf="+pdfPath, "file://"+abs); err != nil && !fileExists(pdfPath) {
		return fmt.Errorf("chrome print failed: %v", err)
	}
	if !fileExists(pdfPath) {
		return fmt.Errorf("chrome produced no PDF")
	}
	return nil
}

var (
	rePage = regexp.MustCompile(`<page width="([\d.]+)" height="([\d.]+)">`)
	reWord = regexp.MustCompile(`<word xMin="([\d.]+)" yMin="([\d.]+)" xMax="([\d.]+)" yMax="([\d.]+)">([^<]*)</word>`)
)

type pdfWord struct {
	Page                   int
	XMin, YMin, XMax, YMax float64
	Text                   string
}

// pdfWords runs pdftotext -bbox and returns every word with its box.
func pdfWords(pdf string) ([]pdfWord, [][2]float64, error) {
	out, err := exec.Command("pdftotext", "-bbox", pdf, "-").Output()
	if err != nil {
		return nil, nil, fmt.Errorf("pdftotext (poppler) is needed: %v", err)
	}
	var words []pdfWord
	var pages [][2]float64
	s := string(out)
	locs := rePage.FindAllStringSubmatchIndex(s, -1)
	for i, loc := range locs {
		w, _ := strconv.ParseFloat(s[loc[2]:loc[3]], 64)
		h, _ := strconv.ParseFloat(s[loc[4]:loc[5]], 64)
		pages = append(pages, [2]float64{w, h})
		end := len(s)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		for _, m := range reWord.FindAllStringSubmatch(s[loc[1]:end], -1) {
			f := func(x string) float64 { v, _ := strconv.ParseFloat(x, 64); return v }
			words = append(words, pdfWord{i, f(m[1]), f(m[2]), f(m[3]), f(m[4]), html.UnescapeString(m[5])})
		}
	}
	return words, pages, nil
}

func pageFills(pdf, paper string) ([]float64, float64, error) {
	words, pages, err := pdfWords(pdf)
	if err != nil {
		return nil, 0, err
	}
	m := cfg.Layout.Margin
	_, hIn := paperIn(paper)
	top := m["top"] * ptPerIn
	printable := (hIn - m["top"] - m["bottom"]) * ptPerIn
	bottom := make([]float64, len(pages))
	for i := range bottom {
		bottom[i] = top
	}
	for _, w := range words {
		if strings.TrimSpace(w.Text) != "" && w.YMax > bottom[w.Page] {
			bottom[w.Page] = w.YMax
		}
	}
	fills := make([]float64, len(pages))
	for i, b := range bottom {
		fills[i] = math.Max(0, math.Min(1, (b-top)/printable))
	}
	return fills, printable, nil
}

func densityVars(t float64) map[string]string {
	out := map[string]string{}
	for k, r := range cfg.Layout.DensityRanges {
		lo, hi := r[0].(float64), r[1].(float64)
		unit, _ := r[2].(string)
		out[k] = fmt.Sprintf("%.3f%s", lo+(hi-lo)*t, unit)
	}
	return out
}

var reFitStyle = regexp.MustCompile(`(?s)<style id="fit">.*?</style>\n?`)

func withDensity(htmlSrc string, vars map[string]string) string {
	var b strings.Builder
	b.WriteString(`<style id="fit">/* set by rkb fit */ :root {`)
	for _, k := range sortedKeys(vars) {
		b.WriteString(" " + k + ": " + vars[k] + ";")
	}
	b.WriteString(" }</style>\n</head>")
	clean := reFitStyle.ReplaceAllString(htmlSrc, "")
	return strings.Replace(clean, "</head>", b.String(), 1)
}

type bulletMetric struct {
	ID           string  `json:"id"`
	Text         string  `json:"text"`
	Lines        int     `json:"lines"`
	LastLineFill float64 `json:"last_line_fill"`
}

var reMeasure = regexp.MustCompile(`(?s)<pre id="rkb-measure">(.*?)</pre>`)

func measureBullets(htmlPath string) []bulletMetric {
	abs, _ := filepath.Abs(htmlPath)
	out, _ := runChrome("--window-size=1100,1600", "--dump-dom", "file://"+abs+"#measure")
	m := reMeasure.FindSubmatch(out)
	if m == nil {
		return nil
	}
	var bm []bulletMetric
	_ = json.Unmarshal([]byte(html.UnescapeString(string(m[1]))), &bm)
	return bm
}

type FitReport struct {
	Status        int               `json:"status"`
	Advice        string            `json:"advice"`
	Density       float64           `json:"density_t"`
	Vars          map[string]string `json:"vars"`
	Pages         int               `json:"pages"`
	PageFill      []float64         `json:"page_fill"`
	Orphans       []bulletMetric    `json:"orphan_last_lines"`
	OverLineLimit []bulletMetric    `json:"over_line_limit"`
	Bullets       []bulletMetric    `json:"bullets"`
	PDF, HTML     string
}

func fit(htmlPath, pdfOut string, pages int, strict, final bool) FitReport {
	src, err := os.ReadFile(htmlPath)
	if err != nil {
		die("%v", err)
	}
	if !hasChrome() {
		if strict || final {
			die("no Chrome/Chromium found; install Google Chrome or set RKB_CHROME to its binary")
		}
		fmt.Println("fit: Chrome/Chromium not found; PDF export skipped (txt, html, docx, tex preserved)")
		return FitReport{Status: 0, Advice: "PDF skipped (no Chrome/Chromium)"}
	}
	page := string(src)
	if final && strings.Contains(page, `class="unverified"`) {
		die("refusing to make a final PDF from a draft HTML (it still marks unverified figures); render with --final first")
	}
	paper := "letter"
	if m := regexp.MustCompile(`<meta name="rkb-paper" content="(\w+)">`).FindStringSubmatch(page); m != nil {
		paper = strings.ToLower(m[1])
	}
	if pdfOut == "" {
		pdfOut = strings.TrimSuffix(htmlPath, filepath.Ext(htmlPath)) + ".pdf"
	}
	if err := os.MkdirAll(filepath.Dir(pdfOut), 0o755); err != nil {
		die("%v", err)
	}
	work, err := os.MkdirTemp("", "rkb-fit-")
	if err != nil {
		die("fit work dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(work) }() // best-effort temp cleanup
	minLast, minInner := cfg.Layout.MinLastFill, cfg.Layout.MinInnerFill

	type res struct {
		fills     []float64
		printable float64
		pdf       string
	}
	cache := map[string]res{}
	render := func(t float64) res {
		key := fmt.Sprintf("%.4f", t)
		if r, ok := cache[key]; ok {
			return r
		}
		h := filepath.Join(work, "p"+key+".html")
		p := filepath.Join(work, "p"+key+".pdf")
		writeFile(h, withDensity(page, densityVars(t)))
		if err := printPDF(h, p); err != nil {
			die("%v", err)
		}
		f, pr, err := pageFills(p, paper)
		if err != nil {
			die("%v", err)
		}
		cache[key] = res{f, pr, p}
		return cache[key]
	}
	lineAt := func(t float64) float64 {
		v := densityVars(t)
		fs, _ := strconv.ParseFloat(strings.TrimSuffix(v["--fs"], "pt"), 64)
		lh, _ := strconv.ParseFloat(v["--lh"], 64)
		return fs * lh
	}

	rep := FitReport{}
	r0, r1 := render(0), render(1)
	tBest := 0.0
	switch {
	case len(r0.fills) > pages:
		over := (float64(len(r0.fills)-pages-1) + r0.fills[len(r0.fills)-1]) * r0.printable
		rep.Status = 2
		rep.Advice = fmt.Sprintf("OVERFLOW: the tightest setting needs %d pages; cut about %d line(s) — the weakest bullet for this posting first.", len(r0.fills), int(math.Max(1, math.Round(over/lineAt(0)))))
	case len(r1.fills) < pages || (len(r1.fills) == pages && r1.fills[pages-1] < minLast):
		short := float64(pages-len(r1.fills))*r1.printable + (1-r1.fills[len(r1.fills)-1])*r1.printable
		rep.Status = 3
		tBest = 1
		rep.Advice = fmt.Sprintf("SHORT: the loosest setting fills %d page(s), last page %.0f%%; add about %d line(s) of real content (the next-best evidenced bullet from the bank).", len(r1.fills), 100*r1.fills[len(r1.fills)-1], int(math.Max(1, math.Round(short/lineAt(1)))))
	default:
		lo, hi := 0.0, 1.0
		for i := 0; i < 10; i++ {
			mid := (lo + hi) / 2
			if len(render(mid).fills) <= pages {
				lo = mid
			} else {
				hi = mid
			}
		}
		tBest = lo
		r := render(tBest)
		last := r.fills[len(r.fills)-1]
		switch {
		case len(r.fills) < pages:
			rep.Status, rep.Advice = 3, fmt.Sprintf("SHORT: content ends on page %d of %d; add content.", len(r.fills), pages)
		case last < minLast:
			rep.Status = 3
			rep.Advice = fmt.Sprintf("SHORT: last page %.0f%% full at the loosest setting that fits; add about %d line(s).", 100*last, int(math.Max(1, math.Round((1-last)*r.printable/lineAt(tBest)))))
		default:
			rep.Advice = fmt.Sprintf("OK: %d page(s), last page %.0f%% full.", len(r.fills), 100*last)
		}
	}
	best := render(tBest)
	if rep.Status == 0 {
		var gaps []string
		for i, f := range best.fills[:len(best.fills)-1] {
			if f < minInner {
				gaps = append(gaps, fmt.Sprintf("p%d %.0f%%", i+1, 100*f))
			}
		}
		if len(gaps) > 0 {
			rep.Status = 4
			rep.Advice = "GAP: inner page(s) end early (" + strings.Join(gaps, ", ") + "). An unsplittable block moved down: shorten the bullet that opens the next page, or move a shorter bullet up."
		}
	}
	vars := densityVars(tBest)
	finalHTML := strings.TrimSuffix(pdfOut, filepath.Ext(pdfOut)) + ".html"
	writeFile(finalHTML, withDensity(page, vars))
	b, err := os.ReadFile(best.pdf)
	if err != nil || len(b) == 0 {
		die("fit: the best PDF candidate is missing or empty (%v)", err)
	}
	if err := writeAtomic(pdfOut, b); err != nil {
		die("%v", err)
	}
	rep.Bullets = measureBullets(finalHTML)
	for _, m := range rep.Bullets {
		if m.Lines > 1 && m.LastLineFill < cfg.Rules.OrphanRatio {
			rep.Orphans = append(rep.Orphans, m)
		}
		if m.Lines > cfg.Rules.MaxLines {
			rep.OverLineLimit = append(rep.OverLineLimit, m)
		}
	}
	if rep.Bullets == nil {
		rep.Advice += " (bullet line metrics unavailable: Chrome --dump-dom returned nothing)"
	}
	if rep.Status == 0 && strict && (len(rep.Orphans) > 0 || len(rep.OverLineLimit) > 0) {
		rep.Status = 5
		rep.Advice += fmt.Sprintf(" STRICT: %d orphan last line(s), %d over-long bullet(s); rewrite and re-run.", len(rep.Orphans), len(rep.OverLineLimit))
	}
	rep.Density, rep.Vars, rep.Pages, rep.PageFill, rep.PDF, rep.HTML = tBest, vars, len(best.fills), best.fills, pdfOut, finalHTML
	return rep
}

func printFit(rep FitReport) {
	fmt.Println(rep.Advice)
	var vs []string
	for _, k := range sortedKeys(rep.Vars) {
		vs = append(vs, k+"="+rep.Vars[k])
	}
	fmt.Printf("density t=%.3f  %s\n", rep.Density, strings.Join(vs, "  "))
	var pf []string
	for i, f := range rep.PageFill {
		pf = append(pf, fmt.Sprintf("p%d %.0f%%", i+1, 100*f))
	}
	fmt.Println("page fill: " + strings.Join(pf, ", "))
	for _, b := range rep.OverLineLimit {
		fmt.Printf("  LONG   %-16s %d lines  %s\n", b.ID, b.Lines, b.Text)
	}
	for _, b := range rep.Orphans {
		fmt.Printf("  ORPHAN %-16s last line %.0f%%  %s\n", b.ID, 100*b.LastLineFill, b.Text)
	}
	fmt.Printf("wrote %s and %s\n", rep.PDF, rep.HTML)
}

func cmdFit(a *args) {
	h := a.need(0, "resume html")
	pages := a.intFlag("pages", 2, 1, 20)
	rep := fit(h, a.get("out", ""), pages, a.bools["strict"], a.bools["final"])
	if r := a.get("report", ""); r != "" {
		writeJSON(r, rep)
	}
	printFit(rep)
	os.Exit(rep.Status)
}

func isMasterCV(c *Composed, sel string) bool {
	s := strings.ToLower(filepath.Base(sel) + " " + c.basename(sel) + " " + c.Meta.Origin)
	return strings.Contains(s, "curriculum-vitae") || strings.Contains(s, "curriculum_vitae") || strings.Contains(s, "master_cv") || strings.Contains(s, "master-cv")
}

func expectedPages(c *Composed, sel string) int {
	if isMasterCV(c, sel) {
		return 4
	}
	return 2
}

// pdfBulletMetrics extracts bullet line counts and last-line fill directly from a PDF via pdftotext -bbox.
func pdfBulletMetrics(pdf string) []bulletMetric {
	words, pages, err := pdfWords(pdf)
	if err != nil || len(pages) == 0 {
		return nil
	}
	type lineBox struct {
		page                   int
		yMin, yMax, xMin, xMax float64
		words                  []string
	}
	var lines []lineBox
	for _, w := range words {
		if strings.TrimSpace(w.Text) == "" {
			continue
		}
		if len(lines) == 0 || lines[len(lines)-1].page != w.Page || math.Abs(w.YMin-lines[len(lines)-1].yMin) > 3.0 {
			lines = append(lines, lineBox{w.Page, w.YMin, w.YMax, w.XMin, w.XMax, []string{w.Text}})
		} else {
			l := &lines[len(lines)-1]
			l.words = append(l.words, w.Text)
			if w.XMin < l.xMin {
				l.xMin = w.XMin
			}
			if w.XMax > l.xMax {
				l.xMax = w.XMax
			}
			if w.YMax > l.yMax {
				l.yMax = w.YMax
			}
		}
	}
	var out []bulletMetric
	type curB struct {
		page  int
		lines []lineBox
	}
	var cur *curB
	flush := func() {
		if cur == nil || len(cur.lines) == 0 {
			cur = nil
			return
		}
		var txts []string
		for _, l := range cur.lines {
			txts = append(txts, strings.Join(l.words, " "))
		}
		fullTxt := strings.TrimSpace(strings.TrimPrefix(strings.Join(txts, " "), "•"))
		last := cur.lines[len(cur.lines)-1]
		pw := pages[cur.page][0]
		avail := pw - 61.0 - 36.0
		if avail <= 0 {
			avail = 500.0
		}
		fill := math.Max(0, math.Min(1, (last.xMax-last.xMin)/avail))
		out = append(out, bulletMetric{
			ID:           fmt.Sprintf("p%d-b%d", cur.page+1, len(out)+1),
			Text:         truncate(fullTxt, 90),
			Lines:        len(cur.lines),
			LastLineFill: fill,
		})
		cur = nil
	}
	for _, ln := range lines {
		if len(ln.words) > 0 && ln.words[0] == "•" {
			flush()
			cur = &curB{page: ln.page, lines: []lineBox{ln}}
		} else if cur != nil && ln.xMin >= 50.0 && ln.xMin <= 75.0 {
			cur.lines = append(cur.lines, ln)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func verifyPDF(pdfPath, paper string, targetPages int) (fills []float64, bullets []bulletMetric, overMax []bulletMetric, threeLine []bulletMetric, probs []string) {
	fills, _, err := pageFills(pdfPath, paper)
	if err != nil {
		return nil, nil, nil, nil, []string{err.Error()}
	}
	if len(fills) != targetPages {
		probs = append(probs, fmt.Sprintf("page count is %d, expected %d (master CV=4, resumes=2)", len(fills), targetPages))
	}
	minInner := cfg.Layout.MinInnerFill
	if minInner <= 0 {
		minInner = 0.95
	}
	minLast := cfg.Layout.MinLastFill
	if minLast <= 0 {
		minLast = 0.95
	}
	for i, f := range fills {
		req := minInner
		if i == len(fills)-1 {
			req = minLast
		}
		if f+1e-4 < req {
			probs = append(probs, fmt.Sprintf("page %d fill is %.1f%% (minimum %.0f%% required)", i+1, 100*f, 100*req))
		}
	}
	bullets = pdfBulletMetrics(pdfPath)
	maxL := cfg.Rules.MaxLines
	if maxL <= 0 {
		maxL = 3
	}
	prefL := cfg.Rules.PreferredLines
	if prefL <= 0 {
		prefL = 2
	}
	for _, b := range bullets {
		if b.Lines > maxL {
			overMax = append(overMax, b)
			probs = append(probs, fmt.Sprintf("%s spans %d lines (max %d allowed): %s", b.ID, b.Lines, maxL, b.Text))
		} else if b.Lines > prefL {
			threeLine = append(threeLine, b)
		}
	}
	return fills, bullets, overMax, threeLine, probs
}
