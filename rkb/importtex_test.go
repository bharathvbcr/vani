package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// import-tex writes the bank through the same compare-and-swap save as every
// other command. It used to build the bank without a guard, so save had no
// path and every import died at the rename.
func TestImportTexWritesTheBank(t *testing.T) {
	root := t.TempDir()
	cv := filepath.Join(root, cfg.CVDir)
	if err := os.MkdirAll(cv, 0o755); err != nil {
		t.Fatal(err)
	}
	tex := "\\documentclass{article}\n\\begin{document}\n\\section{Experience}\n\\begin{itemize}\n\\item Cut build time from 40 to 10 minutes.\n\\end{itemize}\n\\end{document}\n"
	if err := os.WriteFile(filepath.Join(cv, "Resume_Sample.tex"), []byte(tex), 0o644); err != nil {
		t.Fatal(err)
	}
	bank := filepath.Join(root, cfg.Bank)

	out, rc := runSample(t, root, "bank", "import-tex")
	if rc != 0 {
		t.Fatalf("import-tex: rc=%d\n%s", rc, out)
	}
	b, err := os.ReadFile(bank)
	if err != nil {
		t.Fatalf("no bank written: %v", err)
	}
	if _, err := parseBank(bank, b); err != nil {
		t.Fatalf("written bank does not load: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(bank), "resumes", "resume-sample.json")); err != nil {
		t.Fatalf("no selection written: %v", err)
	}

	if out, rc := runSample(t, root, "bank", "import-tex"); rc == 0 || !strings.Contains(out, "--force") {
		t.Fatalf("a second import without --force was not refused: rc=%d\n%s", rc, out)
	}
	if err := os.WriteFile(bank, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, rc := runSample(t, root, "bank", "import-tex", "--force"); rc != 0 {
		t.Fatalf("import-tex --force: rc=%d\n%s", rc, out)
	}
	if b, _ := os.ReadFile(bank); string(b) == "{}\n" {
		t.Fatal("--force did not replace the bank")
	}

	// No LaTeX sources: --force must not replace the bank with an empty one.
	if err := os.Remove(filepath.Join(cv, "Resume_Sample.tex")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(bank)
	if out, rc := runSample(t, root, "bank", "import-tex", "--force"); rc == 0 || !strings.Contains(out, "nothing to import") {
		t.Fatalf("an import with no .tex files was not refused: rc=%d\n%s", rc, out)
	}
	if after, _ := os.ReadFile(bank); string(after) != string(before) {
		t.Fatal("an import with no .tex files changed the bank")
	}
}
