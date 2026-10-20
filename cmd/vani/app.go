package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/daemon"
	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/notify"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// App is everything the long-running commands need from the rest of Vani.
// The lead wires real implementations in buildApp.
type App struct {
	// Ledger is the posting ledger (folded state plus Append).
	Ledger ledger.Ledger
	// RunOnce runs the whole pipeline once: list boards, triage, score,
	// explain. It returns an error only when the run as a whole failed.
	RunOnce func(ctx context.Context) error
	// RunReport returns what the last RunOnce reported about itself (the
	// advance phase, backend errors) for the run record. Nil means none.
	RunReport func() runDetail
	// Reload hands the app the watchlist and allowed hosts the config file holds
	// now, before each daily run. Nil means the app keeps its own.
	Reload func(watchlist []sources.Board, allowedHosts []string)
	// PollInbox folds the jobs the extension spooled (internal/inbox) and
	// advances the new ones. The daemon calls it between daily runs, never
	// while a run is going. Nil means no polling.
	PollInbox func(ctx context.Context) error
	// Rescore scores up to limit postings that went past the scoring stage
	// without a score (vani rescore). Nil means not available.
	Rescore func(ctx context.Context, limit int) (rescoreReport, error)
	// Tune sets what the next RunOnce does in its advance phase: advance caps
	// it (0 keeps advance_per_run) and listOnly skips it. Every run sets it,
	// so a manual run's overrides never reach the next scheduled one. Nil means
	// the app cannot be tuned, and a run that asks for it fails.
	Tune func(advance int, listOnly bool)
	// Notifier announces a finished digest. Nil means no notification.
	Notifier notify.Notifier
	// Closer releases resources (the ledger file, child processes). May be nil.
	Closer func() error
}

func (a *App) close() {
	if a != nil && a.Closer != nil {
		_ = a.Closer()
	}
}

// buildApp constructs the real components from the config (wire.go). It is a
// variable so CLI tests can substitute an App built on a temp ledger.
var buildApp = wireApp

// dailyJob is what the daemon and run-once execute: run the pipeline, then
// publish the digest. Each run is written to the run record (runrecord.go) when
// it starts and again when it ends, however it ends. trigger says who started
// it (triggerSchedule, triggerManual) and opts are a manual run's overrides;
// the daemon's runs take none.
func (e *env) dailyJob(app *App, cfg config.Config, trigger string, opts runOptions) daemon.Job {
	return func(ctx context.Context) error {
		rec := runRecord{PID: os.Getpid(), StartedAt: now(), Outcome: outcomeRunning, Trigger: trigger, Options: opts.recorded()}
		if app.Ledger != nil {
			rec.CountsBefore = statusCounts(app.Ledger.All())
		}
		e.saveRunRecord(rec)
		stage, err := stagePipeline, ledgerBroken(app.Ledger)
		if err == nil {
			err = tuneRun(app, opts)
		}
		if err == nil {
			stage, err = e.dailySteps(ctx, app, cfg, opts, &rec)
			if berr := ledgerBroken(app.Ledger); berr != nil {
				err = errors.Join(err, berr)
			}
		}
		rec.FinishedAt = now()
		if app.RunReport != nil {
			rec.runDetail = app.RunReport()
		}
		if app.Ledger != nil {
			rec.CountsAfter = statusCounts(app.Ledger.All())
		}
		switch {
		case err == nil:
			rec.Outcome = outcomeOK
		case errors.Is(err, context.Canceled):
			rec.Outcome, rec.Stage, rec.Error = outcomeCancelled, stage, runErrorText(err)
		default:
			rec.Outcome, rec.Stage, rec.Error = outcomeFailed, stage, runErrorText(err)
			rec.Cause = runCause(err)
		}
		e.saveRunRecord(rec)
		e.announceFailure(ctx, app, rec)
		return err
	}
}

// ledgerBroken reports a ledger a failed write left unusable, as a
// daemon.ErrFatal: the daemon holds one ledger for its whole life, so without
// exiting it would fail every later run until someone restarted it.
func ledgerBroken(l ledger.Ledger) error {
	b, ok := l.(interface{ Broken() error })
	if !ok {
		return nil
	}
	if err := b.Broken(); err != nil {
		return fmt.Errorf("%w: %w", daemon.ErrFatal, err)
	}
	return nil
}

