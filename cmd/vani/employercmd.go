package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/bharathvbcr/Vani/internal/employer"
	"github.com/bharathvbcr/Vani/internal/pipeline"
)

// employerIndexPath is where the merged employer-attribute index lives.
func employerIndexPath(stateDir string) string {
	return filepath.Join(stateDir, "employer", "index.json")
}

// loadEmployerView opens the read-only lookup view, or returns nil when
// nothing has been imported. A corrupt index is an error, never a silent "no
// data". warnf hears that the view had to be rebuilt from the full index,
// which costs the full load once, or could not be written back, which costs
// it on every run.
func loadEmployerView(stateDir string, warnf func(format string, a ...any)) (*employer.View, error) {
	v, info, err := employer.LoadView(employerIndexPath(stateDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("employer index: %w", err)
	}
	if info.Rebuilt {
		warnf("employer: lookup view rebuilt from the full index (%v)", info.Reason)
	}
	if info.WriteErr != nil {
		warnf("employer: lookup view not written, so every run loads the full index: %v", info.WriteErr)
	}
	return v, nil
}

func (e *env) cmdEmployer(args []string) error {
	if len(args) == 0 {
		return usagef("employer: expected import or lookup")
	}
	switch args[0] {
	case "import":
		return e.employerImport(args[1:])
	case "lookup":
		return e.employerLookup(args[1:])
	}
	return usagef("employer: unknown subcommand %q", args[0])
}

func (e *env) employerImport(args []string) error {
	fs := e.newFlags("employer import")
	asJSON := e.jsonFlag(fs, `{"source","names","files"} on stdout`)
	if err := parsePositional(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usagef("employer import: expected one CSV file (see docs/EMPLOYER.md)")
	}
	path := fs.Arg(0)
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	part, err := employer.Import(f, filepath.Base(path))
	if err != nil {
		return fmt.Errorf("employer import %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(employerIndexPath(e.stateDir)), 0o700); err != nil {
		return err
	}
	x, err := employer.Update(employerIndexPath(e.stateDir), part)
	if err != nil {
		return fmt.Errorf("employer index: %w", err)
	}
	s := part.Source
	return e.reply(*asJSON, struct {
		Source employer.SourceMeta `json:"source"`
		Names  int                 `json:"names"`
		Files  int                 `json:"files"`
	}{s, x.Names(), len(x.Sources())}, nil, func() {
		fmt.Fprintf(e.stdout, "imported %s (%s): %d of %d rows counted, skipped %v; index now %d names from %d files\n",
			s.Name, s.Kind, s.RowsCounted, s.RowsRead, s.Skipped, x.Names(), len(x.Sources()))
	})
}

// employerLookupReply is `vani employer lookup --json`: the badge's parts and
// its text. Confident is whether the match is strong enough to boost rank.
type employerLookupReply struct {
	Company     string   `json:"company"`
	MatchedName string   `json:"matched_name"`
	Confidence  string   `json:"confidence"`
	Confident   bool     `json:"confident"`
	Attributes  []string `json:"attributes"`
	Kinds       []string `json:"kinds"`
	Badge       string   `json:"badge"`
}

func (e *env) employerLookup(args []string) error {
	fs := e.newFlags("employer lookup")
	asJSON := e.jsonFlag(fs, "one JSON object on stdout")
	if err := parsePositional(fs, args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return usagef("employer lookup: expected a company name")
	}
	x, err := loadEmployerView(e.stateDir, func(format string, a ...any) {
		fmt.Fprintf(e.stderr, "vani: "+format+"\n", a...)
	})
	if err == nil && x == nil {
		err = errors.New("no employer lists imported yet (vani employer import <file.csv>; see docs/EMPLOYER.md)")
	}
	if err != nil {
		return err
	}
	company := strings.Join(fs.Args(), " ")
	b := x.Lookup(company)
	out := employerLookupReply{Company: company, MatchedName: b.MatchedName, Confidence: b.Confidence,
		Confident: b.Confident(), Attributes: nonNil(b.Attributes), Kinds: nonNil(b.Kinds), Badge: b.String()}
	return e.reply(*asJSON, out, nil, func() { fmt.Fprintln(e.stdout, b.String()) })
}

// employerLookup adapts the index to pipeline.EmployerLookup. Only a
// confident match carries attributes, so only it can boost rank.
type employerLookup struct{ x *employer.View }

func (l employerLookup) Employer(company string) pipeline.EmployerInfo {
	b := l.x.Lookup(company)
	info := pipeline.EmployerInfo{Badge: b.String()}
	if b.Confident() {
		info.Attributes = b.Attributes
	}
	return info
}
