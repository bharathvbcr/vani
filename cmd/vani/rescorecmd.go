package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sync"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// rescorePlan is what `vani rescore` would do to a ledger.
type rescorePlan struct {
	// Eligible are the Scored and Explained postings with no score and a
	// description to score (pipeline.Unscored and pipeline.Described).
	Eligible int `json:"eligible"`
	// NoDescription are unscored postings with nothing to score; they keep
	// the ranker's unknown-coverage prior rather than a coverage near zero.
	NoDescription int `json:"no_description"`
	// Digested are digested postings without a score. They are left alone
	// (already shown to the owner) and only counted.
	Digested int `json:"digested"`
	// ZeroOfZero are scored postings whose stored coverage is DevPrism's 0 of 0
	// (pipeline.ZeroOfZero); the repair marks them unknown without a DevPrism call.
	ZeroOfZero int `json:"zero_of_zero"`
	// Unknown are live postings marked gap.coverage_unknown (DevPrism found no
	// must-haves). Disjoint from Digested, which counts only postings with
	// neither a rank nor that marker, and from Eligible and NoDescription.
	Unknown int `json:"unknown"`
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
		case r.Status == posting.Digested && r.Annotations[ledger.AnnRank] == "" && !ledger.CoverageUnknown(r):
			p.Digested++
		}
		if ledger.CoverageUnknown(r) && r.Status != posting.Dead && r.Status != posting.Dropped {
			p.Unknown++
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
	Advance  pipeline.AdvanceReport
}

// errScoringOff refuses a rescore with nothing to score with.
var errScoringOff = errors.New("devprism.binary is empty: nothing to score with (set it, see docs/PHASE0-devprism.md)")

// rescore scores up to limit unscored postings (rescorable) through the same
// pipeline.Advance the daily run uses, so its concurrency (advance_concurrency),
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
	// Count only what this pass scored: a key that was rescorable before the
	// wrapped call and carries a rank, or the coverage-unknown marker a
	// NoMustHaves answer writes in place of one, after it. A posting a
	// concurrent writer dropped mid-pass, or one rescore skipped, carries
	// neither and is not counted.
	var mu sync.Mutex
	scored := 0
	counted := func(ctx context.Context, k posting.Key) error {
		before, _ := l.Get(k)
		if err := rescore(ctx, k); err != nil {
			return err
		}
		if r, ok := l.Get(k); ok && rescorable(before) && (r.Annotations[ledger.AnnRank] != "" || ledger.CoverageUnknown(r)) {
			mu.Lock()
			scored++
			mu.Unlock()
		}
		return nil
	}
	// The kindRescore plan leaves out the cap line, which names advance_per_run
	// and the next daily run: wrong here (--limit, and the run does not
	// rescore). The command prints how many are still unscored instead.
	adv, err := pipeline.Advance(ctx, baseAdvancePlan(kindRescore, limit, workers, warnf), pipeline.AdvanceDeps{Records: recs, Advance: counted})
	rep.Advance = adv
	rep.Rescored = scored
	return rep, err
}

// coverageRescoreReport is what one `vani rescore --coverage-unknown` did. The
// counts come from the ledger after each answer, never from attempts.
type coverageRescoreReport struct {
	// Eligible are the postings planCoverage counted when the pass started.
	Eligible int `json:"eligible"`
	// Asked are the postings DevPrism answered about: Measured now carry a
	// coverage, StillUnknown were answered with no must-have again and keep the
	// marker (nothing was written for them).
	Asked        int `json:"asked"`
	Measured     int `json:"measured"`
	StillUnknown int `json:"still_unknown"`
	// Failed hit a DevPrism error and are unchanged; NotReached were eligible
	// and not attempted (the --limit, or a stop). Both stay eligible.
	Failed     int                    `json:"failed"`
	NotReached int                    `json:"not_reached"`
	Advance    pipeline.AdvanceReport `json:"-"`
}

// planCoverage counts the postings `vani rescore --coverage-unknown` would ask
// DevPrism about again (pipeline.CoverageRescorable).
func planCoverage(recs []ledger.Record) int {
	n := 0
	for _, r := range recs {
		if pipeline.CoverageRescorable(r) {
			n++
		}
	}
	return n
}

// rescoreCoverage asks DevPrism again about up to limit postings it found no
// must-have in (pipeline.Runner.RescoreUnknown), through the same
// pipeline.Advance as the daily run, so its concurrency, cancellation and stop
// rules hold. It does not run the 0-of-0 repair (the default mode's).
func (w *wired) rescoreCoverage(ctx context.Context, limit int) (coverageRescoreReport, error) {
	if w.cfg.DevPrism.Binary == "" {
		return coverageRescoreReport{}, errScoringOff
	}
	r := &pipeline.Runner{Ledger: w.led}
	if err := w.scoring(r); err != nil {
		return coverageRescoreReport{}, err
	}
	return rescoreCoverageWith(ctx, w.led, limit, w.cfg.AdvanceWorkers(), r.RescoreUnknown, w.warnf)
}