// dailySteps is the run itself; stage names where it stopped when it fails.
//
// The watchlist and allowed hosts are re-read from the config file first, so a
// board saved while the daemon runs is searched from this run on. If the file
// cannot be read the run goes on with the previous values and the failure is
// joined into the run's error: the run record says the config was stale, and
// the run is not reported as a clean one.
//
// A manual run's board selection (opts.Boards) is applied to the watchlist the
// reload produced, never before it, or the reload would put every board back.
// A selection the watchlist no longer satisfies stops the run before it lists
// anything.
func (e *env) dailySteps(ctx context.Context, app *App, cfg config.Config, opts runOptions, rec *runRecord) (stage string, err error) {
	reloadErr := e.reloadBoards(app, cfg, opts.Boards)
	if errors.Is(reloadErr, errBoardSelection) {
		return stagePipeline, reloadErr
	}
	stage, err = e.runAndPublish(ctx, app, cfg, opts, rec)
	return stage, errors.Join(reloadErr, err)
}

// reloadBoards applies the config file's watchlist and allowed hosts to app,
// cut to the boards only names when it names any. When the file cannot be read
// the selection is cut from cfg, the config this process started with.
func (e *env) reloadBoards(app *App, cfg config.Config, only []string) error {
	if app.Reload == nil {
		if len(only) > 0 {
			return fmt.Errorf("%w: this app cannot limit a run to some boards", errBoardSelection)
		}
		return nil
	}
	fresh, _, err := config.LoadWithDeprecations(e.configPath)
	var stale error
	if err != nil {
		stale = fmt.Errorf("%w; this run uses the previous watchlist and allowed_hosts: %w", errConfigStale, err)
		if len(only) == 0 {
			return stale
		}
		fresh = cfg
	}
	watchlist := fresh.Watchlist
	if len(only) > 0 {
		if watchlist, err = selectBoards(fresh.Watchlist, only); err != nil {
			return errors.Join(stale, err)
		}
	}
	app.Reload(watchlist, fresh.AllowedHosts)
	return stale
}

// tuneRun hands the run's advance overrides to the app, and resets them for a
// run that has none.
func tuneRun(app *App, opts runOptions) error {
	if app.Tune == nil {
		if opts.tunes() {
			return errors.New("this app cannot change a run's advance phase (--advance, --list-only)")
		}
		return nil
	}
	app.Tune(opts.Advance, opts.ListOnly)
	return nil
}

// runAndPublish runs the pipeline and publishes the digest. Between the two it
// warms the knowledge-base scores the digest orders by (semanticwire.go), with
// what the pipeline's own warm before its advance phase left of
// semantic.warm_per_run; that never fails the run, and rec carries what it did.
// rec.DigestRoles is the size of the digest it wrote (nil when it wrote none).
// With opts.NoDigest it stops after the pipeline: no warm, no digest.
func (e *env) runAndPublish(ctx context.Context, app *App, cfg config.Config, opts runOptions, rec *runRecord) (stage string, err error) {
	if app.RunOnce == nil {
		return stagePipeline, errors.New("app has no pipeline runner")
	}
	if err := app.RunOnce(ctx); err != nil {
		return stagePipeline, fmt.Errorf("pipeline: %w", err)
	}
	if opts.NoDigest {
		fmt.Fprintln(e.stdout, "digest: skipped (--no-digest); the run's roles wait for the next digest")
		return stagePipeline, nil
	}
	loc, err := scheduleLoc(cfg)
	if err != nil {
		return stageDigest, err
	}
	if cfg.SemanticOn() && app.Ledger != nil && ctx.Err() == nil {
		mk, _ := marks.ReadMarks(marks.Dir(e.stateDir)) // unreadable marks: the kept postings only
		// semantic.warm_per_run is one budget for the run: what the warm before
		// the advance phase embedded is not spent twice (preAdvanceBudget).
		limit := cfg.SemanticWarmLimit()
		if app.RunReport != nil {
			if pre := app.RunReport().SemanticPre; pre != nil {
				limit = max(limit-pre.Scored, 0)
			}
		}
		if limit > 0 {
			rep := warmSemantic(ctx, cfg, e.stateDir, app.Ledger.All(), mk, limit)
			rec.Semantic = &rep
			e.logSemantic(rep)
		} else {
			rec.Semantic = &semanticReport{Ran: true, Note: "kept postings not warmed: semantic.warm_per_run was spent before the advance phase"}
			e.logSemantic(*rec.Semantic)
		}
	}
	dopts, _ := digestOptions(cfg, e.stateDir) // the warm's note already said why there are no scores
	rec.DigestRoles, err = e.publishDigest(ctx, app, now().In(loc), loc, dopts)
	return stageDigest, err
}

// saveRunRecord writes the record; a failure is a warning, never a failed run:
// the run's work is in the ledger either way.
func (e *env) saveRunRecord(r runRecord) {
	if err := writeRunRecord(e.stateDir, r); err != nil {
		fmt.Fprintf(e.stderr, "vani: could not record the run: %v\n", err)
	}
}

func scheduleLoc(cfg config.Config) (*time.Location, error) {
	_, _, loc, err := cfg.Schedule.Parsed()
	return loc, err
}

