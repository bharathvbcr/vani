package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// rescorePlan is what `vani rescore` would do to a ledger.
type rescorePlan struct {
	// Eligible are the Scored and Explained postings with no score and a
	// description to score (pipeline.Unscored and pipeline.Described).
	Eligible int
	// NoDescription are unscored postings with nothing to score; they keep
	// the ranker's unknown-coverage prior rather than a coverage near zero.
	NoDescription int
	// Digested are digested postings without a score. They are left alone
	// (already shown to the owner) and only counted.
	Digested int
	// ZeroOfZero are scored postings whose stored coverage is DevPrism's 0 of 0
	// (pipeline.ZeroOfZero); the repair marks them unknown without a DevPrism call.
	ZeroOfZero int
}

// repairUnknownCoverage marks every ZeroOfZero record in l as unknown coverage
// through repair and returns how many it wrote. It needs no DevPrism, so it runs
// even when scoring is off. A failure stops it with the count so far; rerunning
// is safe because a repaired record no longer matches.
func repairUnknownCoverage(ctx context.Context, l ledger.Ledger, repair func(context.Context, posting.Key) (bool, error)) (int, error) {
	n := 0
	for _, r := range l.All() {
		if !pipeline.ZeroOfZero(r) {
			continue
		}
		wrote, err := repair(ctx, r.Key)
		if err != nil {
			return n, err
		}
		if wrote {
			n++
		}
	}
	return n, nil
}

// planRescore counts what `vani rescore` would score in recs.
func planRescore(recs []ledger.Record) rescorePlan {
	var p rescorePlan
	for _, r := range recs {
		switch {
		case pipeline.Unscored(r) && pipeline.Described(r):
			p.Eligible++
		case pipeline.Unscored(r):
			p.NoDescription++
		case r.Status == posting.Digested && r.Annotations[ledger.AnnRank] == "":
			p.Digested++
		}
		if pipeline.ZeroOfZero(r) {
			p.ZeroOfZero++
		}
	}
	return p
}

// rescorable is the selection rescore advances.
func rescorable(r ledger.Record) bool { return pipeline.Unscored(r) && pipeline.Described(r) }

// rescoreReport is what one `vani rescore` did. Rescored is counted from the
// ledger afterwards, never from attempts, so it is exactly what was written.
type rescoreReport struct {
	Plan     rescorePlan
	Rescored int
	// Repaired is how many 0-of-0 coverages were marked unknown this run.
	Repaired int
	Advance  advanceReport
}

// errScoringOff refuses a rescore with nothing to score with.
var errScoringOff = errors.New("devprism.binary is empty: nothing to score with (set it, see docs/PHASE0-devprism.md)")

// rescore scores up to limit unscored postings (rescorable) through the same
// advanceAll the daily run uses, so its concurrency (advance_concurrency),
// cancellation and stop rules hold: maxConsecutiveFailed DevPrism failures in
// a row, or a pass where none was scored, fail it. Freshest postings go first.
func (w *wired) rescore(ctx context.Context, limit int) (rescoreReport, error) {
	r := &pipeline.Runner{Ledger: w.led}
	repaired, err := repairUnknownCoverage(ctx, w.led, r.RepairUnknownCoverage)
	if err != nil {
		return rescoreReport{Repaired: repaired}, err
	}
	if w.cfg.DevPrism.Binary == "" {
		return rescoreReport{Repaired: repaired}, errScoringOff
	}
	if err := w.scoring(r); err != nil {
		return rescoreReport{Repaired: repaired}, err
	}
	rep, err := rescoreWith(ctx, w.led, limit, w.cfg.AdvanceWorkers(), r.Rescore, w.warnf)
	rep.Repaired = repaired
	return rep, err
}

