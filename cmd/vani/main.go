// Command vani is Vani's CLI: configuration, watchlist, the daily run and the
// scheduler daemon.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/daemon"
	"github.com/bharathvbcr/Vani/internal/ledger"
)

// Exit codes.
const (
	exitOK      = 0
	exitRuntime = 1
	exitUsage   = 2
)

const usageText = `usage: vani <command> [flags]

commands:
  init                       write a default config (refuses to overwrite)
  config check               validate the config
  config motivation [--set]  print motivation_notes as JSON; --set replaces them with stdin
  config exclude [--set]     print the exclusions (titles, title_unless, companies, locations,
                             descriptions) as JSON; --set replaces them with the JSON object on
                             stdin. The next run drops what they match and reopens what they
                             no longer match; vani prefilter shows it first
  watchlist list [--json]    list boards
  watchlist add    --ats gh|lv|ab|wd|pg --token T --company C [--region eu]
                   [--host H --search S] (workday) [--url U --link-pattern RE] (pg)
                   [--domain HOST [--icon URL]] [--allow-host]
  watchlist remove --ats A --token T
  watchlist domain --ats A --token T --domain HOST [--allow-host]
                             the company's website host its icon is fetched from ("" clears it)
  watchlist icon   --ats A --token T --url URL [--allow-host]
                             an https icon URL for a site that serves none at the usual paths,
                             asked for first ("" clears it; the board needs a domain)
  logos refresh [--force]    fetch the icons of boards with a domain now (the daily run does it
                             too); --force refetches cached and recently failed ones
  daemon                     run the scheduler in the foreground
  run-once [--advance N] [--board ATS:TOKEN]... [--list-only] [--no-digest]
                             run the pipeline once and write the digest; the flags change this
                             run only: cap the advance phase at N postings, list only the named
                             watchlist boards, skip the advance phase, or skip the digest.
                             Refused while the daemon (or another run) holds the ledger
  run-request [--json] [same flags as run-once]
                             ask the running daemon to make that run itself (it polls every few
                             seconds, between its scheduled run and inbox polls); refused when no
                             daemon is running or a request is already waiting. A request the
                             daemon has not picked up within an hour is dropped
  digest [--date YYYY-MM-DD] write the digest for pending roles
  status [--json]            show ledger counts by status; --json adds the last run's outcome,
                             today's spend against each cap and the decision-model setup
                             (reads the ledger without its lock)
  label [-n N] [--labels DIR] label postings for the eval set (disagreements first)
  eval [--labels DIR]        per-backend precision and coverage on the labelled set
  audit [--list | --json] [--blind] [--seed S] [--scored N] [--dropped N]
                             weekly stratified would-I-apply sample (deciles + drop classes);
                             --json prints keys, strata and p_select only (no posting text),
                             --blind hides the model's readings while you answer
                             (audit-sample is the old name)
  audit answer --key K --verdict yes|maybe|no|skip [--source S --p-select P]
                             record one would-I-apply answer; the stratum and p_select come from
                             the flags (an Explore row's, or an audit --json pick's) or the draw
  relevance [-k LIST]        precision@k, drop misses and backend reliability from would_apply labels
  lca import FILE.csv        import a DOL LCA disclosure file (docs/LCA.md)
  lca lookup COMPANY         show the sponsorship-history badge for a company
  employer import FILE.csv   import an IRS EO BMF or IPEDS HD file (docs/EMPLOYER.md)
  employer lookup COMPANY    show the employer-attribute badge for a company
  track list                 list track deciders (tracks_dir, default <state dir>/tracks)
  track show ID              show and check one track (docs/TRACKS.md)
  track use [--dry-run] ID   copy a track's target summary, drop rules and employer
                             preferences into the config; other rules are kept
  kb sync [--dry-run | --confirm] [--bank P] [--rkb P] [--code-root D]
                             copy rkb's bank one way into DevPrism (never deletes)
  reasons draft [--force]    have the llm draft, once, the catalog notes are picked from
                             (spends from the daily llm budget; never overwrites without --force)
  reasons import [--force] FILE
                             check and install a catalog drafted elsewhere ({drafted_by?,fit,gap,check})
  reasons check              validate the catalog against the current target summary
  kbmatch run [--top N] [--dry-run]
                             ask the llm whether the top postings' work is the work in your
                             knowledge base (spends from the daily llm budget; sends each posting,
                             the target summary and a bounded digest of the bank to the provider;
                             the answers only order and explain)
  kbmatch status             how many answers are kept and current
  impressions log --surface jobs|audit --slate ID [--section S] [--json]
                             record the rows a screen shows ({"rows":[{"key","position"}]} on
                             stdin), once per slate; keys and positions only, never a title
  labels [--json]            the label vocabularies (fit, status, category, triage options) as
                             value and display string in display order; reads no state
  jobs list [--json] [--view new|saved|applied|hidden|all] [--q TEXT] [--limit N]
            [--sort default|priority|relevance|fresh|company] [--fit LIST]
            [--work-mode LIST] [--sponsorship LIST] [--max-age DAYS] [--open] [--half-life DAYS]
            [--state LIST] [--area LIST] [--remote | --or-remote]
                             browse retrieved roles (reads the ledger without its lock), with
                             freshness, display-only relevance/priority scores, keywords, and the
                             places and remote listing the board's location names (--or-remote:
                             in those states/areas, or listed as remote)
  jobs stats [--json] [same selection flags as list]
                             distributions over the selection: fit, age, scores, companies, keywords,
                             US states, areas and remote listings
  jobs folders [--json] [--view new|saved|applied|hidden|all]
                             category folder counts for the selection (career tracks from drafts/deciders)
  jobs show [--json] KEY     one role with its description
  jobs find [--json] --url URL
                             which tracked role a page is (scheme, host and path against url and
                             apply_url; query and fragment ignored); read-only, never fetches
  jobs ingest --json         jobs scraped from a listing page: {page,save_search,jobs} on stdin,
                             {queued,duplicates,rejected,search,analysis} on stdout. Queues them in
                             <state dir>/inbox (the ledger's owner folds them) and, with
                             save_search, adds the page to the watchlist (docs/EXTENSION_INGEST.md)
  jobs mark [--json] KEY saved|applied|hidden|clear
                             record what you did with a role (state dir marks/, never the ledger)
  jobs note [--json] KEY TEXT
                             a private note on a role ("" clears it); never sent to a model
  jobs gaps [--json] [-n N] [--examples N] [same selection flags as list]
                             skills to build: what the selected roles ask for that DevPrism found
                             missing (ranked first) or your target summary does not name, by the
                             priority of the roles each blocks; display-only
  retriage [--dry-run] [--title-drops]
                             return postings triaged with no backend reading to live; with
                             --title-drops, postings dropped on a title the current
                             title_signals.read and drop rules no longer drop
  rescore [--dry-run] [--limit N]
                             score scored and explained postings that have no score (they went
                             through while devprism.binary was empty); status and explanation
                             are kept. DevPrism only, no model; at most N (default
                             advance_per_run). --dry-run counts without the ledger lock
  prefilter [--list]        dry run of the prefilter (exclude, and title_signals.read with the
                             drop rules): how many live postings it would drop before triage,
                             which kept ones the next run's exclusions drop or return to live,
                             and how the decision model read the ones it already read (never
                             writes)
  bench local [--model GGUF]... [-n N] [--questions combined|per_slot] [--batch N]
              [--parallel N] [--json]
                             replay postings the primary already read through local decision
                             models; per-slot agreement, confusion, calibration and latency
                             (Metal only, no API key, never writes the ledger)
  semantic warm [--max N]    embed the freshest postings against the knowledge base (needs a
                             -tags tessl build, semantic.snapshot and kb.bank); semantic status
                             shows the build, config, profile size, cached scores and model
  answers similar [--json]   which question wordings read alike, by embedding: {"texts":[...],
                             "min_cos"?} on stdin, {"available","pairs":[{a,b,cos,tier}]} on
                             stdout; wordings only, never answer values (-h lists the bounds)
  draft                      draft one application answer: JSON request on stdin, JSON reply
                             on stdout (spends from the daily llm budget)
  suggest                    triage a form's new fields with the decision model and draft the
                             essays: JSON request on stdin, JSON reply on stdout
  resume                     build a resume PDF for one job with DevPrism (no model): JSON
                             request on stdin, JSON reply with the PDF on stdout
  migrate rekey-fallback [--dry-run]
                             one-time: move fallback-keyed (x:) postings, and their marks, to the
                             keys titles that keep C++/C#/.NET apart give them (backs up first;
                             refuses a second run; takes the ledger lock)
  launchd plist [--binary P] [--label L]
                             print a launchd agent plist (never installs it); it pins the
                             config and state dir this command resolved

environment: VANI_CONFIG, VANI_STATE_DIR
`

