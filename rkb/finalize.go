package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// build: draft outputs (txt, ascii txt, html, pdf, docx, generated LaTeX) with
// unverified figures highlighted in the HTML/PDF. Nothing is gated.
//
// finalize: the single fail-closed release command. In order, any failure stops:
//   1. claims.json is for this exact resume, fresh, validator report fresh
//   2. ecosystem report fresh and every declared link VERIFIED/PARTIAL
//   3. lint --final: figures double-verified in THIS sentence, statements
//      checked, both readers passed the exact wording in both rounds
//   4. render --final, 5. fit --strict --final (full pages, no orphans)
//   6. ATS text layer, 7. Word export within the page count, 8. LaTeX
// It writes manifest.json recording every check and the hash of every input
// and output, so a resume on disk can always say how it was verified.

type Manifest struct {
	Resume     string            `json:"resume"`
	ResumeHash string            `json:"resume_hash"`
	Finalized  string            `json:"finalized"`
	Checks     map[string]string `json:"checks"`
	Inputs     map[string]string `json:"inputs_sha256"`
	Outputs    map[string]string `json:"outputs_sha256"`
	Figures    int               `json:"figures"`
	PageFill   []float64         `json:"page_fill"`
	FontPt     string            `json:"font_pt"`
	DocxFontPt float64           `json:"docx_font_pt"`
	DocxPages  int               `json:"docx_pages"`
	// statements the person attested because no artifact can exist (NDA, lab work)
	Attested map[string]string `json:"attested,omitempty"`
}

// outputsFor renders every format into outDir (and the LaTeX into texDir).
func outputsFor(c *Composed, cf *ClaimsFile, sel, outDir, texDir string, final, strict bool, pages int) (rendered, FitReport, string, float64, int, string) {
	r := renderResume(c, cf, final)
	if len(r.R.missing) > 0 {
		die("unregistered figure token(s): %s", strings.Join(sortedKeys(r.R.missing), ", "))
	}
	base := c.basename(sel)
	writeFile(filepath.Join(outDir, base+".txt"), r.Text)
	writeFile(filepath.Join(outDir, base+".ascii.txt"), toASCII(r.Text))
	h := filepath.Join(outDir, base+".html")
	writeFile(h, r.HTML)
	rep := fit(h, filepath.Join(outDir, base+".pdf"), pages, strict, final)
	fs := 10.0
	if v, ok := rep.Vars["--fs"]; ok {
		fs, _ = strconv.ParseFloat(strings.TrimSuffix(v, "pt"), 64)
	}
	docx := filepath.Join(outDir, base+".docx")
	dfs, dpages := exportDocx(c, cf, docx, pages, fs)
	tex := filepath.Join(texDir, base+".tex")
	writeFile(tex, buildLatex(c, sel))

	pdfPath := filepath.Join(outDir, base+".pdf")
	if !fileExists(pdfPath) {
		if pdflatex, err := exec.LookPath("pdflatex"); err == nil && pdflatex != "" {
			cv := inRoot(cfg.CVDir)
			absTex, _ := filepath.Abs(tex)
			outGen, err := os.MkdirTemp("", "rkb-pdflatex-")
			if err == nil {
				defer func() { _ = os.RemoveAll(outGen) }() // best-effort temp cleanup
				for i := 0; i < 2; i++ {
					cmd := exec.Command("pdflatex", "-interaction=nonstopmode", "-halt-on-error", "-output-directory="+outGen, absTex)
					cmd.Dir = cv // the generated LaTeX \inputs the CV sources relative to it
					_ = cmd.Run()
				}
				if b, err := os.ReadFile(filepath.Join(outGen, base+".pdf")); err == nil {
					writeFile(pdfPath, string(b))
					f, buls, overMax, _, probs := verifyPDF(pdfPath, c.paper(), expectedPages(c, sel))
					rep.PDF = pdfPath
					rep.PageFill = f
					rep.Pages = len(f)
					rep.Bullets = buls
					rep.OverLineLimit = overMax
					if len(probs) > 0 {
						rep.Status = 2
						rep.Advice = "LAYOUT ERROR: " + strings.Join(probs, "; ")
					} else {
						rep.Advice = fmt.Sprintf("OK: compiled %s via pdflatex (%d/%d page(s), all >=95%% fill, 0 bullets >3 lines)", filepath.Base(pdfPath), len(f), expectedPages(c, sel))
					}
				}
			}
		}
	}

	return r, rep, docx, dfs, dpages, tex
}

func cmdBuild(a *args) {
	sel := a.need(0, "resume")
	c := loadResume(sel)
	cf := loadClaimsFile(a.get("claims", "claims.json"))
	pages := a.intFlag("pages", max(c.Meta.Pages, 1), 1, 20)
	out := a.get("out-dir", filepath.Join(inRoot(cfg.HTMLOutDir), slug(c.basename(sel), 60), "draft"))
	r, rep, docx, dfs, dp, tex := outputsFor(c, cf, sel, out, inRoot(cfg.LatexOutDir), false, false, pages)
	printFit(rep)
	fmt.Printf("draft: %s.{txt,ascii.txt,html,pdf} + %s (%.1fpt, %d pages) + %s\n", filepath.Join(out, c.basename(sel)), filepath.Base(docx), dfs, dp, tex)
	if len(r.R.unverified) > 0 {
		fmt.Printf("%d figure(s) not double-verified yet (highlighted): %s\n", len(r.R.unverified), strings.Join(sortedKeys(r.R.unverified), ", "))
	}
	R := lint(c, cf, false, "")
	fmt.Printf("lint: %d error(s), %d warning(s) — run `rkb lint` for detail\n", len(R.Errors), len(R.Warns))
}