// publishDigest writes the digest for date and marks the included postings
// Digested, in that order: a crash between the two re-lists them tomorrow
// rather than losing them.
//
// The digest honours the Jobs marks (marks only, never notes). When they
// cannot be read the digest is held back: no file and no Digested event, and
// the error is returned (errDigestHeldBack; dailyJob announces it). Listing
// everything instead would show, and then consume, roles the user hid. The
// pipeline's results are already in the ledger, so the roles are listed once
// the store reads again.
//
// roles is how many roles the digest listed, set once the digest is written
// (0 when nothing was new) and nil when it was not. A failure marking them
// Digested still returns roles: the digest exists, and the run record says so.
func (e *env) publishDigest(ctx context.Context, app *App, date time.Time, loc *time.Location, opts digest.Options) (roles *int, err error) {
	if app.Ledger == nil {
		return nil, errors.New("app has no ledger")
	}
	dir := marks.Dir(e.stateDir)
	m, err := marks.ReadMarks(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: read the Jobs marks in %s: %w", errDigestHeldBack, dir, err)
	}
	recs := app.Ledger.All()
	var adv *pipeline.AdvanceReport
	if app.RunReport != nil {
		adv = app.RunReport().Advance
	}
	counts := countsFor(recs, date, loc)
	if adv != nil {
		counts.CouldNotCheck = uncheckedCauses(adv.FailOpen)
	}
	path, keys, err := digest.WriteWith(e.stateDir, date, recs, m, counts, opts)
	if err != nil {
		return nil, err
	}
	n := len(keys)
	var errs []error
	for _, k := range keys {
		ev := ledger.Event{V: 1, At: now(), Key: k, Status: posting.Digested, Reason: "digest " + date.Format("2006-01-02")}
		if err := app.Ledger.Append(ctx, ev); err != nil {
			errs = append(errs, fmt.Errorf("mark %s digested: %w", k, err))
		}
	}
	fmt.Fprintf(e.stdout, "digest: %s (%d roles)\n", path, len(keys))
	if len(keys) > 0 && app.Notifier != nil {
		if err := app.Notifier.Notify(ctx, "Vani digest", digestBody(len(keys), adv)); err != nil {
			fmt.Fprintf(e.stderr, "vani: notification failed: %v\n", err)
		}
	}
	return &n, errors.Join(errs...)
}

// countsFor derives the footer counts for the calendar day of date in loc from
// ledger state: postings first seen that day, and postings dropped or dead
// whose last event was that day.
func countsFor(recs []ledger.Record, date time.Time, loc *time.Location) digest.Counts {
	y, m, d := date.In(loc).Date()
	same := func(t time.Time) bool {
		ty, tm, td := t.In(loc).Date()
		return ty == y && tm == m && td == d
	}
	var c digest.Counts
	for _, r := range recs {
		if same(r.FirstSeen) {
			c.New++
		}
		switch r.Status {
		case posting.Live:
			c.AwaitingTriage++
		case posting.Dropped:
			if same(r.LastEvent) {
				c.Dropped++
			}
		case posting.Dead:
			if same(r.LastEvent) {
				c.Dead++
			}
		}
	}
	return c
}

func (e *env) cmdDigest(args []string) error {
	fs := e.newFlags("digest")
	dateStr := fs.String("date", "", "digest date YYYY-MM-DD (default: today in the schedule zone)")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	loc, err := scheduleLoc(cfg)
	if err != nil {
		return err
	}
	date := now().In(loc)
	if *dateStr != "" {
		date, err = time.ParseInLocation("2006-01-02", *dateStr, loc)
		if err != nil {
			return usagef("--date %q is not YYYY-MM-DD", *dateStr)
		}
	}
	app, err := buildApp(cfg, e.stateDir, e.stderr)
	if err != nil {
		return err
	}
	defer app.close()
	ctx, stop := signalContext()
	defer stop()
	opts, warning := digestOptions(cfg, e.stateDir)
	if warning != "" {
		fmt.Fprintf(e.stderr, "vani: digest: %s\n", warning)
	}
	_, err = e.publishDigest(ctx, app, date, loc, opts)
	return err
}

func (e *env) printStatus(recs []ledger.Record, cfg config.Config) error {
	by := statusCounts(recs)
	statuses := make([]string, 0, len(by))
	for s := range by {
		statuses = append(statuses, s)
	}
	sort.Strings(statuses)
	fmt.Fprintf(e.stdout, "boards: %d\npostings: %d\n", len(cfg.Watchlist), len(recs))
	for _, s := range statuses {
		fmt.Fprintf(e.stdout, "  %-10s %d\n", s, by[s])
	}
	fmt.Fprintf(e.stdout, "schedule: %s %s\nstate dir: %s\n", cfg.Schedule.DailyAt, cfg.Schedule.Timezone, e.stateDir)
	return nil
}