// usageError marks an error that should exit 2 and print usage context.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// signalContext returns the context the daemon runs under. Tests replace it.
var signalContext = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// now is the clock for digest dates. Tests replace it.
var now = time.Now

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// env carries what every subcommand needs.
type env struct {
	configPath string
	stateDir   string
	stdout     io.Writer
	stderr     io.Writer
}

// run executes the CLI and returns the exit code: 0 ok, 1 runtime error,
// 2 usage error.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}
	cfgPath, stateDir, err := config.DefaultPaths()
	if err != nil {
		fmt.Fprintf(stderr, "vani: %v\n", err)
		return exitRuntime
	}
	e := &env{configPath: cfgPath, stateDir: stateDir, stdout: stdout, stderr: stderr}

	var cmdErr error
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usageText)
		return exitOK
	case "init":
		cmdErr = e.cmdInit(args[1:])
	case "config":
		cmdErr = e.cmdConfig(args[1:])
	case "watchlist":
		cmdErr = e.cmdWatchlist(args[1:])
	case "logos":
		cmdErr = e.cmdLogos(args[1:])
	case "daemon":
		cmdErr = e.cmdDaemon(args[1:])
	case "run-once":
		cmdErr = e.cmdRunOnce(args[1:])
	case "run-request":
		cmdErr = e.cmdRunRequest(args[1:])
	case "digest":
		cmdErr = e.cmdDigest(args[1:])
	case "status":
		cmdErr = e.cmdStatus(args[1:])
	case "launchd":
		cmdErr = e.cmdLaunchd(args[1:])
	case "lca":
		cmdErr = e.cmdLCA(args[1:])
	case "employer":
		cmdErr = e.cmdEmployer(args[1:])
	case "track":
		cmdErr = e.cmdTrack(args[1:])
	case "label":
		cmdErr = e.cmdLabel(args[1:])
	case "eval":
		cmdErr = e.cmdEval(args[1:])
	case "kb":
		cmdErr = e.cmdKB(args[1:])
	case "draft":
		cmdErr = e.cmdDraft(args[1:])
	case "suggest":
		cmdErr = e.cmdSuggest(args[1:])
	case "resume":
		cmdErr = e.cmdResume(args[1:])
	case "reasons":
		cmdErr = e.cmdReasons(args[1:])
	case "kbmatch":
		cmdErr = e.cmdKBMatch(args[1:])
	case "jobs":
		cmdErr = e.cmdJobs(args[1:])
	case "impressions":
		cmdErr = e.cmdImpressions(args[1:])
	case "labels":
		cmdErr = e.cmdLabels(args[1:])
	case "retriage":
		cmdErr = e.cmdRetriage(args[1:])
	case "rescore":
		cmdErr = e.cmdRescore(args[1:])
	case "prefilter":
		cmdErr = e.cmdPrefilter(args[1:])
	case "migrate":
		cmdErr = e.cmdMigrate(args[1:])
	case "bench":
		cmdErr = e.cmdBench(args[1:])
	case "semantic":
		cmdErr = e.cmdSemantic(args[1:])
	case "answers":
		cmdErr = e.cmdAnswers(args[1:])
	case "audit", "audit-sample": // audit-sample: the old name
		cmdErr = e.cmdAudit(args[1:])
	case "relevance":
		cmdErr = e.cmdRelevance(args[1:])
	default:
		cmdErr = usagef("unknown command %q", args[0])
	}

	switch {
	case cmdErr == nil, errors.Is(cmdErr, flag.ErrHelp):
		return exitOK
	case errors.As(cmdErr, new(usageError)):
		fmt.Fprintf(stderr, "vani: %v\n\n%s", cmdErr, usageText)
		return exitUsage
	default:
		fmt.Fprintf(stderr, "vani: %v\n", cmdErr)
		return exitRuntime
	}
}