// rescoreCoverageWith is rescoreCoverage over any ledger and pass function, for
// tests. A posting DevPrism still finds no must-have in stays eligible, so a
// later pass with a small --limit asks the freshest ones again first: the
// report's NotReached says how many a pass did not get to.
func rescoreCoverageWith(ctx context.Context, l ledger.Ledger, limit, workers int, ask func(context.Context, posting.Key) (bool, error), warnf func(string, ...any)) (coverageRescoreReport, error) {
	recs := l.All()
	rep := coverageRescoreReport{Eligible: planCoverage(recs)}
	var mu sync.Mutex
	counted := func(ctx context.Context, k posting.Key) error {
		wrote, err := ask(ctx, k)
		if err != nil {
			return err
		}
		measured := false
		if r, ok := l.Get(k); ok && wrote && !ledger.CoverageUnknown(r) && r.Annotations[ledger.AnnCoverage] != "" {
			measured = true
		}
		mu.Lock()
		rep.Asked++
		if measured {
			rep.Measured++
		}
		mu.Unlock()
		return nil
	}
	adv, err := pipeline.Advance(ctx, baseAdvancePlan(kindRescoreCoverage, limit, workers, warnf), pipeline.AdvanceDeps{Records: recs, Advance: counted})
	rep.Advance = adv
	rep.StillUnknown = rep.Asked - rep.Measured
	rep.Failed = adv.Failed
	rep.NotReached = max(0, rep.Eligible-adv.Attempted)
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
	coverage := flags.Bool("coverage-unknown", false, "ask DevPrism again about shown postings it found no must-have in "+
		"(scored, explained or digested, with the coverage-unknown marker): after a DevPrism upgrade, the ones it now reads "+
		"get a measured coverage in place; status, explanation and drops are kept. No 0-of-0 repair")
	asJSON := e.jsonFlag(flags, "one JSON object on stdout")
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
		if *coverage {
			c := coverageRescoreReport{Eligible: planCoverage(snap.Records)}
			if *asJSON {
				return e.reply(true, rescoreReply{DryRun: true, Limit: n, Coverage: &c, ScoringOff: cfg.DevPrism.Binary == ""}, nil, nil)
			}
			fmt.Fprintf(e.stdout, "%d posting(s) with unknown coverage would be asked again (at most %d this time)\n", c.Eligible, n)
			if cfg.DevPrism.Binary == "" {
				fmt.Fprintf(e.stdout, "devprism.binary is empty: a real rescore would refuse\n")
			}
			return nil
		}
		p := planRescore(snap.Records)
		if *asJSON {
			return e.reply(true, rescoreReply{DryRun: true, Limit: n, Plan: p, ScoringOff: cfg.DevPrism.Binary == ""}, nil, nil)
		}
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
	if *coverage {
		if app.RescoreCoverage == nil {
			return errors.New("rescore --coverage-unknown is not available in this build")
		}
		c, err := app.RescoreCoverage(ctx, n)
		if *asJSON {
			return e.reply(true, &rescoreReply{Limit: n, Coverage: &c, ScoringOff: cfg.DevPrism.Binary == ""}, err, nil)
		}
		fmt.Fprintf(e.stdout, "%d posting(s) with unknown coverage asked again: %d measured, %d still unknown (left as they were)\n",
			c.Asked, c.Measured, c.StillUnknown)
		if c.Failed > 0 || c.NotReached > 0 {
			fmt.Fprintf(e.stdout, "%d failed and %d not reached of %d eligible: they stay unknown; run it again\n", c.Failed, c.NotReached, c.Eligible)
		}
		return err
	}
	rep, err := app.Rescore(ctx, n)
	if *asJSON {
		return e.reply(true, &rescoreReply{Limit: n, Plan: rep.Plan, Rescored: rep.Rescored, StillUnscored: rep.Plan.Eligible - rep.Rescored,
			Repaired: rep.Repaired, ScoringOff: cfg.DevPrism.Binary == ""}, err, nil)
	}
	if rep.Repaired > 0 {
		fmt.Fprintf(e.stdout, "%d posting(s) with a 0-of-0 coverage marked unknown (no DevPrism call)\n", rep.Repaired)
	}
	fmt.Fprintf(e.stdout, "%d posting(s) rescored, %d still unscored\n", rep.Rescored, rep.Plan.Eligible-rep.Rescored)
	e.printRescoreLeftAlone(rep.Plan)
	return err
}

// rescoreReply is `vani rescore --json`. A dry run fills Plan alone; a real one
// also what it scored and repaired, and a failure part way keeps those.
type rescoreReply struct {
	DryRun        bool        `json:"dry_run"`
	Limit         int         `json:"limit"`
	Plan          rescorePlan `json:"plan"`
	ScoringOff    bool        `json:"scoring_off"`
	Rescored      int         `json:"rescored"`
	StillUnscored int         `json:"still_unscored"`
	Repaired      int         `json:"repaired"`
	// Coverage is what --coverage-unknown planned or did; absent otherwise.
	Coverage *coverageRescoreReport `json:"coverage,omitempty"`
	failure
}

func (e *env) printRescoreLeftAlone(p rescorePlan) {
	if p.NoDescription > 0 {
		fmt.Fprintf(e.stdout, "%d unscored posting(s) have no description; left unscored\n", p.NoDescription)
	}
	if p.Digested > 0 {
		fmt.Fprintf(e.stdout, "%d digested posting(s) have no score; left alone\n", p.Digested)
	}
	if p.Unknown > 0 {
		fmt.Fprintf(e.stdout, "%d posting(s) have unknown coverage: DevPrism found no must-haves\n", p.Unknown)
	}
}
