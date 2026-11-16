package lappi

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// devCouncilClient is DevCouncil's copy of client.go, which this package was
// moved from. Until DevCouncil imports this module (a change that touches the
// Manvi and Jarvis pins), the two copies must stay identical in code.
const devCouncilClient = "DevCouncil/backend/go_orchestrator/devcouncil/lappi/client.go"

// devCouncilRoots are where a DevCouncil checkout sits relative to this
// package (decide/lappi in the Vani repo): .deps/ is where scripts/bootstrap.sh
// clones it (and CI runs it), and ../../../../devtools/ is the owner's
// $CODE_ROOT/apps/Vani + $CODE_ROOT/devtools layout. The first that exists is
// compared.
var devCouncilRoots = []string{"../../.deps", "../../../../devtools"}

// codeDecls prints every top-level declaration of a Go file without comments.
func codeDecls(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0) // no ParseComments: comments are dropped
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range f.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			continue // imports may differ in grouping only
		}
		var b bytes.Buffer
		if err := printer.Fprint(&b, fset, d); err != nil {
			t.Fatal(err)
		}
		out = append(out, b.String())
	}
	return out
}

// TestClientMatchesDevCouncilCopy fails when either copy of the client code
// changes without the other. It skips, saying so and naming every place it
// looked, when no DevCouncil checkout is found.
func TestClientMatchesDevCouncilCopy(t *testing.T) {
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	dir := filepath.Dir(here)
	theirs := ""
	var tried []string
	for _, root := range devCouncilRoots {
		p := filepath.Join(dir, root, devCouncilClient)
		tried = append(tried, p)
		if _, err := os.Stat(p); err == nil {
			theirs = p
			break
		}
	}
	if theirs == "" {
		t.Skipf("no DevCouncil checkout found (looked at %v): drift NOT checked", tried)
	}
	t.Logf("comparing with %s", theirs)
	ours, them := codeDecls(t, filepath.Join(dir, "client.go")), codeDecls(t, theirs)
	if len(ours) != len(them) {
		t.Fatalf("client.go has %d declarations, DevCouncil's copy %d: port the change to both", len(ours), len(them))
	}
	for i := range ours {
		if ours[i] != them[i] {
			t.Errorf("declaration %d differs from DevCouncil's copy (port the change to both):\nours:\n%s\ntheirs:\n%s", i, ours[i], them[i])
		}
	}
}

// The detector must be able to fail.
func TestCodeDeclsSeesCodeNotComments(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := codeDecls(t, write("a.go", "package x\n// one\nfunc F() int { return 1 }\n"))
	b := codeDecls(t, write("b.go", "package x\n// two\nfunc F() int { return 1 }\n"))
	c := codeDecls(t, write("c.go", "package x\nfunc F() int { return 2 }\n"))
	if a[0] != b[0] {
		t.Fatal("a comment change counted as drift")
	}
	if a[0] == c[0] {
		t.Fatal("a code change was not seen")
	}
}