// newFlags builds a FlagSet that reports parse errors to the caller instead of
// exiting, and prints its own usage to stderr.
func (e *env) newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("vani "+name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	return fs
}

// parse parses args and rejects stray positional arguments.
// parsePositional is parse for commands that take positional arguments; the
// caller checks fs.NArg itself.
func parsePositional(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError{err.Error()}
	}
	return nil
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError{err.Error()}
	}
	if fs.NArg() != 0 {
		return usagef("unexpected arguments: %v", fs.Args())
	}
	return nil
}

// loadConfig loads the config and warns on stderr about each old key name it
// still uses (the file keeps working; Save writes the new names).
func (e *env) loadConfig() (config.Config, error) {
	cfg, old, err := config.LoadWithDeprecations(e.configPath)
	if err != nil {
		return config.Config{}, err
	}
	for _, d := range old {
		fmt.Fprintf(e.stderr, "vani: config %s: %s\n", e.configPath, d)
	}
	return cfg, nil
}

func (e *env) cmdInit(args []string) error {
	if err := parse(e.newFlags("init"), args); err != nil {
		return err
	}
	if _, err := os.Lstat(e.configPath); err == nil {
		return fmt.Errorf("%s already exists; refusing to overwrite", e.configPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := config.Save(e.configPath, config.Default()); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "wrote %s\nstate dir: %s\n", e.configPath, e.stateDir)
	return nil
}

func (e *env) cmdConfig(args []string) error {
	if len(args) > 0 && args[0] == "motivation" {
		return e.cmdConfigMotivation(args[1:])
	}
	if len(args) > 0 && args[0] == "exclude" {
		return e.cmdConfigExclude(args[1:])
	}
	if len(args) == 0 || args[0] != "check" {
		return usagef("config: expected subcommand \"check\", \"motivation\" or \"exclude\"")
	}
	if err := parse(e.newFlags("config check"), args[1:]); err != nil {
		return err
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "ok: %s (%d boards, %d allowed hosts)\n", e.configPath, len(cfg.Watchlist), len(cfg.AllowedHosts))
	return nil
}

// daemonClock drives `vani daemon`'s schedule. Tests replace it.
var daemonClock daemon.Clock = daemon.SystemClock{}

func (e *env) cmdDaemon(args []string) error {
	if err := parse(e.newFlags("daemon"), args); err != nil {
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
	// The ledger is held from here on, so this process is the daemon: run
	// requests (runrequest.go) find it through daemon.json.
	pid := os.Getpid()
	if err := writeDaemonRecord(e.stateDir, pid, now()); err != nil {
		return fmt.Errorf("announcing the daemon: %w", err)
	}
	defer func() {
		if err := removeDaemonRecord(e.stateDir, pid); err != nil {
			fmt.Fprintf(e.stderr, "vani: removing the daemon record: %v\n", err)
		}
	}()
	e.clearClaimedRequest()
	sigCtx, stop := signalContext()
	defer stop()
	// A manual run that leaves the ledger unusable ends the daemon as a
	// scheduled one does: cancelling ctx ends daemon.Run, and fatal is returned.
	ctx, cancel := context.WithCancel(sigCtx)
	var fatal error
	fmt.Fprintf(e.stderr, "vani: daemon started, daily at %s %s\n", cfg.Schedule.DailyAt, cfg.Schedule.Timezone)
	// The daily run, the inbox poll and a requested manual run all write the
	// ledger: one at a time.
	var ledgerMu sync.Mutex
	daily := e.dailyJob(app, cfg, triggerSchedule, runOptions{})
	job := func(ctx context.Context) error {
		ledgerMu.Lock()
		defer ledgerMu.Unlock()
		if err := ctx.Err(); err != nil {
			return err // ended while a manual run held the ledger: no run, no record
		}
		return daily(ctx)
	}
	var wg sync.WaitGroup
	// Registered after app.close's defer, so it runs first: the polls must be
	// done with the ledger before the ledger closes.
	defer func() { cancel(); wg.Wait() }()
	if app.PollInbox != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.pollInboxLoop(ctx, app.PollInbox, &ledgerMu)
		}()
	}
	manual := func(ctx context.Context, opts runOptions) error {
		return e.dailyJob(app, cfg, triggerManual, opts)(ctx)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := e.pollRequestLoop(ctx, manual, &ledgerMu); err != nil {
			fmt.Fprintf(e.stderr, "vani: requested manual run failed, exiting: %v\n", err)
			fatal = err
			cancel()
		}
	}()
	err = daemon.Run(ctx, cfg.Schedule, job, daemonClock)
	cancel()
	wg.Wait()
	if err == nil {
		err = fatal
	}
	return err
}

