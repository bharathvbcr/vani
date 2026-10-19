package main

import (
	"fmt"

	"github.com/bharathvbcr/Vani/internal/pipeline"
)

// cmdRetriage returns postings that were triaged while no decision backend
// could read them (budget spent, provider down, a broken build) to live, so
// the next run triages them. It spends nothing and calls no backend.
func (e *env) cmdRetriage(args []string) error {
	fs := e.newFlags("retriage")
	dry := fs.Bool("dry-run", false, "list what would be returned to live; change nothing")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	app, err := buildApp(cfg, e.stateDir, e.stderr)
	if err != nil {
		return err
	}
	defer app.close()
	ctx, cancel := signalContext()
	defer cancel()
	keys, err := pipeline.RetriageUnread(ctx, app.Ledger, now(), *dry)
	verb := "returned to live"
	if *dry {
		verb = "would return to live"
	}
	fmt.Fprintf(e.stdout, "%d posting(s) %s (triaged with no backend reading)\n", len(keys), verb)
	return err
}
