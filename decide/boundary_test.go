package decide

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The module imports only the standard library and itself. That keeps Rule 7
// one-directional by construction: decide can never reach a caller's
// approval, policy or notification code, and it adds no dependency to the
// repos that use it.
func TestStdlibOnly(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	root := filepath.Dir(file)
	fset := token.NewFileSet()
	files := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		files++
		for _, imp := range f.Imports {
			ip, _ := strconv.Unquote(imp.Path.Value)
			first := strings.SplitN(ip, "/", 2)[0]
			if ip != "github.com/bharathvbcr/decide" && !strings.HasPrefix(ip, "github.com/bharathvbcr/decide/") && strings.Contains(first, ".") {
				t.Errorf("%s imports %s: decide is stdlib-only", path, ip)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("scanned no Go files: the check did not run")
	}
}
