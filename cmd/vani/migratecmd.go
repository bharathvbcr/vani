package main

import (
	"fmt"

	"github.com/bharathvbcr/Vani/internal/rekey"
)

// cmdMigrate runs a one-time state migration. The only one is rekey-fallback
// (internal/rekey): fallback-keyed (x:) postings move to the keys titles that
// keep C++, C# and .NET apart give them. It takes the ledger's lock, so it
// refuses while the daemon or a run holds it.
func (e *env) cmdMigrate(args []string) error {
	if len(args) == 0 || args[0] != "rekey-fallback" {
		return usagef("migrate: expected subcommand \"rekey-fallback\"")
	}
	fs := e.newFlags("migrate rekey-fallback")
	dry := fs.Bool("dry-run", false, "plan and check for collisions; write nothing")
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	rep, err := rekey.Run(e.stateDir, *dry, now())
	if err != nil {
		return err
	}
	verb := "moved"
	if rep.DryRun {
		verb = "would move"
	}
	fmt.Fprintf(e.stdout, "postings: %d, fallback-keyed (x:): %d, %s: %d, unchanged: %d, not derived from their fields (left alone): %d\n",
		rep.Records, rep.Fallback, verb, len(rep.Changes), rep.Unchanged, len(rep.Foreign))
	for _, c := range rep.Changes {
		fmt.Fprintf(e.stdout, "  %s -> %s\n", c.Old, c.New)
	}
	for _, k := range rep.Foreign {
		fmt.Fprintf(e.stdout, "  left alone: %s\n", k)
	}
	if !rep.DryRun {
		if rep.Resumed {
			fmt.Fprintln(e.stdout, "finished an earlier run that had stopped part way")
		}
		fmt.Fprintf(e.stdout, "marks moved: %d\nbackup: %s\nrecord: %s (in the ledger directory; a second run is refused)\n",
			rep.MarksMoved, rep.Backup, rekey.MarkerName)
	}
	return nil
}
