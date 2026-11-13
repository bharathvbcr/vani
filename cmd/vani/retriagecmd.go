package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/jobcategory"
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
// the next run triages them. With --title-drops it instead lets back postings
// dropped on their title that the configured title readings no longer drop;
// with --policy-drops, every dropped posting the current drop policy would no
// longer drop, title and model drops alike (pipeline.RetriagePolicyDrops).
// A drop on a watched board is not put back at once: it waits, still dropped,
// for its board's next full listing to show it (a pending reopen, applied by
// the run's listing). A dry run of either reports what it would let back by
// drop class. --silver-drops is the auditor's rescue rule (silverrescue.go),
// which defers the same way. It spends nothing and calls no backend. --dry-run reads the ledger without its lock (as rescore --dry-run
// does) and changes nothing.
func (e *env) cmdRetriage(args []string) error {
	flags := e.newFlags("retriage")
	dry := flags.Bool("dry-run", false, "list what would be returned to live; read the ledger only, change nothing")
	titles := flags.Bool("title-drops", false, "let back postings dropped on their title that title_signals.read and the drop rules no longer drop")
	policy := flags.Bool("policy-drops", false, "let back postings dropped on their title or on a decision model's reading that the current drop rules no longer drop")
	refresh := flags.Bool("refresh-unjudged", false, fmt.Sprintf("with --policy-drops: also send model drops whose reading cannot be judged, and whose values no drop rule names, for a fresh read (at most %d)", pipeline.MaxRefreshUnjudged))
	rescue := flags.Bool("rescue", false, "with --silver-drops: take the auditor's picks within the daily cap, only while its verdict on gold is go (a watched board's waits for its next listing, then goes to unsure); with --dry-run only count")
	silver := flags.Bool("silver-drops", false, "report the model drops the would_apply auditor keeps picking over their stratum (the rescue rule D6) and its precision on gold; read-only, no model")
	minScore := flags.Float64("min-score", defaultSilverMinScore, "with --silver-drops: the auditor's score (0.5-1) a drop needs")
	minGames := flags.Int("min-games", defaultSilverMinGames, fmt.Sprintf("with --silver-drops: the fewest comparisons (at least %d)", pipeline.MinSilverRescueGames))
	maxPerDay := flags.Int("max-per-day", defaultSilverMaxPerDay, fmt.Sprintf("with --silver-drops --rescue: the most rescues per UTC day (1-%d)", pipeline.MaxSilverRescuePerDay))
	asJSON := e.jsonFlag(flags, `{"dry_run","title_drops","policy_drops","returned","keys","title_returned","model_returned","fresh_returned","refresh_returned","deferred","reopened","already_pending","over_cap","unjudged","stale","unreadable","unreadable_checked","classes"} on stdout`)
	if err := parse(flags, args); err != nil {
		return err
	}
	switch {
	case *titles && *policy:
		return usagef("retriage: --title-drops and --policy-drops are exclusive (--policy-drops includes the title drops)")
	case *refresh && !*policy:
		return usagef("retriage: --refresh-unjudged needs --policy-drops")
	case *silver && (*titles || *policy):
		return usagef("retriage: --silver-drops is separate from --title-drops and --policy-drops")
	case *rescue && !*silver:
		return usagef("retriage: --rescue needs --silver-drops (a drop the rules no longer make comes back through --policy-drops)")
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	if *silver {
		return e.retriageSilver(cfg, pipeline.SilverRule{MinScore: *minScore, MinGames: *minGames, MaxPerDay: *maxPerDay}, *dry, *rescue, *asJSON)
	}
	l, closeLedger, err := e.retriageLedger(cfg, *dry)
	if err != nil {
		return err
	}
	defer closeLedger()
	ctx, cancel := signalContext()
	defer cancel()
	if *titles || *policy {
		return e.retriageDrops(ctx, l, cfg, *dry, *titles, *refresh, *asJSON)
	}
	keys, err := pipeline.RetriageUnread(ctx, l, now(), *dry)
	if *asJSON {
		return e.reply(true, &retriageRunReply{DryRun: *dry, Returned: len(keys), Keys: nonNil(keys)}, err, nil)
	}
	verb := "returned to live"
	if *dry {
		verb = "would return to live"
	}
	fmt.Fprintf(e.stdout, "%d posting(s) %s (triaged with no backend reading)\n", len(keys), verb)
	return err
}

// retriageLedger is the ledger a retriage reads: a lock-free snapshot for a
// dry run, the locked ledger otherwise.
func (e *env) retriageLedger(cfg config.Config, dry bool) (ledger.Ledger, func(), error) {
	if !dry {
		app, err := buildApp(cfg, e.stateDir, e.stderr)
		if err != nil {
			return nil, nil, err
		}
		return app.Ledger, app.close, nil
	}
	snap, err := ledger.ReadSnapshot(filepath.Join(e.stateDir, "ledger"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) { // no ledger yet: nothing to return
		return nil, nil, fmt.Errorf("retriage: read the ledger: %w", err)
	}
	if snap.Torn {
		fmt.Fprintln(e.stderr, "vani: retriage: the ledger ends in a torn line; it was left out")
	}
	return snapshotLedger{recs: snap.Records}, func() {}, nil
}

// retriageDrops is --title-drops and --policy-drops. A real run holds the
// ledger lock, which is what serialises the pending-reopen file's writers.
func (e *env) retriageDrops(ctx context.Context, l ledger.Ledger, cfg config.Config, dry, titleOnly, refresh, asJSON bool) error {
	path := filepath.Join(e.stateDir, pipeline.PendingReopenFile)
	pending, err := pipeline.LoadPendingReopens(path, now())
	if err != nil {
		// A set that could not be read is never taken for an empty one, nor
		// overwritten.
		return fmt.Errorf("retriage: %w", err)
	}
	sc := reopenScope(cfg)
	sc.Defer, sc.Pending = sc.Watched, pending
	sc.TitleOnly, sc.RefreshUnjudged = titleOnly, refresh
	// Each drop's class as it stands before the reopen (eval.DropClass, the
	// audit's), so the reply says which kinds of drop the rules let back.
	rules := evalDropRules(cfg)
	classOf := map[posting.Key]string{}
	for _, r := range l.All() {
		if r.Status == posting.Dropped {
			classOf[r.Key] = eval.DropClass(r, rules)
		}
	}
	got, err := pipeline.RetriagePolicyDrops(ctx, l, now(), dropPolicy(cfg, cfg.Decide.OpenRouterName()), sc, dry)
	if !dry && len(got.Deferred) > 0 {
		if serr := pending.Save(path, now()); serr != nil {
			err = errors.Join(err, fmt.Errorf("retriage: %d reopen(s) were not recorded as pending: %w", len(got.Deferred), serr))
		}
	}
	keys := append(append(append(append([]posting.Key{}, got.Title...), got.Model...), got.Fresh...), got.Refresh...)
	classes := map[string]int{}
	for _, k := range keys {
		classes[classOf[k]]++
	}
	if asJSON {
		r := &retriageRunReply{DryRun: dry, TitleDrops: titleOnly, PolicyDrops: !titleOnly, Returned: len(keys), Keys: keys, Classes: classes,
			TitleReturned: len(got.Title), ModelReturned: len(got.Model), FreshReturned: len(got.Fresh), RefreshReturned: len(got.Refresh),
			Deferred: new(len(got.Deferred)), Reopened: new(got.Reopened()), AlreadyPending: &got.AlreadyPending, OverCap: &got.OverCap,
			Stale: &got.Stale, Unreadable: &got.Unreadable, UnreadableChecked: new(!dry)}
		if !titleOnly {
			r.Unjudged = &got.Unjudged
		}
		return e.reply(true, r, err, nil)
	}
	verb, why := "let back", "dropped on a rule the current drop policy no longer drops"
	if dry {
		verb = "would be let back"
	}
	if titleOnly {
		why = "dropped on a title the current rules no longer drop"
	}
	fmt.Fprintf(e.stdout, "%d posting(s) %s (%s)\n", got.Returned(), verb, why)
	fmt.Fprintf(e.stdout, "  waiting for their board to list them again: %d\n", len(got.Deferred))
	fmt.Fprintf(e.stdout, "  back now, with no board listing to wait for (inbox and page postings): %d\n", got.Reopened())
	fmt.Fprintf(e.stdout, "  title drops, back to live for the backends to read: %d\n", len(got.Title))
	if !titleOnly {
		fmt.Fprintf(e.stdout, "  model drops, back on their stored reading (no model asked): %d\n", len(got.Model))
		fmt.Fprintf(e.stdout, "  model drops whose title, location or description moved since the reading, back to live for a fresh read: %d\n", len(got.Fresh))
		if refresh {
			fmt.Fprintf(e.stdout, "  unjudged drops sent for a fresh read: %d\n", len(got.Refresh))
		}
		if got.Unjudged > 0 {
			fmt.Fprintf(e.stdout, "  model drops not judged, left dropped: %d (read under another model or question, or no calibrated reading from %q or lappi)\n", got.Unjudged, cfg.Decide.OpenRouterName())
		}
	}
	if got.AlreadyPending > 0 {
		fmt.Fprintf(e.stdout, "  already waiting for their board: %d\n", got.AlreadyPending)
	}
	if got.OverCap > 0 {
		fmt.Fprintf(e.stdout, "  left out, over a bound (%d pending at most, %d fresh reads a run): %d\n", pipeline.MaxPendingReopens, pipeline.MaxRefreshUnjudged, got.OverCap)
	}
	writeLeftDropped(e, got, dry)
	if len(classes) > 0 {
		names := make([]string, 0, len(classes))
		for c := range classes {
			names = append(names, c)
		}
		slices.SortFunc(names, func(a, b string) int {
			if c := cmp.Compare(classes[b], classes[a]); c != 0 {
				return c
			}
			return strings.Compare(a, b)
		})
		fmt.Fprintln(e.stdout, "  by drop class:")
		for _, c := range names {
			fmt.Fprintf(e.stdout, "    %d %s\n", classes[c], c)
		}
	}
	return err
}

// writeLeftDropped names the drops the current rules would let back that the
// reopen left dropped, and why, so a count of zero returned is never read as
// "every drop was checked".
func writeLeftDropped(e *env, got pipeline.PolicyReopen, dry bool) {
	if got.Stale > 0 {
		fmt.Fprintf(e.stdout, "  left dropped, on a board no longer watched: %d\n", got.Stale)
	}
	switch {
	case dry:
		fmt.Fprintln(e.stdout, "  stored descriptions: not checked in a dry run (it reads the event log, where every body is whole)")
	case got.Unreadable > 0:
		fmt.Fprintf(e.stdout, "  left dropped, stored description unreadable: %d\n", got.Unreadable)
	}
}

// reopenScope is what bounds a drop reopen besides the drop policy: the model
// id fresh triage records readings under (triageModelID), the slot options
// today's question offers (the shared fixture's and the job-category slot's),
// and the boards still on the watchlist.
func reopenScope(cfg config.Config) pipeline.ReopenScope {
	opts := jdtriage.SlotValues()
	extra := jobcategory.DecideSlot()
	opts[extra.Name] = extra.Options
	return pipeline.ReopenScope{ModelID: triageModelID(cfg), Options: opts, Watched: pipeline.WatchedBy(cfg.Watchlist)}
}

// retriageRunReply is `vani retriage --json`: the postings let back (or that
// would be, in a dry run). A failure part way keeps the ones it let back.
// The class counts split returned: title_returned, model_returned,
// fresh_returned (model drops whose text moved, back to live) and
// refresh_returned (unjudged drops sent for a fresh read). With
// --policy-drops or --title-drops, deferred are the ones waiting for their
// board's next listing (pending reopens), reopened the ones back now,
// already_pending the drops already waiting, over_cap the ones a bound left
// out, and stale and unreadable the drops the rules would let back that were
// left dropped (an unwatched board, an unreadable stored body; a dry run
// cannot check the bodies, so unreadable_checked is false there). unjudged
// counts model drops the current rules could not judge (--policy-drops), and
// classes the returned postings by drop class.
// Every count is present whenever the mode reports it, zero included, so a 0
// is told apart from a binary that does not count it.
type retriageRunReply struct {
	DryRun            bool          `json:"dry_run"`
	TitleDrops        bool          `json:"title_drops"`
	PolicyDrops       bool          `json:"policy_drops"`
	Returned          int           `json:"returned"`
	Keys              []posting.Key `json:"keys"`
	TitleReturned     int           `json:"title_returned"`
	ModelReturned     int           `json:"model_returned"`
	FreshReturned     int           `json:"fresh_returned"`
	RefreshReturned   int           `json:"refresh_returned"`
	Deferred          *int          `json:"deferred,omitempty"`
	Reopened          *int          `json:"reopened,omitempty"`
	AlreadyPending    *int          `json:"already_pending,omitempty"`
	OverCap           *int          `json:"over_cap,omitempty"`
	Unjudged          *int          `json:"unjudged,omitempty"`
	Stale             *int          `json:"stale,omitempty"`
	Unreadable        *int          `json:"unreadable,omitempty"`
	UnreadableChecked *bool         `json:"unreadable_checked,omitempty"`
	// Classes counts the returned postings by their drop class
	// (eval.DropClass) as it stood before the reopen.
	Classes map[string]int `json:"classes,omitempty"`
	failure
}
