package cover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderStampsADrawableLetterheadAndUsesStandardFonts(t *testing.T) {
	const head = "HXTOKENQX"
	pdf, err := Render("I want this role at Northwind. The work matches what I have done.", []string{head, "second line"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(pdf)
	if !strings.HasPrefix(text, "%PDF-") {
		t.Fatal("not a PDF")
	}
	if !strings.Contains(text, "/Helvetica") || !strings.Contains(text, "/Helvetica-Bold") || !strings.Contains(text, "/WinAnsiEncoding") {
		t.Fatal("standard fonts missing")
	}
	if strings.Contains(text, "/Subtype /Type0") || strings.Contains(text, "CIDFont") {
		t.Fatal("a non-standard font was embedded")
	}
	if !strings.Contains(text, head) || !strings.Contains(text, "Northwind") {
		t.Fatal("letterhead or body missing from the page")
	}
	if strings.Count(text, "/Type /Page ") != 1 || !strings.Contains(text, "/Count 1") {
		t.Fatal("not one page")
	}
}

func TestRenderDropsALetterheadTheFontCannotDraw(t *testing.T) {
	pdf, err := Render("I want this role at Northwind.", []string{"HXTOKENQX", "λ"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(pdf), "HXTOKENQX") || strings.Contains(string(pdf), "λ") {
		t.Fatal("an undrawable letterhead was stamped")
	}
	if !strings.Contains(string(pdf), "Northwind") {
		t.Fatal("the body was dropped with the letterhead")
	}
}

func TestRenderKeepsNoPDFWhenTheBodyCannotBeDrawn(t *testing.T) {
	pdf, err := Render("I want this role at Northwind. λ", nil)
	if pdf != nil || CodeOf(err) != CodeRender {
		t.Fatalf("pdf %d err %v", len(pdf), err)
	}
}

func TestRenderRefusesALetterThatDoesNotFitOnePage(t *testing.T) {
	body := strings.Repeat("word ", 4000)
	pdf, err := Render(body, nil)
	if pdf != nil || CodeOf(err) != CodeRender {
		t.Fatalf("a long letter produced a PDF (%d) err %v", len(pdf), err)
	}
}

func TestLetterheadLinesComeFromTheBaseSelectionOnly(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.json")
	bank := filepath.Join(dir, "bank.json")
	if err := os.WriteFile(base, []byte(`{"header":"primary","sections":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bank, []byte(`{"headers":{"primary":{"name":"HXTOKENQX","contact_lines":["line-two"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	lines := LetterheadLines(base, bank)
	if strings.Join(lines, "|") != "HXTOKENQX|line-two" {
		t.Fatalf("lines %#v", lines)
	}
	if LetterheadLines(filepath.Join(dir, "missing.json"), bank) != nil {
		t.Fatal("a missing base selection looked like a letterhead")
	}
	if LetterheadLines("", bank) != nil {
		t.Fatal("an empty path looked like a letterhead")
	}
}
