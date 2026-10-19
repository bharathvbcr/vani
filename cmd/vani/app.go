package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/daemon"
	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/notify"
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
// publish the digest.
func (e *env) dailyJob(app *App, cfg config.Config) daemon.Job {
	return func(ctx context.Context) error {
		if app.RunOnce == nil {
			return errors.New("app has no pipeline runner")
		}
		if err := app.RunOnce(ctx); err != nil {
			return fmt.Errorf("pipeline: %w", err)
		}
		loc, err := scheduleLoc(cfg)
		if err != nil {
			return err
		}
		return e.publishDigest(ctx, app, now().In(loc), loc)
	}
}

func scheduleLoc(cfg config.Config) (*time.Location, error) {
	_, _, loc, err := cfg.Schedule.Parsed()
	return loc, err
}

// publishDigest writes the digest for date and marks the included postings
// Digested, in that order: a crash between the two re-lists them tomorrow
// rather than losing them.
func (e *env) publishDigest(ctx context.Context, app *App, date time.Time, loc *time.Location) error {
	if app.Ledger == nil {
		return errors.New("app has no ledger")
	}
	recs := app.Ledger.All()
	path, keys, err := digest.Write(e.stateDir, date, recs, countsFor(recs, date, loc))
	if err != nil {
		return err
	}
	var errs []error
	for _, k := range keys {
		ev := ledger.Event{V: 1, At: now(), Key: k, Status: posting.Digested, Reason: "digest " + date.Format("2006-01-02")}
		if err := app.Ledger.Append(ctx, ev); err != nil {
			errs = append(errs, fmt.Errorf("mark %s digested: %w", k, err))
		}
	}
	fmt.Fprintf(e.stdout, "digest: %s (%d roles)\n", path, len(keys))
	if len(keys) > 0 && app.Notifier != nil {
		body := fmt.Sprintf("%d roles to review", len(keys))
		if err := app.Notifier.Notify(ctx, "Vani digest", body); err != nil {
			fmt.Fprintf(e.stderr, "vani: notification failed: %v\n", err)
		}
	}
	return errors.Join(errs...)
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
	return e.publishDigest(ctx, app, date, loc)
}

func (e *env) printStatus(app *App, cfg config.Config) error {
	if app.Ledger == nil {
		return errors.New("app has no ledger")
	}
	recs := app.Ledger.All()
	by := map[posting.Status]int{}
	for _, r := range recs {
		by[r.Status]++
	}
	statuses := make([]string, 0, len(by))
	for s := range by {
		statuses = append(statuses, string(s))
	}
	sort.Strings(statuses)
	fmt.Fprintf(e.stdout, "boards: %d\npostings: %d\n", len(cfg.Watchlist), len(recs))
	for _, s := range statuses {
		fmt.Fprintf(e.stdout, "  %-10s %d\n", s, by[posting.Status(s)])
	}
	fmt.Fprintf(e.stdout, "schedule: %s %s\nstate dir: %s\n", cfg.Schedule.DailyAt, cfg.Schedule.Timezone, e.stateDir)
	return nil
}
