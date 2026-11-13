package main

import (
	"context"
	"errors"
	"fmt"

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
// With --stale-drops it reports every dropped posting today's rules would not
// drop (read-only), and with --rescue returns the model drops among them to
// unsure (retriagestale.go). It spends nothing and calls no backend. --dry-run reads the ledger without
// its lock (as rescore --dry-run does) and changes nothing.
func (e *env) cmdRetriage(args []string) error {
	flags := e.newFlags("retriage")
	dry := flags.Bool("dry-run", false, "list what would be returned to live; read the ledger only, change nothing")
	titles := flags.Bool("title-drops", false, "return postings dropped on their title that title_signals.read and the drop rules no longer drop")
	stale := flags.Bool("stale-drops", false, "report every dropped posting today's rules (exclude, title_signals.read, drop_rules on the stored readings) would not drop, by drop class; read-only, no model")
	rescue := flags.Bool("rescue", false, "with --stale-drops: return the model drops among them to unsure, readings kept; with --silver-drops: return the auditor's picks to unsure within the daily cap, only while its verdict on gold is go; with --dry-run only count")
	silver := flags.Bool("silver-drops", false, "report the model drops the would_apply auditor keeps picking over their stratum (the rescue rule D6) and its precision on gold; read-only, no model")
	minScore := flags.Float64("min-score", defaultSilverMinScore, "with --silver-drops: the auditor's score (0.5-1) a drop needs")
	minGames := flags.Int("min-games", defaultSilverMinGames, fmt.Sprintf("with --silver-drops: the fewest comparisons (at least %d)", pipeline.MinSilverRescueGames))
	maxPerDay := flags.Int("max-per-day", defaultSilverMaxPerDay, fmt.Sprintf("with --silver-drops --rescue: the most rescues per UTC day (1-%d)", pipeline.MaxSilverRescuePerDay))
	asJSON := e.jsonFlag(flags, `{"dry_run","title_drops","returned","keys"} on stdout; with --stale-drops {"stale_drops","dry_run","rescue","dropped","still_dropped","stale","not_judged","not_judged_why","classes","items","items_shown","truncated","rescued","rescued_keys","left_to_other"}`)
	if err := parse(flags, args); err != nil {
		return err
	}
	switch {
	case *stale && *titles:
		return usagef("retriage: --stale-drops and --title-drops are separate; --stale-drops lists title drops too")
	case *silver && (*stale || *titles):
		return usagef("retriage: --silver-drops is separate from --stale-drops and --title-drops")
	case *rescue && !*stale && !*silver:
		return usagef("retriage: --rescue needs --stale-drops or --silver-drops")
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	if *silver {
		return e.retriageSilver(cfg, pipeline.SilverRule{MinScore: *minScore, MinGames: *minGames, MaxPerDay: *maxPerDay}, *dry, *rescue, *asJSON)
	}
	if *stale {
		return e.retriageStale(cfg, *dry, *rescue, *asJSON)
	}
	var l ledger.Ledger
	if *dry {
		snap, err := e.readLedgerSnapshot("retriage") // no ledger yet: nothing to return
		if err != nil {
			return err
		}
		l = snap
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
	var keys []posting.Key
	why := "triaged with no backend reading"
	if *titles {
		keys, err = pipeline.RetriageTitleDrops(ctx, l, now(), dropPolicy(cfg, cfg.Decide.OpenRouterName()), *dry)
		why = "dropped on a title the current rules no longer drop"
	} else {
		keys, err = pipeline.RetriageUnread(ctx, l, now(), *dry)
	}
	if *asJSON {
		return e.reply(true, &retriageRunReply{DryRun: *dry, TitleDrops: *titles, Returned: len(keys), Keys: nonNil(keys)}, err, nil)
	}
	fmt.Fprintf(e.stdout, "%d posting(s) %s (%s)\n", len(keys), verb, why)
	return err
}

// retriageRunReply is `vani retriage --json`: the postings returned to live (or
// that would be, in a dry run). A failure part way keeps the ones it returned.
type retriageRunReply struct {
	DryRun     bool          `json:"dry_run"`
	TitleDrops bool          `json:"title_drops"`
	Returned   int           `json:"returned"`
	Keys       []posting.Key `json:"keys"`
	failure
}
