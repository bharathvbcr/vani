package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Golden tests for the final gates: a figure token printed in any rendered
// field (skills line, entry title, org, dates, stack, header) must be covered
// by the same double-verification as one in a bullet. Synthetic data only.

// w2aTokenFields: claim id -> where the synthetic resume prints it.
var w2aTokenFields = map[string]string{
	"clmLine":  "Skills line 1",
	"clmStack": "entry e1 sub",
	"clmHead":  "header headline",
	"clmOrg":   "entry e1 org",
	"clmDates": "entry e1 dates",
	"clmTitle": "entry e1 title",
}

func w2aResume() *Composed {
	return &Composed{
		Composed: true,
		Header:   Header{Name: "Test Person", Headline: "Cut cost by {{clmHead}}", Contact: []string{"a@b.c"}},
		Sections: []*Section{
			{Title: "Skills", Kind: "lines", Lines: []Line{{Label: "Tools", Text: "Go, {{clmLine}} jobs"}}},
			{Title: "Projects", Kind: "entries", Entries: []*Entry{{
				ID: "e1", Title: "Engine {{clmTitle}}", Org: "Acme {{clmOrg}}", Dates: "{{clmDates}} months", Stack: "{{clmStack}} nodes",
				Bullets: []*Bullet{{ID: "b0", Text: "**Faster:** cut latency by {{clmA}}"}},
			}}},
		},
	}
}

// w2aClaims: every claim is double-verified and the only confirmed sentence is
// the bullet's, which is what the pre-fix export could ever produce.
func w2aClaims(c *Composed) *ClaimsFile {
	cf := &ClaimsFile{Generated: nowISO(), ResumeHash: c.resumeHash(), VMReport: map[string]string{"mtime": nowISO()},
		Claims: map[string]*ClaimView{}, Statements: map[string]*StatementView{}, Guards: 1}
	for _, id := range append(sortedKeys(w2aTokenFields), "clmA") {
		cf.Claims[id] = &ClaimView{Value: "40%", Kind: "pct", DoubleVerified: true}
	}
	cf.Claims["clmA"].ConfirmedSentences = []string{sentenceHash("**Faster:** cut latency by {{clmA}}", "clmA")}
	return cf
}

func w2aJoin(xs []string) string { return strings.Join(xs, "\n") }

func TestW2AFinalGateNamesEveryUncoveredField(t *testing.T) {
	c := w2aResume()
	cf := w2aClaims(c)
	errs := w2aJoin(finalPreconditions(c, cf, ""))
	for id, field := range w2aTokenFields {
		if !strings.Contains(errs, id) || !strings.Contains(errs, field) {
			t.Errorf("final preconditions do not name %s in %q:\n%s", id, field, errs)
		}
	}
	R := lint(c, cf, true, "")
	all := w2aJoin(R.Errors)
	for id := range w2aTokenFields {
		if !strings.Contains(all, id) {
			t.Errorf("lint --final does not name %s:\n%s", id, all)
		}
	}
}

func TestW2AClaimUsageCoversEveryRenderedField(t *testing.T) {
	u := claimUsage(w2aResume())
	for id := range w2aTokenFields {
		if len(u[id]) == 0 {
			t.Errorf("claimUsage has no sentence for %s, so it could never be double-verified", id)
		}
	}
	if len(u["clmA"]) != 1 {
		t.Errorf("bullet token usage = %v", u["clmA"])
	}
}

func TestW2ACoveredFieldsPass(t *testing.T) {
	c := w2aResume()
	cf := w2aClaims(c)
	for id := range w2aTokenFields {
		for _, s := range claimUsage(c)[id] {
			cv := cf.Claims[id]
			cv.ConfirmedSentences = append(cv.ConfirmedSentences, sentenceHash(s, id))
		}
	}
	if errs := finalPreconditions(c, cf, ""); len(errs) != 0 {
		t.Errorf("every figure is confirmed in its own text, yet: %v", errs)
	}
}

func TestW2ARenderFinalExitsNonZero(t *testing.T) {
	root := t.TempDir()
	c := w2aResume()
	cf := w2aClaims(c)
	sel := filepath.Join(root, "r.composed.json")
	writeJSON(sel, c)
	cfp := filepath.Join(root, "claims.json")
	writeJSON(cfp, cf)
	out, code := runSample(t, root, "render", sel, "--claims", cfp, "--out-dir", filepath.Join(root, "out"))
	if code != 0 {
		t.Fatalf("a draft render keeps exit 0, got %d:\n%s", code, out)
	}
	out, code = runSample(t, root, "render", sel, "--claims", cfp, "--final", "--out-dir", filepath.Join(root, "outf"))
	if code == 0 {
		t.Fatalf("render --final must fail while figures are unverified:\n%s", out)
	}
	for id := range w2aTokenFields {
		if !strings.Contains(out, id) {
			t.Errorf("render --final does not list %s:\n%s", id, out)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "outf", "r.composed.txt")); err == nil {
		t.Errorf("a refused final render must not write outputs")
	}
}

func TestW2ADocxFinalIsGated(t *testing.T) {
	root := t.TempDir()
	c := w2aResume()
	sel := filepath.Join(root, "r.composed.json")
	writeJSON(sel, c)
	cfp := filepath.Join(root, "claims.json")
	writeJSON(cfp, w2aClaims(c))
	docxOut := filepath.Join(root, "r.docx")
	if out, code := runSample(t, root, "docx", sel, "--claims", cfp, "--out", docxOut); code == 0 {
		t.Fatalf("docx is always final and must fail while figures are unverified:\n%s", out)
	}
	if _, err := os.Stat(docxOut); err == nil {
		t.Errorf("a refused docx export must not write the file")
	}
}
