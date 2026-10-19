package main

import (
	"fmt"

	"github.com/bharathvbcr/Vani/internal/pipeline"
)

// cmdRetriage returns postings that were triaged while no decision backend
// could read them (budget spent, provider down, a broken build) to live, so
// the next run triages them. With --title-drops it instead returns postings
// dropped on their title that the configured title readings no longer drop.
// It spends nothing and calls no backend.
func (e *env) cmdRetriage(args []string) error {
	fs := e.newFlags("retriage")
	dry := fs.Bool("dry-run", false, "list what would be returned to live; change nothing")
	titles := fs.Bool("title-drops", false, "return postings dropped on their title that title_signals.read and the drop rules no longer drop")
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
	verb := "returned to live"
	if *dry {
		verb = "would return to live"
	}
	if *titles {
		keys, err := pipeline.RetriageTitleDrops(ctx, app.Ledger, now(), dropPolicy(cfg, cfg.Decide.OpenRouterName()), *dry)
		fmt.Fprintf(e.stdout, "%d posting(s) %s (dropped on a title the current rules no longer drop)\n", len(keys), verb)
		return err
	}
	keys, err := pipeline.RetriageUnread(ctx, app.Ledger, now(), *dry)
	fmt.Fprintf(e.stdout, "%d posting(s) %s (triaged with no backend reading)\n", len(keys), verb)
	return err
}
