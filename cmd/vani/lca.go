package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/bharathvbcr/Vani/internal/lca"
)

// lcaIndexPath is where the merged LCA index lives.
func lcaIndexPath(stateDir string) string { return filepath.Join(stateDir, "lca", "index.json") }

// lcaNotes is where loadLCA says that it had to refuse or could not write the
// lookup view. A routine rebuild (no view yet, or the index was re-imported)
// is silent.
var lcaNotes io.Writer = os.Stderr

// loadLCA opens the lookup view of the index (index.view beside index.json,
// bound to the index file's checksum), or returns nil when no index has been
// imported yet. A missing, stale or corrupt view is rebuilt from the full
// index, so the answers are the index's either way. Any other failure (a
// corrupt index) is an error, never a silent "no data".
func loadLCA(stateDir string) (lca.Looker, error) {
	path := lcaIndexPath(stateDir)
	v, info, err := lca.LoadView(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lca index: %w", err)
	}
	if !info.Routine() {
		fmt.Fprintf(lcaNotes, "vani: lca view %s was rebuilt from the index (reason: %v; write error: %v)\n", lca.ViewPath(path), info.Reason, info.WriteErr)
	}
	return v, nil
}

// loadLCAIndex opens the full index for a merge, or returns nil when none has
// been imported yet.
func loadLCAIndex(stateDir string) (*lca.Index, error) {
	x, err := lca.Load(lcaIndexPath(stateDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lca index: %w", err)
	}
	return x, nil
}

func (e *env) cmdLCA(args []string) error {
	if len(args) == 0 {
		return usagef("lca: expected import or lookup")
	}
	switch args[0] {
	case "import":
		return e.lcaImport(args[1:])
	case "lookup":
		return e.lcaLookup(args[1:])
	}
	return usagef("lca: unknown subcommand %q", args[0])
}

func (e *env) lcaImport(args []string) error {
	fs := e.newFlags("lca import")
	if err := parsePositional(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usagef("lca import: expected one CSV file (see docs/LCA.md)")
	}
	path := fs.Arg(0)
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	part, err := lca.Import(f, filepath.Base(path))
	if err != nil {
		return fmt.Errorf("lca import %s: %w", path, err)
	}
	x, err := loadLCAIndex(e.stateDir)
	if err != nil {
		return err
	}
	if x == nil {
		x = lca.NewIndex()
	}
	if err := x.Merge(part); err != nil {
		return err
	}
	if err := x.Save(lcaIndexPath(e.stateDir)); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "imported %s: %d employers, fiscal years %v\n", filepath.Base(path), x.Employers(), x.Coverage())
	return nil
}

func (e *env) lcaLookup(args []string) error {
	fs := e.newFlags("lca lookup")
	if err := parsePositional(fs, args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return usagef("lca lookup: expected a company name")
	}
	x, err := loadLCA(e.stateDir)
	if err != nil {
		return err
	}
	if x == nil {
		return errors.New("no LCA data imported yet (vani lca import <file.csv>)")
	}
	fmt.Fprintln(e.stdout, x.Lookup(strings.Join(fs.Args(), " ")).String())
	return nil
}

// lcaBadger adapts the LCA index to pipeline.Badger.
type lcaBadger struct{ x lca.Looker }

func (b lcaBadger) Badge(company string) string { return b.x.Lookup(company).String() }
