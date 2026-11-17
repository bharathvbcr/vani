package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// ats: an ATS reads the PDF's text layer, not the picture. Check it reads like
// the exported .txt: reading order (word-sequence similarity >= 0.97), headings
// present in order, name and email intact, no ligature / replacement /
// private-use glyphs, nothing in the header/footer band.

var reWordTok = regexp.MustCompile(`[A-Za-z0-9%×→.@+\-/]+`)

func atsWords(s string) []string {
	s = strings.NewReplacer(" ", " ", " ", " ").Replace(s)
	return reWordTok.FindAllString(strings.ToLower(s), -1)
}

// similarity is difflib-style: 2*matches/(len(a)+len(b)) using an LCS of words.
func similarity(a, b []string) float64 {
	if len(a)+len(b) == 0 {
		return 1
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
			} else if prev[j] >= cur[j-1] {
				cur[j] = prev[j]
			} else {
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
	}
	return 2 * float64(prev[len(b)]) / float64(len(a)+len(b))
}

func atsCheck(pdf, txt string) (float64, []string) {
	gotB, err := runCmd(timeoutPoppler, runOpts{}, "pdftotext", "-enc", "UTF-8", pdf, "-")
	if err != nil {
		die("pdftotext (poppler) is needed: %v", err)
	}
	got := string(gotB)
	wantB, err := os.ReadFile(txt)
	if err != nil {
		die("%v", err)
	}
	want := string(wantB)
	// the .txt spells out link targets; in the PDF they are annotations
	wantCmp := regexp.MustCompile(`\s*\(https?://[^)]*\)`).ReplaceAllString(want, "")
	var problems []string
	ratio := similarity(atsWords(wantCmp), atsWords(got))
	if ratio < 0.97 {
		problems = append(problems, fmt.Sprintf("reading order: PDF text matches the .txt at %.3f (< 0.97)", ratio))
	}
	lines := strings.Split(want, "\n")
	upGot := strings.ToUpper(got)
	pos := -1
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if i == 0 || t == "" || t != strings.ToUpper(t) || !regexp.MustCompile(`[A-Z]{3}`).MatchString(t) {
			continue
		}
		k := strings.Index(upGot[pos+1:], t)
		if k < 0 {
			problems = append(problems, "heading missing or out of order in PDF text: "+t)
		} else {
			pos += 1 + k
		}
	}
	first := strings.TrimSpace(lines[0])
	if !strings.Contains(strings.ToLower(got), strings.ToLower(first)) {
		problems = append(problems, "name not found in PDF text: "+first)
	}
	for _, em := range regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.]+`).FindAllString(want, -1) {
		if !strings.Contains(got, em) {
			problems = append(problems, "email lost in extraction: "+em)
		}
	}
	bad := 0
	for _, r := range got {
		if (r >= 0xFB00 && r <= 0xFB06) || r == 0xFFFD || (r >= 0xE000 && r <= 0xF8FF) {
			bad++
		}
	}
	if bad > 0 {
		problems = append(problems, fmt.Sprintf("%d ligature/replacement/private-use glyph(s) in the text layer", bad))
	}
	if words, pages, err := pdfWords(pdf); err == nil {
		band := 0.3 * ptPerIn
		flagged := map[int]bool{}
		for _, w := range words {
			h := pages[w.Page][1]
			if !flagged[w.Page] && strings.TrimSpace(w.Text) != "" && (w.YMin < band || w.YMax > h-band) {
				flagged[w.Page] = true
				problems = append(problems, fmt.Sprintf("text in the header/footer band on page %d: %q", w.Page+1, w.Text))
			}
		}
	}
	return ratio, problems
}

func cmdATS(a *args) {
	ratio, problems := atsCheck(a.need(0, "pdf"), a.need(1, "txt"))
	fmt.Printf("PDF text vs .txt similarity: %.3f\n", ratio)
	if len(problems) > 0 {
		fmt.Println("PROBLEMS:")
		for _, p := range problems {
			fmt.Println("  - " + p)
		}
		os.Exit(1)
	}
	fmt.Println("ATS text layer: clean")
}
