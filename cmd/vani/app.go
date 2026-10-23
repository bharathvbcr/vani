package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
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
	// Reload hands the app the config the file holds now, before each run. Nil
	// means the app keeps its own.
	Reload func(cfg config.Config)
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
//
// A panic in the run's own goroutine is recovered (guardedRun) and recorded as a
// failed run, so the record never stays "running" and the failure is announced.
// A panic in a goroutine the pipeline started cannot be recovered here and
// ends the process, as any unrecovered goroutine panic does; launchd's
// KeepAlive restarts the daemon, and the record stays "running" until the next
// run replaces it.
func (e *env) dailyJob(app *App, cfg config.Config, trigger string, opts runOptions) daemon.Job {
	return func(ctx context.Context) error {
		e.keepInterruptedRun()
		rec := runRecord{PID: os.Getpid(), StartedAt: now(), Outcome: outcomeRunning, Trigger: trigger, Options: opts.recorded(), Log: e.runLog}
		if app.Ledger != nil {
			rec.CountsBefore = statusCounts(app.Ledger.All())
		}
		e.saveRunRecord(rec)
		stage, err := e.guardedRun(ctx, app, cfg, opts, &rec)
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
		case errors.Is(err, context.Canceled) && !errors.Is(err, daemon.ErrFatal) && !errors.Is(err, errRunPanic):
			// Someone stopped the run. A ledger that failed, or a panic, while it
			// was being stopped is still a failure and says so below.
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

// maxPanicText bounds a recovered panic's value where it is recorded or logged.
const maxPanicText = 200

// panicText is a recovered panic value as one bounded, control-free line. The
// value can carry untrusted text (a JD in an index error or a parse failure),
// so no recover site records it verbatim; like every other logged error
// (oneLine(err.Error(), 200)) the full value is kept nowhere.
func panicText(p any) string { return oneLine(fmt.Sprint(p), maxPanicText) }

// maxPanicStack bounds the stack a recovered run panic prints.
const maxPanicStack = 4096

// guardedRun is the run between its first and last record write. A panic comes
// back as an error wrapping errRunPanic, joined with whatever the run had
// already failed with, and the stack goes to stderr.
func (e *env) guardedRun(ctx context.Context, app *App, cfg config.Config, opts runOptions, rec *runRecord) (stage string, err error) {
	stage = stagePipeline
	defer func() {
		if p := recover(); p != nil {
			stack := debug.Stack()
			if len(stack) > maxPanicStack {
				stack = stack[:maxPanicStack]
			}
			fmt.Fprintf(e.stderr, "vani: the run panicked: %s\n%s\n", panicText(p), stack)
			err = errors.Join(err, fmt.Errorf("%w: %s", errRunPanic, panicText(p)))
			if berr := ledgerBroken(app.Ledger); berr != nil {
				err = errors.Join(err, berr)
			}
		}
	}()
	if err = ledgerBroken(app.Ledger); err != nil {
		return stage, err
	}
	if err = tuneRun(app, opts); err != nil {
		return stage, err
	}
	stage, err = e.dailySteps(ctx, app, cfg, opts, rec)
	if berr := ledgerBroken(app.Ledger); berr != nil {
		err = errors.Join(err, berr)
	}
	return stage, err
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
// The config is re-read from the file first (reloadConfig), so a board or an
// exclusion saved while the daemon runs applies from this run on. If the file
// cannot be read the run goes on with the previous config and the failure is
// joined into the run's error: the run record says the config was stale, and
// the run is not reported as a clean one.
//
// A manual run's board selection (opts.Boards) is applied to the watchlist the
// reload produced, never before it, or the reload would put every board back.
// A selection the watchlist no longer satisfies stops the run before it lists
// anything.
func (e *env) dailySteps(ctx context.Context, app *App, cfg config.Config, opts runOptions, rec *runRecord) (stage string, err error) {
	cfg, reloadErr := e.reloadConfig(app, cfg, opts.Boards)
	if errors.Is(reloadErr, errBoardSelection) {
		return stagePipeline, reloadErr
	}
	stage, err = e.runAndPublish(ctx, app, cfg, opts, rec)
	return stage, errors.Join(reloadErr, err)
}

// reloadConfig is the one place a run's config is refreshed: it reads the whole
// config file, hands it to app (App.Reload), and returns what the run reads, so
// edits saved while the daemon runs (the Mac app's exclusions, a board, a
// weights file) apply from the next run on.
//
// The schedule is fixed for the process's life (daemon.Run holds it), as are
// the state dir and the ledger path, which no config key sets; a changed
// schedule keeps the running one and says so. When the file cannot be read or
// is invalid, the run goes on with the last config a run applied (cfg, the
// config the process started with, before the first) and the error is
// errConfigStale. The watchlist is cut to the boards only names when it names
// any; the cut is this run's and is never remembered.
func (e *env) reloadConfig(app *App, cfg config.Config, only []string) (config.Config, error) {
	prev := e.lastApplied(cfg)
	if app.Reload == nil {
		if len(only) > 0 {
			return prev, fmt.Errorf("%w: this app cannot limit a run to some boards", errBoardSelection)
		}
		return prev, nil
	}
	fresh, old, err := config.LoadWithDeprecations(e.configPath)
	e.warnDeprecated(old)
	var stale error
	if err != nil {
		stale = fmt.Errorf("%w; this run uses the previous watchlist, allowed_hosts and policy: %w", errConfigStale, err)
		if len(only) == 0 {
			return prev, stale
		}
		fresh = prev
	}
	for _, p := range processPinned {
		if p.differs(fresh, prev) {
			fmt.Fprintf(e.stderr, "vani: config: %s changed; it applies after the daemon restarts\n", p.name)
			p.pin(&fresh, prev)
		}
	}
	e.setApplied(fresh)
	if len(only) > 0 {
		if fresh.Watchlist, err = selectBoards(fresh.Watchlist, only); err != nil {
			return prev, errors.Join(stale, err)
		}
	}
	app.Reload(fresh)
	return fresh, stale
}

// processPinned lists the config fields fixed for the process's life: the
// daemon's schedule and the DevPrism binary and tools hash its scorer is
// started with. reloadConfig keeps the running value of each and says so; add a
// process-lifetime field here and nowhere else.
var processPinned = []struct {
	name    string
	differs func(fresh, prev config.Config) bool
	pin     func(fresh *config.Config, prev config.Config)
}{
	{"the schedule",
		func(f, p config.Config) bool { return f.Schedule != p.Schedule },
		func(f *config.Config, p config.Config) { f.Schedule = p.Schedule }},
	{"devprism.binary or devprism.tools_hash",
		func(f, p config.Config) bool { return f.DevPrism != p.DevPrism },
		func(f *config.Config, p config.Config) { f.DevPrism = p.DevPrism }},
}

// lastApplied is the config the last reload left, or def before the first.
func (e *env) lastApplied(def config.Config) config.Config {
	e.appliedMu.Lock()
	defer e.appliedMu.Unlock()
	if e.applied == nil {
		return def
	}
	return *e.applied
}

func (e *env) setApplied(cfg config.Config) {
	e.appliedMu.Lock()
	defer e.appliedMu.Unlock()
	e.applied = &cfg
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
			recs := withMarkedText(app.Ledger, app.Ledger.All(), mk)
			rep := warmSemanticRun(ctx, cfg, e.stateDir, recs, mk, limit)
			rep.noteUnreadable(unreadableMarked(recs, mk))
			rec.Semantic = &rep
			e.logSemantic(rep)
		} else {
			rec.Semantic = &semanticReport{Ran: true, Note: "kept postings not warmed: semantic.warm_per_run was spent before the advance phase"}
			e.logSemantic(*rec.Semantic)
		}
	}
	dopts, warning := digestOptions(cfg, e.stateDir)
	if warning != "" {
		rec.DigestWarning = warning
		fmt.Fprintf(e.stderr, "vani: digest: %s\n", warning)
	}
	var pub published
	pub, err = e.publishDigest(ctx, app, now().In(loc), loc, dopts)
	rec.DigestRoles, rec.ImpressionsError = pub.roles, pub.impressionsError
	return stageDigest, err
}

// keepInterruptedRun archives the previous run's record when that run never
// ended (archiveInterruptedRun), before this run replaces it. A failure is a
// warning, never a failed run: the record is bookkeeping, and an unreadable one
// is replaced either way.
func (e *env) keepInterruptedRun() {
	r, err := archiveInterruptedRun(e.stateDir)
	switch {
	case err != nil:
		fmt.Fprintf(e.stderr, "vani: the previous run's record was not kept: %s\n", oneLine(err.Error(), maxRunErrorRunes))
	case r != nil:
		fmt.Fprintf(e.stderr, "vani: the previous run (%s) never finished and its process is gone; kept in the run history as interrupted\n", runID(*r))
	}
}

// saveRunRecord writes the record, and a finished one to the run history too;
// a failure is a warning, never a failed run: the run's work is in the ledger
// either way. last.json is written first, so a history failure never costs the
// record status reads.
func (e *env) saveRunRecord(r runRecord) {
	if err := writeRunRecord(e.stateDir, r); err != nil {
		fmt.Fprintf(e.stderr, "vani: could not record the run: %v\n", err)
		return
	}
	if r.Outcome == outcomeRunning {
		return
	}
	if err := writeRunHistory(e.stateDir, r); err != nil {
		fmt.Fprintf(e.stderr, "vani: could not keep the run in the history: %v\n", err)
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
// The result's roles is how many roles the digest listed, set once it is written
// (0 when nothing was new) and nil when it was not. A failure marking them
// Digested still returns roles: the digest exists, and the run record says so.
func (e *env) publishDigest(ctx context.Context, app *App, date time.Time, loc *time.Location, opts digest.Options) (published, error) {
	if app.Ledger == nil {
		return published{}, errors.New("app has no ledger")
	}
	dir := marks.Dir(e.stateDir)
	m, err := marks.ReadMarks(dir)
	if err != nil {
		return published{}, fmt.Errorf("%w: read the Jobs marks in %s: %w", errDigestHeldBack, dir, err)
	}
	recs := app.Ledger.All()
	var adv *pipeline.AdvanceReport
	var stats map[string]backendStat
	if app.RunReport != nil {
		rd := app.RunReport()
		adv, stats = rd.Advance, rd.BackendStats
	}
	counts := countsFor(recs, date, loc)
	if adv != nil {
		counts.CouldNotCheck = uncheckedCauses(adv.FailOpen)
	}
	path, keys, shown, err := digest.WriteShown(e.stateDir, date, recs, m, counts, opts)
	if err != nil {
		return published{}, err
	}
	// What the digest put in front of the owner is the ranking's measurement
	// (unmarked means seen-and-passed only when it was shown). A failure to
	// record it never fails the digest, but it is said, not swallowed.
	var pub published
	if err := recordImpressions(ctx, e.stateDir, now(), date.Format("2006-01-02"), shown); err != nil {
		pub.impressionsError = err.Error()
		fmt.Fprintf(e.stderr, "vani: the digest's impressions were not recorded, so this run's ranking cannot be measured: %v\n", err)
	}
	n := len(keys)
	var markErr error
	if err := pipeline.MarkDigested(ctx, app.Ledger, keys, now(), "digest "+date.Format("2006-01-02")); err != nil {
		markErr = fmt.Errorf("mark the digest's roles digested: %w", err)
	}
	fmt.Fprintf(e.stdout, "digest: %s (%d roles)\n", path, len(keys))
	if len(keys) > 0 && app.Notifier != nil {
		if err := app.Notifier.Notify(ctx, "Vani digest", digestBody(len(keys), adv, stats)); err != nil {
			fmt.Fprintf(e.stderr, "vani: notification failed: %v\n", err)
		}
	}
	pub.roles = &n
	return pub, markErr
}

// published is what publishDigest did: roles is how many new roles the digest
// listed (nil when no digest was written), and impressionsError is set when
// the digest was written but what it showed could not be recorded.
type published struct {
	roles            *int
	impressionsError string
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
	fmt.Fprintf(e.stdout, "schedule: %s %s\n", cfg.Schedule.DailyAt, cfg.Schedule.Timezone)
	// Whether anything fires that schedule: the same answer `status --json` gives.
	at := now()
	live := by[string(posting.Live)]
	dfacet := e.daemonFacet()
	sched := scheduleKeeping(cfg, scheduleOf(cfg, nil, at), dfacet, e.lastRunFacet(at), &live, at)
	for _, l := range unkeptLines(sched, at) {
		fmt.Fprintln(e.stdout, l)
	}
	for _, l := range daemonLines(dfacet) {
		fmt.Fprintln(e.stdout, l)
	}
	fmt.Fprintf(e.stdout, "state dir: %s\n", e.stateDir)
	return nil
}
