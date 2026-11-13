package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Bounds on what `retriage --stale-drops` lists. The counts always cover every
// dropped posting; only the list of keys is capped, and the reply says so.
const (
	maxStaleItemsJSON = 1000
	maxStaleItemsText = 50
)

// staleDropsReply is `vani retriage --stale-drops --json`: every dropped posting
// re-judged under today's rules. Keys and drop classes, no posting text.
// Dropped = StillDropped + Stale + NotJudged. With --rescue, Rescued is the
// model drops returned to unsure (the ones that would be, with --dry-run).
type staleDropsReply struct {
	StaleDrops   bool                  `json:"stale_drops"`
	DryRun       bool                  `json:"dry_run"`
	Rescue       bool                  `json:"rescue"`
	Dropped      int                   `json:"dropped"`
	StillDropped int                   `json:"still_dropped"`
	Stale        int                   `json:"stale"`
	NotJudged    int                   `json:"not_judged"`
	NotJudgedWhy map[string]int        `json:"not_judged_why"`
	Classes      []pipeline.StaleClass `json:"classes"`
	Items        []pipeline.StaleDrop  `json:"items"`
	ItemsShown   int                   `json:"items_shown"`
	Truncated    bool                  `json:"truncated"`
	Rescued      int                   `json:"rescued"`
	RescuedKeys  []posting.Key         `json:"rescued_keys"`
	LeftToOther  int                   `json:"left_to_other"`
	failure
}

// retriageStale is `vani retriage --stale-drops`: the report is read-only and
// reads the ledger without its lock. --rescue (without --dry-run) takes the
// ledger and returns the stale model drops to unsure.
func (e *env) retriageStale(cfg config.Config, dry, rescue, asJSON bool) error {
	policy := dropPolicy(cfg, cfg.Decide.OpenRouterName())
	rules := evalDropRules(cfg)
	class := func(r ledger.Record) string { return eval.DropClass(r, rules) }
	ctx, cancel := signalContext()
	defer cancel()

	var l ledger.Ledger
	if rescue && !dry {
		app, err := buildApp(cfg, e.stateDir, e.stderr)
		if err != nil {
			return err
		}
		defer app.close()
		l = app.Ledger
	} else {
		snap, err := e.readLedgerSnapshot("retriage")
		if err != nil {
			return err
		}
		l = snap
	}

	rep, err := pipeline.StaleDrops(ctx, l, policy, class)
	reply := &staleDropsReply{StaleDrops: true, DryRun: dry, Rescue: rescue,
		Dropped: rep.Dropped, StillDropped: rep.StillDropped, Stale: rep.Stale, NotJudged: rep.NotJudged,
		NotJudgedWhy: rep.NotJudgedWhy, Classes: rep.Classes, Items: rep.Items, RescuedKeys: []posting.Key{}}
	if err == nil && rescue {
		var out pipeline.StaleRescue
		out, err = pipeline.RescueStaleDrops(ctx, l, now(), policy, dry)
		reply.Rescued, reply.LeftToOther, reply.RescuedKeys = len(out.Rescued), out.LeftToOther, nonNil(out.Rescued)
	}
	limit := maxStaleItemsJSON
	if !asJSON {
		limit = maxStaleItemsText
	}
	reply.Items, reply.Truncated = capStaleItems(reply.Items, limit)
	reply.ItemsShown = len(reply.Items)
	return e.reply(asJSON, reply, err, func() { e.printStale(reply) })
}

// capStaleItems keeps the first limit items and says whether it cut any.
func capStaleItems(items []pipeline.StaleDrop, limit int) ([]pipeline.StaleDrop, bool) {
	if len(items) <= limit {
		return items, false
	}
	return items[:limit], true
}

// readLedgerSnapshot reads the ledger without its lock, for a command that
// writes nothing (retriage --dry-run, --stale-drops). No ledger yet is an empty
// one; a torn last line is left out and said so.
func (e *env) readLedgerSnapshot(cmd string) (snapshotLedger, error) {
	snap, err := ledger.ReadSnapshot(filepath.Join(e.stateDir, "ledger"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return snapshotLedger{}, fmt.Errorf("%s: read the ledger: %w", cmd, err)
	}
	if snap.Torn {
		fmt.Fprintf(e.stderr, "vani: %s: the ledger ends in a torn line; it was left out\n", cmd)
	}
	return snapshotLedger{recs: snap.Records}, nil
}

func (e *env) printStale(r *staleDropsReply) {
	w := e.stdout
	fmt.Fprintf(w, "%d dropped posting(s) re-judged under today's rules: %d still dropped, %d stale, %d not judged\n",
		r.Dropped, r.StillDropped, r.Stale, r.NotJudged)
	if r.NotJudged > 0 {
		why := make([]string, 0, len(r.NotJudgedWhy))
		for k, n := range r.NotJudgedWhy {
			why = append(why, fmt.Sprintf("%s: %d", k, n))
		}
		sort.Strings(why)
		fmt.Fprintf(w, "not judged (a check that could not run is not a pass): %s\n", strings.Join(why, "; "))
	}
	if r.Stale > 0 {
		fmt.Fprintln(w, "stale drops by class (today's rules would not drop them):")
		for _, c := range r.Classes {
			fmt.Fprintf(w, "  %-24s %d\n", c.Class, c.Count)
		}
		for _, it := range r.Items {
			fmt.Fprintf(w, "%s  %s  dropped on [%s]  today: %s (%s)\n", it.Key, it.Class, recordedText(it.Recorded), it.Today, it.Why)
		}
		if r.Truncated {
			fmt.Fprintf(w, "... %d of %d stale drops listed; --json lists up to %d\n", r.ItemsShown, r.Stale, maxStaleItemsJSON)
		}
	}
	switch {
	case r.Rescue && r.DryRun:
		fmt.Fprintf(w, "%d model drop(s) would return to unsure (readings kept)\n", r.Rescued)
	case r.Rescue:
		fmt.Fprintf(w, "%d model drop(s) returned to unsure (readings kept)\n", r.Rescued)
	case r.Stale > 0:
		fmt.Fprintln(w, "read-only: add --rescue to return the model drops among them to unsure")
	}
	if r.Rescue && r.LeftToOther > 0 {
		fmt.Fprintf(w, "%d stale title or exclusion drop(s) left: `vani retriage --title-drops` and the next run's exclusion stage return them to live, so a backend reads them\n", r.LeftToOther)
	}
}

// recordedText is the readings a drop was made on, "slot=value p=0.912 (backend)".
func recordedText(rs []pipeline.StaleReading) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		s := r.Slot
		if r.Value != "" {
			s += "=" + r.Value
		}
		if r.P != nil {
			s += fmt.Sprintf(" p=%.3f", *r.P)
		}
		if r.Backend != "" {
			s += " (" + r.Backend + ")"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}
