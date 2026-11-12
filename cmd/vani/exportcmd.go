package main

import (
	"encoding/json"
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

// cmdExport is `vani export heldout` or `vani export posting-fingerprints`.
func (e *env) cmdExport(args []string) error {
	if len(args) == 0 {
		return usagef("export: want `export heldout` or `export posting-fingerprints`")
	}
	switch args[0] {
	case "heldout":
		return e.cmdExportHeldout(args)
	case "posting-fingerprints":
		return e.cmdExportPostingFingerprints(args)
	default:
		return usagef("export: want `export heldout` or `export posting-fingerprints`")
	}
}

func (e *env) cmdExportHeldout(args []string) error {
	if len(args) == 0 || args[0] != "heldout" {
		return usagef("export: want `export heldout [--out PATH] [--labels DIR]`")
	}
	fs := e.newFlags("export heldout")
	out := fs.String("out", "", "output file (default: <state dir>/export/heldout/owner_labels.jsonl)")
	labels := fs.String("labels", "", "label directory (default: state dir)")
	asJSON := e.jsonFlag(fs, `{"path","exported","missing"} on stdout`)
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
	if *asJSON {
		var err error
		if len(missing) > 0 {
			err = fmt.Errorf("export heldout: %d labelled posting(s) have no text and are not in the export", len(missing))
		}
		return e.reply(true, &heldoutReply{Path: path, Exported: n, Missing: nonNil(missing)}, err, nil)
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

// cmdExportPostingFingerprints is `vani export posting-fingerprints [--out PATH]`.
// It writes qd-posting-heldout.v1 for the labelled postings. It does not
// change the owner_labels.jsonl export.
func (e *env) cmdExportPostingFingerprints(args []string) error {
	fs := e.newFlags("export posting-fingerprints")
	out := fs.String("out", "", "output file (default: <state dir>/export/heldout/signatures.json)")
	labels := fs.String("labels", "", "label directory (default: state dir)")
	seed := fs.Uint64("seed", eval.DoorSeed, "MinHash seed")
	asJSON := e.jsonFlag(fs, `{"path","exported","missing"} on stdout`)
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	s, err := e.openEvalStore(*labels)
	if err != nil {
		return err
	}
	path := *out
	if path == "" {
		path = filepath.Join(e.stateDir, "export", "heldout", "signatures.json")
	}
	recs, err := e.ledgerRecords()
	if err != nil {
		return err
	}
	snap := ledger.Snapshot{Records: recs}
	raw, missing, err := eval.ExportLabelledFingerprints(s, func(k posting.Key) (string, bool) {
		r, ok := findRecord(snap, k)
		return r.Posting.Description, ok
	}, *seed)
	if err != nil {
		return err
	}
	// The count is read back from what is written, so it cannot disagree
	// with the file.
	var doc struct {
		Entries []json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("export posting-fingerprints: the export is not readable back: %w", err)
	}
	if err := eval.WriteDoorExport(path, raw); err != nil {
		return err
	}
	var missingErr error
	if len(missing) > 0 {
		missingErr = fmt.Errorf("export posting-fingerprints: %d labelled posting(s) have no text and are not in the export", len(missing))
	}
	if *asJSON {
		return e.reply(true, &heldoutReply{Path: path, Exported: len(doc.Entries), Missing: nonNil(missing)}, missingErr, nil)
	}
	fmt.Fprintf(e.stdout, "exported %d posting fingerprint(s) to %s\n", len(doc.Entries), path)
	for _, k := range missing {
		fmt.Fprintf(e.stderr, "vani: no text for labelled posting %s; left out of the export\n", k)
	}
	return missingErr
}

// heldoutReply is `vani export heldout --json`. Missing are the labelled keys
// with no text, left out of the export; any makes the reply a failure.
type heldoutReply struct {
	Path     string        `json:"path"`
	Exported int           `json:"exported"`
	Missing  []posting.Key `json:"missing"`
	failure
}

// exportHeldout writes the held-out export for s to path (atomically) and
// returns the row count and the labelled keys that had no usable text.
func (e *env) exportHeldout(s *eval.Store, path string) (int, []posting.Key, error) {
	recs, err := e.ledgerRecords()
	if err != nil {
		return 0, nil, err
	}
	snap := ledger.Snapshot{Records: recs}
	x, err := eval.ExportHeldoutLocked(path, s, func(k posting.Key) (string, bool) {
		r, ok := findRecord(snap, k)
		return r.Posting.Description, ok
	})
	if err != nil {
		return 0, nil, err
	}
	return x.Rows, x.Missing, nil
}

// reexportHeldout is the label store's AfterWrite hook: the export follows
// every newly written label. A failure is reported, never fatal, because the
// label itself has been saved; `vani export heldout` repeats it.
func (e *env) reexportHeldout(s *eval.Store) {
	defer func() {
		if r := recover(); r != nil {
			// Never print r: a panic value may carry posting text.
			fmt.Fprintln(e.stderr, "vani: held-out export panicked and was not updated; the label was saved")
		}
	}()
	_, missing, err := e.exportHeldout(s, e.heldoutExportPath())
	if err != nil {
		fmt.Fprintf(e.stderr, "vani: held-out export not updated: %v\n", err)
		return
	}
	for _, k := range missing {
		fmt.Fprintf(e.stderr, "vani: no text for labelled posting %s; left out of the held-out export\n", k)
	}
}
