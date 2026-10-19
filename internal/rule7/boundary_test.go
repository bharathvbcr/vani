// Package rule7 holds the import-boundary test for Rule 7 (Lappi-decision
// docs/caller-contract.md): a decision may only narrow, rank or annotate.
package rule7

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Packages that make allow/pass/approve decisions must not import the
// decision layer (the shared decide module or Vani's triage packages under
// internal/decide), and Vani's triage packages must not import them. The
// shared module is stdlib-only, which its own test enforces.
var gatedPackages = map[string]bool{"vault": true, "notify": true, "approve": true}

func lastElem(p string) string { return p[strings.LastIndex(p, "/")+1:] }

func importsDecide(path string) bool {
	return path == "github.com/bharathvbcr/decide" || strings.HasPrefix(path, "github.com/bharathvbcr/decide/") ||
		strings.HasSuffix(path, "/internal/decide") || strings.Contains(path, "/internal/decide/")
}

// findViolations parses every Go file under internalDir (imports only).
func findViolations(internalDir string) (violations []string, goFiles int, err error) {
	fset := token.NewFileSet()
	err = filepath.WalkDir(internalDir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		goFiles++
		rel, _ := filepath.Rel(internalDir, path)
		rel = filepath.ToSlash(rel)
		dir := filepath.ToSlash(filepath.Dir(rel))
		inDecide := dir == "decide" || strings.HasPrefix(dir, "decide/")
		gatedPkg := gatedPackages[strings.TrimSuffix(f.Name.Name, "_test")] || gatedPackages[lastElem(dir)]
		for _, imp := range f.Imports {
			ip, _ := strconv.Unquote(imp.Path.Value)
			if gatedPkg && importsDecide(ip) {
				violations = append(violations, rel+" imports "+ip)
			}
			if inDecide && gatedPackages[lastElem(ip)] && strings.Contains(ip, "/internal/") {
				violations = append(violations, rel+" imports "+ip)
			}
		}
		return nil
	})
	return violations, goFiles, err
}

func internalDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	return filepath.Dir(filepath.Dir(file)) // .../internal
}

func TestImportBoundary(t *testing.T) {
	v, n, err := findViolations(internalDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("scanned no Go files: the check did not run")
	}
	for _, s := range v {
		t.Error(s)
	}
}

// The check must be able to fail: plant violations in a scratch tree.
func TestImportBoundaryDetectsViolations(t *testing.T) {
	root := t.TempDir()
	write := func(rel, src string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("approve/a.go", "package approve\nimport _ \"github.com/bharathvbcr/decide\"\n")
	write("vault/b.go", "package vault\nimport _ \"github.com/bharathvbcr/decide/jev\"\n")
	write("x/notify/c.go", "package notify\nimport _ \"github.com/bharathvbcr/Vani/internal/decide/jdtriage\"\n")
	write("decide/jdtriage/d.go", "package jdtriage\nimport _ \"github.com/bharathvbcr/Vani/internal/vault\"\n")
	write("decide/llmbackend/e.go", "package llmbackend\nimport _ \"github.com/bharathvbcr/Vani/internal/notify\"\n")
	write("ok/f.go", "package ok\nimport _ \"github.com/bharathvbcr/decide\"\n")
	write("decide/jdtriage/g.go", "package jdtriage\nimport _ \"github.com/bharathvbcr/Vani/internal/posting\"\n")
	write("vault/h.go", "package vault\nimport _ \"github.com/bharathvbcr/decidex\"\n") // a lookalike module is not decide
	v, n, err := findViolations(root)
	if err != nil || n != 8 {
		t.Fatalf("err=%v files=%d", err, n)
	}
	if len(v) != 5 {
		t.Fatalf("violations = %v, want 5", v)
	}
}