func cmdFinalize(a *args) {
	sel := a.need(0, "resume")
	c := loadResume(sel)
	cfPath := a.get("claims", "claims.json")
	cf := loadClaimsFile(cfPath)
	eco := a.get("eco", "")
	pages := a.intFlag("pages", max(c.Meta.Pages, 1), 1, 20)
	out := a.get("out-dir", filepath.Join(inRoot(cfg.HTMLOutDir), slug(c.basename(sel), 60)))
	m := Manifest{Resume: sel, ResumeHash: c.resumeHash(claimViewValues(cf)), Checks: map[string]string{},
		Inputs: map[string]string{}, Outputs: map[string]string{}}
	stop := func(step string, problems []string) {
		fmt.Printf("FINALIZE STOPPED at %s:\n", step)
		for _, p := range problems {
			fmt.Println("  - " + p)
		}
		os.Exit(1)
	}

	R := lint(c, cf, true, eco)
	if len(R.Errors) > 0 {
		stop("lint --final", R.Errors)
	}
	m.Checks["lint_final"] = fmt.Sprintf("pass (%d warnings)", len(R.Warns))
	m.Checks["claims_export"] = "for this resume, " + cf.Generated
	m.Checks["validator_report"] = cf.VMReport["mtime"]

	// Everything is rendered into a private staging folder and checked there.
	// Only when every gate has passed are the files copied (each atomically)
	// to the output folder, so a stopped finalize never leaves a half-updated
	// set of "final" files behind, and the previous good release stays intact.
	stage, err := os.MkdirTemp("", "rkb-finalize-")
	if err != nil {
		die("staging folder: %v", err)
	}
	defer func() { _ = os.RemoveAll(stage) }() // best-effort: the stage holds only copies
	stop = func(step string, problems []string) {
		fmt.Printf("FINALIZE STOPPED at %s (nothing in %s was changed):\n", step, out)
		for _, p := range problems {
			fmt.Println("  - " + p)
		}
		_ = os.RemoveAll(stage) // best-effort: the stage holds only copies
		os.Exit(1)
	}
	stageTex := filepath.Join(stage, "tex")
	r, rep, sdocx, dfs, dp, stex := outputsFor(c, cf, sel, stage, stageTex, true, true, pages)
	if probs := r.R.coverage(cf); len(probs) > 0 || len(r.R.unverified) > 0 {
		stop("figure coverage", append(probs, fmt.Sprintf("%d rendered figure(s) are not double-verified", len(r.R.unverified))))
	}
	if rep.Status != 0 {
		printFit(rep)
		stop("fit --strict", []string{rep.Advice})
	}
	m.Checks["fit"] = rep.Advice
	sbase := filepath.Join(stage, c.basename(sel))
	ratio, probs := atsCheck(sbase+".pdf", sbase+".txt")
	if len(probs) > 0 {
		stop("ATS text layer", probs)
	}
	m.Checks["ats"] = fmt.Sprintf("clean, similarity %.3f", ratio)
	if dp > pages {
		stop("Word export", []string{fmt.Sprintf("the .docx runs to %d pages even at %.1fpt; trim content", dp, dfs)})
	}
	if dp < 0 {
		m.Checks["docx"] = fmt.Sprintf("written at %.1fpt; page count not checked (no LibreOffice)", dfs)
	} else {
		m.Checks["docx"] = fmt.Sprintf("%d page(s) at %.1fpt", dp, dfs)
	}
	for id, sv := range cf.Statements {
		if sv.Attested != "" {
			if m.Attested == nil {
				m.Attested = map[string]string{}
			}
			m.Attested[id] = sv.Attested
		}
	}
	// The inputs must not have changed while rendering (another agent editing
	// the bank or the log mid-finalize would make the manifest lie).
	for _, p := range []string{sel, c.BankPath, cfPath, eco, inRoot(cfg.VerifyLog)} {
		if p != "" && fileExists(p) {
			m.Inputs[p] = sha256File(p)
		}
	}
	if c.BankSHA != "" && m.Inputs[c.BankPath] != "" && m.Inputs[c.BankPath] != c.BankSHA {
		stop("inputs", []string{c.BankPath + " changed while finalize was running; re-run it"})
	}
	base := filepath.Join(out, c.basename(sel))
	tex := filepath.Join(inRoot(cfg.LatexOutDir), filepath.Base(stex))
	docx := filepath.Join(out, filepath.Base(sdocx))
	publish := [][2]string{
		{sbase + ".pdf", base + ".pdf"}, {sbase + ".txt", base + ".txt"}, {sbase + ".ascii.txt", base + ".ascii.txt"},
		{sbase + ".html", base + ".html"}, {sdocx, docx}, {stex, tex},
	}
	for _, pp := range publish {
		if !fileExists(pp[0]) {
			stop("publish", []string{"expected output was not produced: " + filepath.Base(pp[0])})
		}
	}
	m.Finalized, m.Figures, m.PageFill, m.FontPt, m.DocxFontPt, m.DocxPages = nowISO(), len(r.R.used), rep.PageFill, rep.Vars["--fs"], dfs, dp
	for _, pp := range publish {
		b, err := os.ReadFile(pp[0])
		if err != nil {
			die("publish %s: %v", pp[0], err)
		}
		if err := writeAtomic(pp[1], b); err != nil {
			die("publish %s: %v (outputs before this one were already replaced; re-run finalize)", pp[1], err)
		}
		m.Outputs[pp[1]] = sha256Bytes(b)
	}
	rep.PDF, rep.HTML = base+".pdf", base+".html"
	writeJSON(filepath.Join(out, "manifest.json"), m)
	printFit(rep)
	fmt.Printf("FINAL: %s.{pdf,docx,txt,ascii.txt,html}, LaTeX %s, manifest.json — %d figures, all double-verified in context\n", base, tex, len(r.R.used))
}
