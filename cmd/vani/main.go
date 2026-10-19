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
	"syscall"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/daemon"
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
  watchlist list             list boards
  watchlist add    --ats gh|lv|ab|wd|pg --token T --company C [--region eu]
                   [--host H --search S] (workday) [--url U --link-pattern RE] (pg) [--allow-host]
  watchlist remove --ats A --token T
  daemon                     run the scheduler in the foreground
  run-once                   run the pipeline once and write the digest
  digest [--date YYYY-MM-DD] write the digest for pending roles
  status                     show ledger counts by status
  label [-n N] [--labels DIR] label postings for the eval set (disagreements first)
  eval [--labels DIR]        per-backend precision and coverage on the labelled set
  lca import FILE.csv        import a DOL LCA disclosure file (docs/LCA.md)
  lca lookup COMPANY         show the sponsorship-history badge for a company
  employer import FILE.csv   import an IRS EO BMF or IPEDS HD file (docs/EMPLOYER.md)
  employer lookup COMPANY    show the employer-attribute badge for a company
  track list                 list track deciders (tracks_dir, default <state dir>/tracks)
  track show ID              show and check one track (docs/TRACKS.md)
  track use [--dry-run] ID   copy a track's target summary, drop rules and employer
                             preferences into the config; other rules are kept
  kb sync [--dry-run] [--bank P] [--rkb P] [--code-root D]
                             copy rkb's bank one way into DevPrism (never deletes)
  reasons draft [--force]    have the llm draft, once, the catalog notes are picked from
                             (spends from the daily llm budget; never overwrites without --force)
  reasons import [--force] FILE
                             check and install a catalog drafted elsewhere ({drafted_by?,fit,gap,check})
  reasons check              validate the catalog against the current target summary
  jobs list [--json] [--view new|saved|applied|hidden|all] [--q TEXT] [--limit N]
                             browse retrieved roles (reads the ledger without its lock)
  jobs show [--json] KEY     one role with its description
  jobs mark [--json] KEY saved|applied|hidden|clear
                             record what you did with a role (state dir marks/, never the ledger)
  jobs note [--json] KEY TEXT
                             a private note on a role ("" clears it); never sent to a model
  retriage [--dry-run]       return postings triaged with no backend reading to live
  draft                      draft one application answer: JSON request on stdin, JSON reply
                             on stdout (spends from the daily llm budget)
  launchd plist [--binary P] [--label L]
                             print a launchd agent plist (never installs it)

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
	case "daemon":
		cmdErr = e.cmdDaemon(args[1:])
	case "run-once":
		cmdErr = e.cmdRunOnce(args[1:])
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
	case "reasons":
		cmdErr = e.cmdReasons(args[1:])
	case "jobs":
		cmdErr = e.cmdJobs(args[1:])
	case "retriage":
		cmdErr = e.cmdRetriage(args[1:])
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

func (e *env) loadConfig() (config.Config, error) {
	return config.Load(e.configPath)
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
	if len(args) == 0 || args[0] != "check" {
		return usagef("config: expected subcommand \"check\"")
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
	ctx, stop := signalContext()
	defer stop()
	fmt.Fprintf(e.stderr, "vani: daemon started, daily at %s %s\n", cfg.Schedule.DailyAt, cfg.Schedule.Timezone)
	return daemon.Run(ctx, cfg.Schedule, e.dailyJob(app, cfg), daemon.SystemClock{})
}

func (e *env) cmdRunOnce(args []string) error {
	if err := parse(e.newFlags("run-once"), args); err != nil {
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
	ctx, stop := signalContext()
	defer stop()
	return e.dailyJob(app, cfg)(ctx)
}

func (e *env) cmdStatus(args []string) error {
	if err := parse(e.newFlags("status"), args); err != nil {
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
	return e.printStatus(app, cfg)
}