// inboxPollInterval is how often the daemon looks for spooled extension jobs
// between daily runs. A variable only so tests need not wait.
var inboxPollInterval = 2 * time.Minute

// pollInboxLoop calls poll every inboxPollInterval, holding mu so it never
// overlaps the daily run, until ctx ends. A failed or panicking poll is
// reported and the loop goes on: the spooled files stay for the next one.
func (e *env) pollInboxLoop(ctx context.Context, poll func(context.Context) error, mu *sync.Mutex) {
	t := time.NewTicker(inboxPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := e.pollInboxOnce(ctx, poll, mu); err != nil && ctx.Err() == nil {
			fmt.Fprintf(e.stderr, "vani: inbox poll: %v\n", err)
		}
	}
}

func (e *env) pollInboxOnce(ctx context.Context, poll func(context.Context) error, mu *sync.Mutex) (err error) {
	mu.Lock()
	defer mu.Unlock()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return poll(ctx)
}

// cmdRunOnce runs the pipeline once, by hand. Its flags tune this run only;
// the config, and so the daemon's runs, are unchanged.
func (e *env) cmdRunOnce(args []string) error {
	fs := e.newFlags("run-once")
	optsp, boards := runOptionFlags(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	opts := *optsp
	opts.Boards = *boards
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	if err := checkRunOptions(opts, cfg.Watchlist); err != nil {
		return err
	}
	app, err := buildApp(cfg, e.stateDir, e.stderr)
	if errors.Is(err, ledger.ErrLocked) {
		hint := "the daemon, or a run-once, rescore or retriage still going; try again when it is done"
		if _, derr := liveDaemon(e.stateDir); derr == nil {
			hint = "the daemon is running; `vani run-request` with the same flags asks it to make this run"
		}
		return fmt.Errorf("%w: another vani holds it (%s)", err, hint)
	}
	if err != nil {
		return err
	}
	defer app.close()
	ctx, stop := signalContext()
	defer stop()
	return e.dailyJob(app, cfg, triggerManual, opts)(ctx)
}

func (e *env) cmdStatus(args []string) error {
	fs := e.newFlags("status")
	asJSON := fs.Bool("json", false, "one JSON object on stdout: last run, ledger, spend, configuration")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *asJSON {
		return e.statusReport()
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	recs, err := e.ledgerRecords() // no lock: works while the daemon runs
	if err != nil {
		return err
	}
	return e.printStatus(recs, cfg)
}
