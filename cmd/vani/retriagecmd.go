package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// errSnapshotReadOnly is what a snapshotLedger answers to any write.
var errSnapshotReadOnly = errors.New("ledger snapshot is read-only")

// snapshotLedger is a ledger.Ledger over a lock-free ReadSnapshot, for the
// planning functions' dry-run path: they read All and write nothing. A write
// is refused, so a planner that forgot its dry-run check fails loudly.
type snapshotLedger struct{ recs []ledger.Record }

func (s snapshotLedger) Append(context.Context, ledger.Event) error { return errSnapshotReadOnly }
func (s snapshotLedger) AppendBatch(context.Context, []ledger.Event) error {
	return errSnapshotReadOnly
}
func (s snapshotLedger) All() []ledger.Record { return s.recs }
func (s snapshotLedger) Close() error         { return nil }
func (s snapshotLedger) Get(k posting.Key) (ledger.Record, bool) {
	for _, r := range s.recs {
		if r.Key == k {
			return r, true
		}
	}
	return ledger.Record{}, false
}

// cmdRetriage returns postings that were triaged while no decision backend
// could read them (budget spent, provider down, a broken build) to live, so
// the next run triages them. With --title-drops it instead returns postings
// dropped on their title that the configured title readings no longer drop.
// It spends nothing and calls no backend. --dry-run reads the ledger without
// its lock (as rescore --dry-run does) and changes nothing.
func (e *env) cmdRetriage(args []string) error {
	flags := e.newFlags("retriage")
	dry := flags.Bool("dry-run", false, "list what would be returned to live; read the ledger only, change nothing")
	titles := flags.Bool("title-drops", false, "return postings dropped on their title that title_signals.read and the drop rules no longer drop")
	if err := parse(flags, args); err != nil {
		return err
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	var l ledger.Ledger
	if *dry {
		snap, err := ledger.ReadSnapshot(filepath.Join(e.stateDir, "ledger"))
		if err != nil && !errors.Is(err, fs.ErrNotExist) { // no ledger yet: nothing to return
			return fmt.Errorf("retriage: read the ledger: %w", err)
		}
		if snap.Torn {
			fmt.Fprintln(e.stderr, "vani: retriage: the ledger ends in a torn line; it was left out")
		}
		l = snapshotLedger{recs: snap.Records}
	} else {
		app, err := buildApp(cfg, e.stateDir, e.stderr)
		if err != nil {
			return err
		}
		defer app.close()
		l = app.Ledger
	}
	ctx, cancel := signalContext()
	defer cancel()
	verb := "returned to live"
	if *dry {
		verb = "would return to live"
	}
	if *titles {
		keys, err := pipeline.RetriageTitleDrops(ctx, l, now(), dropPolicy(cfg, cfg.Decide.OpenRouterName()), *dry)
		fmt.Fprintf(e.stdout, "%d posting(s) %s (dropped on a title the current rules no longer drop)\n", len(keys), verb)
		return err
	}
	keys, err := pipeline.RetriageUnread(ctx, l, now(), *dry)
	fmt.Fprintf(e.stdout, "%d posting(s) %s (triaged with no backend reading)\n", len(keys), verb)
	return err
}