// rescoreWith is rescore over any ledger and rescore function, for tests.
func rescoreWith(ctx context.Context, l ledger.Ledger, limit, workers int, rescore func(context.Context, posting.Key) error, warnf func(string, ...any)) (rescoreReport, error) {
	recs := l.All()
	rep := rescoreReport{Plan: planRescore(recs)}
	var keys []posting.Key
	for _, r := range recs {
		if rescorable(r) {
			keys = append(keys, r.Key)
		}
	}
	// advanceAll's cap line names advance_per_run and the next daily run, which
	// is wrong here (--limit, and the run does not rescore); the command prints
	// how many are still unscored instead.
	quiet := func(format string, a ...any) {
		if !strings.HasPrefix(format, "advance cap ") {
			warnf(format, a...)
		}
	}
	adv, err := advanceAll(ctx, recs, nil, limit, workers, nil, rescore, quiet, withTodo(rescorable))
	rep.Advance = adv
	for _, k := range keys {
		if r, ok := l.Get(k); ok && r.Annotations[ledger.AnnRank] != "" {
			rep.Rescored++
		}
	}
	return rep, err
}

// cmdRescore scores postings that went past the scoring stage while
// devprism.binary was empty. They keep their status and explanation; only the
// score annotations are added. It calls DevPrism, never a model, and spends
// nothing. --dry-run reads the ledger without its lock and changes nothing.
func (e *env) cmdRescore(args []string) error {
	flags := e.newFlags("rescore")
	dry := flags.Bool("dry-run", false, "count what would be scored; read the ledger only, change nothing")
	limit := flags.Int("limit", 0, "score at most N postings this time (default advance_per_run)")
	if err := parse(flags, args); err != nil {
		return err
	}
	if *limit < 0 || *limit > config.MaxAdvancePerRun {
		return usagef("--limit %d must be within 0..%d (0 means advance_per_run)", *limit, config.MaxAdvancePerRun)
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	n := *limit
	if n == 0 {
		n = cfg.AdvanceLimit()
	}
	if *dry {
		snap, err := ledger.ReadSnapshot(filepath.Join(e.stateDir, "ledger"))
		if err != nil && !errors.Is(err, fs.ErrNotExist) { // no ledger yet: nothing to rescore
			return fmt.Errorf("rescore: read the ledger: %w", err)
		}
		if snap.Torn {
			fmt.Fprintln(e.stderr, "vani: rescore: the ledger ends in a torn line; it was left out")
		}
		p := planRescore(snap.Records)
		fmt.Fprintf(e.stdout, "%d posting(s) would be rescored (at most %d this time)\n", p.Eligible, n)
		fmt.Fprintf(e.stdout, "%d posting(s) with a 0-of-0 coverage would be marked unknown (no DevPrism call)\n", p.ZeroOfZero)
		e.printRescoreLeftAlone(p)
		if cfg.DevPrism.Binary == "" {
			fmt.Fprintf(e.stdout, "devprism.binary is empty: a real rescore would refuse\n")
		}
		return nil
	}
	app, err := buildApp(cfg, e.stateDir, e.stderr)
	if err != nil {
		return err
	}
	defer app.close()
	if app.Rescore == nil {
		return errors.New("rescore is not available in this build")
	}
	ctx, cancel := signalContext()
	defer cancel()
	rep, err := app.Rescore(ctx, n)
	if rep.Repaired > 0 {
		fmt.Fprintf(e.stdout, "%d posting(s) with a 0-of-0 coverage marked unknown (no DevPrism call)\n", rep.Repaired)
	}
	fmt.Fprintf(e.stdout, "%d posting(s) rescored, %d still unscored\n", rep.Rescored, rep.Plan.Eligible-rep.Rescored)
	e.printRescoreLeftAlone(rep.Plan)
	return err
}

func (e *env) printRescoreLeftAlone(p rescorePlan) {
	if p.NoDescription > 0 {
		fmt.Fprintf(e.stdout, "%d unscored posting(s) have no description; left unscored\n", p.NoDescription)
	}
	if p.Digested > 0 {
		fmt.Fprintf(e.stdout, "%d digested posting(s) have no score; left alone\n", p.Digested)
	}
}
