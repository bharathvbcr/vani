package main

import (
	"fmt"
	"path/filepath"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// heldoutExportPath is where the owner-labelled keys and text hashes go by
// default: under Vani's state dir, never inside another repository.
func (e *env) heldoutExportPath() string {
	return filepath.Join(e.stateDir, "export", "heldout", "owner_labels.jsonl")
}

// cmdExport is `vani export heldout [--out PATH] [--labels DIR]`.
func (e *env) cmdExport(args []string) error {
	if len(args) == 0 || args[0] != "heldout" {
		return usagef("export: want `export heldout [--out PATH] [--labels DIR]`")
	}
	fs := e.newFlags("export heldout")
	out := fs.String("out", "", "output file (default: <state dir>/export/heldout/owner_labels.jsonl)")
	labels := fs.String("labels", "", "label directory (default: state dir)")
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	s, err := e.openEvalStore(*labels)
	if err != nil {
		return err
	}
	path := *out
	if path == "" {
		path = e.heldoutExportPath()
	}
	n, missing, err := e.exportHeldout(s, path)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "exported %d owner-labelled posting(s) to %s\n", n, path)
	if len(missing) > 0 {
		for _, k := range missing {
			fmt.Fprintf(e.stderr, "vani: no text for labelled posting %s; left out of the export\n", k)
		}
		return fmt.Errorf("export heldout: %d labelled posting(s) have no text and are not in the export", len(missing))
	}
	return nil
}

// exportHeldout writes the held-out export for s to path (atomically) and
// returns the row count and the labelled keys that had no usable text.
func (e *env) exportHeldout(s *eval.Store, path string) (int, []posting.Key, error) {
	recs, err := e.ledgerRecords()
	if err != nil {
		return 0, nil, err
	}
	snap := ledger.Snapshot{Records: recs}
	x, err := eval.BuildHeldoutExport(s, func(k posting.Key) (string, bool) {
		r, ok := findRecord(snap, k)
		return r.Posting.Description, ok
	})
	if err != nil {
		return 0, nil, err
	}
	if err := eval.WriteHeldoutExport(path, x); err != nil {
		return 0, nil, err
	}
	return x.Rows, x.Missing, nil
}

// reexportHeldout is the label store's AfterWrite hook: the export follows
// every newly written label. A failure is reported, never fatal, because the
// label itself has been saved; `vani export heldout` repeats it.
func (e *env) reexportHeldout(s *eval.Store) {
	_, missing, err := e.exportHeldout(s, e.heldoutExportPath())
	if err != nil {
		fmt.Fprintf(e.stderr, "vani: held-out export not updated: %v\n", err)
		return
	}
	for _, k := range missing {
		fmt.Fprintf(e.stderr, "vani: no text for labelled posting %s; left out of the held-out export\n", k)
	}
}
