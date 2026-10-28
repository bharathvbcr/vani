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
	"strings"
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
	// exitConfigRestart is sysexits EX_TEMPFAIL (75). `vani daemon` returns it
	// when a process-lifetime field changed — the schedule, or devprism.binary
	// / devprism.tools_hash — so a supervisor starts a process that reads the
	// file, or when its own binary was replaced on disk (binarywatch.go). Nothing
	// else in vani uses 75. The schedule agent's
	// ScheduleAgentConstants.configRestartStatus is the same number. launchd
	// KeepAlive with SuccessfulExit false also restarts any non-zero exit.
	exitConfigRestart = 75
)

// usageText is the help: usageTemplate with the post-apply stage words filled in
// from marks.Stages (stagesPlaceholder), so `jobs mark`'s help and its usage
// error cannot drift from what marks accepts.
var usageText = strings.ReplaceAll(usageTemplate, stagesPlaceholder, stageList("|"))

const stagesPlaceholder = "{{stages}}"

const usageTemplate = `usage: vani <command> [flags]

commands:
  version [--json]           print the product version ("dev" unless built by scripts/release.sh)
  init [--json]              write a default config (refuses to overwrite)
  config check [--json]      validate the config
  config motivation [--set]  print motivation_notes as JSON; --set replaces them with stdin
  config exclude [--set]     print the exclusions (titles, title_unless, companies, locations,
                             descriptions) as JSON; --set replaces them with the JSON object on
                             stdin. The next run drops what they match and reopens what they
                             no longer match; vani prefilter shows it first
  watchlist list [--json]    list boards
  watchlist add [--json] --ats gh|lv|ab|wd|gm|sr|ib|av|ds|at|oc|pg --token T --company C [--region eu]
                   [--host H --search S] (workday, oc) [--host H --url U] (av)
                   [--url U --link-pattern RE] (pg)
                   [--domain HOST [--icon URL]] [--allow-host]
  watchlist remove [--json] --ats A --token T
  watchlist domain [--json] --ats A --token T --domain HOST [--allow-host]
                             the company's website host its icon is fetched from ("" clears it)
  watchlist icon [--json] --ats A --token T --url URL [--allow-host]
                             an https icon URL for a site that serves none at the usual paths,
                             asked for first ("" clears it; the board needs a domain)
  watchlist discover --search TEXT [--country US] [--work-mode remote|hybrid|onsite]
                   [--seniority LEVEL] [--pages N] [--json] [--allow-host]
                             ask freehire.me (a third-party site; it must be in allowed_hosts,
                             or pass --allow-host) which boards have roles matching a search:
                             the boards ranked by matches, marked watched or new, each with the
                             watchlist add that adds it, and the results Vani has no board for.
                             One request of 100 results by default, at most 3 pages. It never
                             edits the watchlist
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
  runs list [--json]         the finished runs, newest first: every run's record is kept in
                             <state dir>/runs/history/ (the newest 30, 4 MiB); * marks the latest
  runs show [--json] [ID]    one run's record and failures (ID from runs list; default the latest)
  runs failures [--json] [ID]
                             the plain-text failure report for a run: its failed boards, backend
                             errors, error and the log files; the Mac app's "Copy failures"
  label [-n N] [--labels DIR] label postings for the eval set (disagreements first)
  export heldout [--json] [--out PATH]
                             owner-labelled posting keys + normalised-text sha256 for Lappi's held-out set
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
  rankfit [--part all|platt|weights] [--dry-run] [--labels DIR] [--out FILE] [--seed N]
          [--lambda X] [--holdout PCT] [--max-pairs N]
                             fit Score's per-backend calibration (eval slot labels, 100 judged
                             postings per backend) and shares (150 usable would_apply answers
                             from vani audit, plus marks and impressions) to a rank weights file;
                             refuses below the thresholds and writes nothing. Use it by setting
                             rank_weights_file
  lca import [--json] FILE.csv
                             import a DOL LCA disclosure file (docs/LCA.md)
  lca lookup [--json] COMPANY
                             show the sponsorship-history badge for a company
  employer import [--json] FILE.csv
                             import an IRS EO BMF or IPEDS HD file (docs/EMPLOYER.md)
  employer lookup [--json] COMPANY
                             show the employer-attribute badge for a company
  track list [--json]        list track deciders (tracks_dir, default <state dir>/tracks)
  track show [--json] ID     show and check one track (docs/TRACKS.md)
  track use [--json] [--dry-run] [--allow-unchecked] ID
                             copy a track's target summary, drop rules and employer
                             preferences into the config; other rules are kept
  kb sync [--dry-run | --confirm] [--replace [--keep IDS] [--delete-all-kinds]
          [--max-delete N] [--career-db P]] [--bank P] [--rkb P] [--code-root D]
                             copy the bank bullets that kb.selections approve one way into
                             DevPrism; --replace also deletes the blocks outside that pool,
                             after copying career.db (see README, Knowledge base)
  reasons draft [--json] [--force]
                             have the llm draft, once, the catalog notes are picked from
                             (spends from the daily llm budget; never overwrites without --force)
  reasons import [--json] [--force] FILE
                             check and install a catalog drafted elsewhere ({drafted_by?,fit,gap,check})
  reasons check [--json]     validate the catalog against the current target summary
  kbmatch run [--top N] [--dry-run]
                             ask the llm whether the top postings' work is the work in your
                             knowledge base (spends from the daily llm budget; sends each posting,
                             the target summary and a bounded digest of the bank to the provider;
                             the answers only order and explain)
  kbmatch status [--json]    how many answers are kept and current
  teacher [--dry-run] [--budget-usd N] [--model M] [--of BACKEND] [--concurrency N] [--max N]
                             have the llm tier re-read the postings the decision model triaged,
                             with the same question, and keep its readings (teacher_readings.jsonl)
                             for Lappi's agreement filter; --dry-run prices it and asks nothing;
                             a real run needs --budget-usd and stops at the lesser of it and the
                             daily llm cap's headroom
  impressions log --surface jobs|audit --slate ID [--section S] [--json]
                             record the rows a screen shows ({"rows":[{"key","position"}]} on
                             stdin), once per slate; keys and positions only, never a title
  labels [--json]            the label vocabularies (fit, status, category, triage options) as
                             value and display string in display order; reads no state
  jobs list [--json] [--view new|saved|applied|hidden|all|pending] [--q TEXT] [--limit N]
            [--sort default|priority|relevance|fresh|company] [--fit LIST]
            [--work-mode LIST] [--sponsorship LIST] [--max-age DAYS] [--open] [--half-life DAYS]
            [--state LIST] [--area LIST] [--remote | --or-remote]
                             browse retrieved roles (reads the ledger without its lock), with
                             freshness, display-only relevance/priority scores, keywords, and the
                             places and remote listing the board's location names (--or-remote:
                             in those states/areas, or listed as remote). --view pending lists
                             the postings no run has triaged yet, unranked (no fit, relevance or
                             priority), freshest first; only list and folders take it
  jobs stats [--json] [same selection flags as list]
                             distributions over the selection: fit, age, scores, companies, keywords,
                             US states, areas and remote listings
  jobs export [--json] [--out FILE.xlsx] [--top N | --keys-stdin] [same selection flags and --sort as list]
                             write jobs to an Excel workbook: the first N (default 100, at most
                             5000) of the list those flags show, in its order, or the jobs whose
                             keys stdin lists one per line, in that order (keys the selection does
                             not hold are reported as "missing"; text mode then exits 1). Default
                             file: <state dir>/export/jobs/jobs-<view>-<time>.xlsx
  jobs folders [--json] [--view new|saved|applied|hidden|all|pending]
                             category folder counts for the selection (career tracks from drafts/deciders)
  jobs show [--json] KEY     one role with its description
  jobs find [--json] --url URL
                             which tracked role a page is (scheme, host and path against url and
                             apply_url; query and fragment ignored); read-only, never fetches
  jobs ingest --json         jobs scraped from a listing page: {page,save_search,jobs} on stdin,
                             {queued,duplicates,rejected,search,analysis} on stdout. Queues them in
                             <state dir>/inbox (the ledger's owner folds them) and, with
                             save_search, adds the page to the watchlist (docs/EXTENSION_INGEST.md)
  jobs mark [--json] KEY saved|applied|hidden|clear|{{stages}}|unstage
                             record what you did with a role (state dir marks/, never the ledger).
                             A stage word records what became of an applied role, which stays
                             applied; unstage sets it back to plain applied
  jobs note [--json] KEY TEXT
                             a private note on a role ("" clears it); never sent to a model
  jobs retriage [--json] [--dry-run | --clear] KEY
                             have the llm triage model (llm.triage_model, Sonnet) read this one
                             role by hand: the triage question, all four slots (spends from the
                             daily llm budget; sends the posting and the target summary to the
                             provider). The reading is kept in state dir retriage/, outranks the
                             run's reading when ranking, and never drops or undrops a role
                             (--dry-run: the estimated cost, asks nothing; --clear: remove it).
                             Not "retriage", which asks no model
  jobs resume build|render|show|use|revalidate|diff|current|coverage|gaps [--json] ... KEY
                             Resume Studio: one role's resume versions, kept in state dir
                             resumes/ (docs/RUNTIME.md). build [--dry-run] [--allow-base] asks
                             Opus to pick the bullets (spends from the daily llm budget); with
                             [--from vN] [--scope NAME] [--lock NAME ...] [--instruction TEXT]
                             it redrafts a version, keeping what is locked or out of scope;
                             render [--fit] [--from vN] builds the owner's edit (JSON on stdin,
                             no model); show [--no-check] KEY [vN] lists versions and whether
                             the bank still vouches for them; use KEY vN binds a checked version
                             for applying; revalidate KEY vN drops what went stale and rebuilds;
                             diff KEY vA vB says what differs; current KEY vN restores a version;
                             coverage KEY [vN] says which posting terms a version hits and
                             which no eligible bullet covers; gaps [--dry-run] KEY hands those
                             terms to state dir resumes/gaps.jsonl for the career session
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
                             -tags tessl build, semantic.snapshot and kb.bank)
  semantic status [--json]   the semantic build, config, profile size, cached scores and model
  answers similar [--json]   which question wordings read alike, by embedding: {"texts":[...],
                             "min_cos"?} on stdin, {"available","pairs":[{a,b,cos,tier}]} on
                             stdout; wordings only, never answer values (-h lists the bounds)
  draft                      draft one application answer: JSON request on stdin, JSON reply
                             on stdout (spends from the daily llm budget)
  suggest                    triage a form's new fields with the decision model and draft the
                             essays: JSON request on stdin, JSON reply on stdout
  resume                     build a resume PDF for one job with rkb (the model picks the
                             bullets): JSON request on stdin, JSON reply with the PDF on stdout
  migrate rekey-fallback [--json] [--dry-run]
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
	// paths is how configPath and stateDir were resolved (status shows it);
	// zero when the env was built some other way.
	paths  config.Resolved
	stdout io.Writer
	stderr io.Writer

	// jsonAsked is the running command's --json flag (jsonFlag); nil when it
	// has none. runEnv reads it to reply {"error","code"} for a failure the
	// command did not report itself.
	jsonAsked *bool

	// runLog is the log file a manual run's output goes to, as its starter said
	// (VANI_RUN_LOG, checked by checkRunLog); "" when it said nothing or what it
	// said was refused. dailyJob records it in the run record.
	runLog string
	// runTee copies a run's stderr into runs/history/<id>.log when runLog is
	// empty. Nil for commands that do not run the pipeline.
	runTee *cappedTee

	// applied is the config the last run's reload left (reloadConfig); nil
	// before the first. A run whose config file cannot be read falls back to it.
	appliedMu sync.Mutex
	applied   *config.Config

	// restart ends the daemon when a process-lifetime config field changes.
	// Nil outside `vani daemon`; reloadConfig still returns errConfigRestart.
	restart context.CancelCauseFunc

	// binary is the daemon's own executable as it started (binarywatch.go);
	// reloadConfig asks it whether the file was replaced. Nil outside
	// `vani daemon`, or when the identity could not be recorded: no check.
	binary *binWatch
}

// run executes the CLI and returns the exit code: 0 ok, 1 runtime error,
// 2 usage error, exitConfigRestart when the daemon must restart for a
// process-lifetime config change.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}
	paths, err := config.ResolveDefault()
	if err != nil {
		fmt.Fprintf(stderr, "vani: %v\n", err)
		return exitRuntime
	}
	if paths.Warning != "" {
		fmt.Fprintf(stderr, "vani: warning: %s\n", paths.Warning)
	}
	return runEnv(&env{configPath: paths.ConfigPath, stateDir: paths.StateDir, paths: paths, stdout: stdout, stderr: stderr}, args)
}

// runEnv dispatches args against an already-resolved env. run builds the env
// from the process environment; tests that must run in parallel build their
// own, since t.Setenv cannot be used under t.Parallel.
func runEnv(e *env, args []string) int {
	args, global := splitGlobalJSON(args)
	stdout, stderr := e.stdout, e.stderr
	if len(args) == 0 {
		if global {
			fmt.Fprintln(stderr, "vani: expected a command after --json")
			_ = writeJSON(stdout, errorReply{"expected a command after --json (vani --json help lists them)", codeUsage})
			return exitUsage
		}
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		if global {
			if err := writeJSON(stdout, jsonInventory{jsonLeaves}); err != nil {
				fmt.Fprintf(stderr, "vani: %v\n", err)
				return exitRuntime
			}
			return exitOK
		}
		fmt.Fprint(stdout, usageText)
		return exitOK
	}

	// out counts the reply, so a failure asked for JSON that wrote none gets
	// the {"error","code"} envelope below.
	out := &countingWriter{w: stdout}
	e.stdout, e.jsonAsked = out, nil
	defer func() { e.stdout, e.jsonAsked = stdout, nil }()
	leaf, n, known := leafOf(args)

	var cmdErr error
	switch {
	case !global:
	case !known:
		cmdErr = usagef("%q names no command (vani --json help lists them)", strings.Join(args[:min(len(args), 3)], " "))
	case leaf.Mode == jsonFlag:
		args = append(append(args[:n:n], "--json"), args[n:]...)
	case leaf.Mode == jsonText, leaf.Mode == jsonNone:
		cmdErr = errNoJSON(leaf)
	}
	if cmdErr == nil {
		cmdErr = e.dispatch(args)
	}

	// A usage error replies in JSON only under the global flag: a command's own
	// --json has always exited 2 with nothing on stdout for one, and the Mac app
	// reads that silence as an older vani (ResumeStudio.swift).
	code := exitCode(cmdErr)
	asked := global || code != exitUsage && (leaf.Mode == jsonAlways || e.jsonAsked != nil && *e.jsonAsked)
	if code != exitOK && asked && out.n.Load() == 0 && !errors.Is(cmdErr, errAlreadyReported) {
		if err := writeJSON(out, errorReply{cmdErr.Error(), envelopeCode(cmdErr)}); err != nil {
			fmt.Fprintf(stderr, "vani: write error reply: %v\n", err)
		}
	}
	switch code {
	case exitOK:
		return exitOK
	case exitUsage:
		if global {
			fmt.Fprintf(stderr, "vani: %v\n", cmdErr)
		} else {
			fmt.Fprintf(stderr, "vani: %v\n\n%s", cmdErr, usageText)
		}
		return exitUsage
	default:
		fmt.Fprintf(stderr, "vani: %v\n", cmdErr)
		return code
	}
}

// dispatch runs the command args name.
func (e *env) dispatch(args []string) error {
	var cmdErr error
	switch args[0] {
	case "version", "--version":
		cmdErr = e.cmdVersion(args[1:])
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
	case "runs":
		cmdErr = e.cmdRuns(args[1:])
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
	case "export":
		cmdErr = e.cmdExport(args[1:])
	case "eval":
		cmdErr = e.cmdEval(args[1:])
	case "rankfit":
		cmdErr = e.cmdRankfit(args[1:])
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
	case "teacher":
		cmdErr = e.cmdTeacher(args[1:])
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
	return cmdErr
}

// exitCode is the process exit code for a command error. A wrapped
// errConfigRestart is exitConfigRestart, including through errors.Join.
// Named exitCode because statusOf is the test helper that reads `vani status`.
func exitCode(err error) int {
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return exitOK
	case errors.As(err, new(usageError)), errors.As(err, new(unsupportedError)):
		return exitUsage
	case errors.Is(err, errConfigRestart):
		return exitConfigRestart
	default:
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
	e.warnDeprecated(old)
	return cfg, nil
}

// updateConfig is config.UpdateWithDeprecations with the same warnings
// loadConfig prints: an update rewrites the file with the new key names, so
// the user is told which old ones it used.
func (e *env) updateConfig(fn func(*config.Config) error) error {
	old, err := config.UpdateWithDeprecations(e.configPath, fn)
	e.warnDeprecated(old)
	return err
}

func (e *env) warnDeprecated(old []string) {
	for _, d := range old {
		fmt.Fprintf(e.stderr, "vani: config %s: %s\n", e.configPath, d)
	}
}

func (e *env) cmdInit(args []string) error {
	fs := e.newFlags("init")
	asJSON := e.jsonFlag(fs, `{"config","state_dir"} on stdout`)
	if err := parse(fs, args); err != nil {
		return err
	}
	err := config.Create(e.configPath, config.Default())
	if errors.Is(err, os.ErrExist) {
		err = fmt.Errorf("%s already exists; refusing to overwrite", e.configPath)
	}
	return e.reply(*asJSON, struct {
		Config   string `json:"config"`
		StateDir string `json:"state_dir"`
	}{e.configPath, e.stateDir}, err, func() {
		fmt.Fprintf(e.stdout, "wrote %s\nstate dir: %s\n", e.configPath, e.stateDir)
	})
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
	fs := e.newFlags("config check")
	asJSON := e.jsonFlag(fs, `{"config","boards","allowed_hosts"} on stdout`)
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	cfg, err := e.loadConfig()
	return e.reply(*asJSON, configCheckReply{e.configPath, len(cfg.Watchlist), len(cfg.AllowedHosts)}, err, func() {
		fmt.Fprintf(e.stdout, "ok: %s (%d boards, %d allowed hosts)\n", e.configPath, len(cfg.Watchlist), len(cfg.AllowedHosts))
	})
}

// configCheckReply is `vani config check --json`: the config is valid.
type configCheckReply struct {
	Config       string `json:"config"`
	Boards       int    `json:"boards"`
	AllowedHosts int    `json:"allowed_hosts"`
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
	e.installRunTee()
	// Before the app is built, so a binary replaced while it starts is seen.
	e.binary = watchOwnBinary(e.stderr)
	app, err := buildApp(cfg, e.stateDir, e.stderr)
	if err != nil {
		return err
	}
	defer app.close()
	// The ledger is held from here on, so this process is the daemon: run
	// requests (runrequest.go) find it through daemon.json.
	pid := os.Getpid()
	// It says whether it has the decision model's key, from its environment or
	// from the login Keychain (the one read, bounded, kept for the process): a
	// daemon without one runs with the model disabled, loudly.
	var model *daemonModel
	if app.ModelKey != nil {
		model = daemonModelFor(cfg, app.ModelKey)
	} else {
		model = daemonModelOf(cfg, os.Getenv)
	}
	if model != nil && !model.Enabled {
		fmt.Fprintf(e.stderr, "vani: daemon: %s\n", model.Reason)
	}
	if err := writeDaemonAnnouncement(e.stateDir, daemonRecord{V: runRequestVersion, PID: pid, StartedAt: now(), Model: model}); err != nil {
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
	// The cause distinguishes a process-lifetime config change (exit 75) from a
	// signal (exit 0). The first cancel wins.
	ctx, cancel := context.WithCancelCause(sigCtx)
	e.bindRestart(cancel)
	var (
		fatal   error
		fatalMu sync.Mutex
	)
	// setFatal records the first error that must end the daemon and stops it.
	setFatal := func(err error) {
		fatalMu.Lock()
		if fatal == nil {
			fatal = err
		}
		fatalMu.Unlock()
		cancel(err)
	}
	fmt.Fprintf(e.stderr, "vani: daemon started, daily at %s %s\n", cfg.Schedule.DailyAt, cfg.Schedule.Timezone)
	// The daily run, the inbox poll and a requested manual run all write the
	// ledger: one at a time.
	var ledgerMu sync.Mutex
	// The idle warm (idlewarm.go) never takes ledgerMu. A run stops it first, so
	// the run's own semantic warm finds the cache lock free.
	gate := newIdleGate()
	daily := e.dailyJob(app, cfg, triggerSchedule, runOptions{})
	job := func(ctx context.Context) error {
		defer e.pauseIdle(ctx, gate)()
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
	defer func() { cancel(nil); wg.Wait() }()
	if app.PollInbox != nil {
		poll := e.inboxPoll(app, cfg)
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A poll warms nothing, so it does not pause the idle warm; it may have
			// added postings, so it nudges it.
			nudged := func(ctx context.Context) error { defer gate.nudge(); return poll(ctx) }
			if err := e.pollInboxLoop(ctx, nudged, &ledgerMu, app.Ledger); err != nil {
				setFatal(err)
			}
		}()
	}
	if idleWarmEnabled() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.idleWarmLoop(ctx, &idleWarmer{stateDir: e.stateDir, stderr: e.stderr, led: app.Ledger,
				cfg: func() config.Config { return e.lastApplied(cfg) }, gate: gate})
		}()
	}
	// serveRunRequest holds ledgerMu before it calls this, so the pause comes
	// after the lock here; the idle warm never takes it, so that cannot wait on it.
	manual := func(ctx context.Context, opts runOptions) error {
		defer e.pauseIdle(ctx, gate)()
		return e.dailyJob(app, cfg, triggerManual, opts)(ctx)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := e.pollRequestLoop(ctx, manual, &ledgerMu); err != nil {
			fmt.Fprintf(e.stderr, "vani: requested manual run failed, exiting: %v\n", err)
			setFatal(err)
		}
	}()
	err = daemon.RunWith(ctx, cfg.Schedule, job, daemonClock, daemon.Hooks{Skipped: e.recordSkippedSlot})
	cancel(nil)
	wg.Wait()
	fatalMu.Lock()
	defer fatalMu.Unlock()
	if err == nil {
		// A signal cancels the parent with context.Canceled and stays exit 0.
		// A pinned config change cancels with errConfigRestart. Whichever
		// landed first is the cause; a later cancel does not replace it.
		if errors.Is(context.Cause(ctx), errConfigRestart) {
			err = errConfigRestart
		} else {
			err = fatal
		}
	}
	return err
}

// inboxPollInterval is how often the daemon looks for spooled extension jobs
// between daily runs. A variable only so tests need not wait.
var inboxPollInterval = 2 * time.Minute

// pollInboxLoop calls poll every inboxPollInterval, holding mu so it never
// overlaps the daily run, until ctx ends. A failed or panicking poll is
// reported and the loop goes on: the spooled files stay for the next one. A
// poll that leaves the ledger unusable ends the loop with daemon.ErrFatal, as a
// run does (ledgerBroken): the daemon exits within one poll interval instead of
// holding a poisoned ledger until the next daily slot. errConfigRestart ends it
// the same way, without the poll-failure line: the process is exiting so the
// new schedule or DevPrism settings apply.
func (e *env) pollInboxLoop(ctx context.Context, poll func(context.Context) error, mu *sync.Mutex, led ledger.Ledger) error {
	t := time.NewTicker(inboxPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		if err := e.pollInboxOnce(ctx, poll, mu); err != nil {
			if errors.Is(err, errConfigRestart) || errors.Is(err, daemon.ErrFatal) {
				return err
			}
			if ctx.Err() == nil {
				fmt.Fprintf(e.stderr, "vani: inbox poll: %v\n", err)
			}
		}
		if err := ledgerBroken(led); err != nil {
			return err
		}
	}
}

// inboxPoll is app.PollInbox as the daemon calls it: the config is re-read
// first, as a run does (reloadConfig), so a poll advances new postings under
// the exclusions saved since the last one. An unreadable config is said and the
// poll goes on with the previous one. A process-lifetime change does not: the
// poll does not run, and the error is returned so the daemon exits.
func (e *env) inboxPoll(app *App, cfg config.Config) func(context.Context) error {
	return func(ctx context.Context) error {
		if _, err := e.reloadConfig(app, cfg, nil); err != nil {
			if errors.Is(err, errConfigRestart) {
				return err
			}
			fmt.Fprintf(e.stderr, "vani: inbox poll: %v\n", err)
		}
		return app.PollInbox(ctx)
	}
}

func (e *env) pollInboxOnce(ctx context.Context, poll func(context.Context) error, mu *sync.Mutex) (err error) {
	mu.Lock()
	defer mu.Unlock()
	defer func() {
		if p := recover(); p != nil {
			writePanic(e.stderr, "vani: inbox poll panicked: ", p)
			err = fmt.Errorf("panic: %s", panicText(p))
			// pollInbox did not return, so its status was not the panic. Replace
			// it with the panic value only; the stack stays on stderr.
			if werr := writeInboxStatus(e.stateDir, inboxStatus{At: now().UTC(), Error: panicText(p)}); werr != nil {
				fmt.Fprintf(e.stderr, "vani: inbox poll: could not record the panic: %s\n", oneLine(werr.Error(), maxStageNote))
			}
		}
	}()
	return poll(ctx)
}

// recordSkippedSlot keeps a daily slot the daemon woke too late to run. It
// writes the history only: last.json stays the last run that actually started.
// A write failure is said and does not stop the daemon.
func (e *env) recordSkippedSlot(slot time.Time, late time.Duration) {
	if late < 0 {
		late = 0
	}
	rec := runRecord{
		PID:        0,
		StartedAt:  slot.UTC(),
		FinishedAt: slot.Add(late).UTC(),
		Outcome:    outcomeSkipped,
		Trigger:    triggerSchedule,
		Error: oneLine(fmt.Sprintf("skipped: %s late, past the %s window",
			late.Round(time.Second), daemon.MissedRunWindow), maxRunErrorRunes),
	}
	if err := writeRunHistory(e.stateDir, rec); err != nil {
		fmt.Fprintf(e.stderr, "vani: could not record the skipped run: %s\n", oneLine(err.Error(), maxRunErrorRunes))
	}
}

// installRunTee wraps stderr so a pipeline run can keep its own log without
// replacing the process log. buildApp must see the same writer, so call this
// before buildApp. A second call does nothing.
func (e *env) installRunTee() {
	if e.runTee != nil {
		return
	}
	tee := newCappedTee(e.stderr)
	e.runTee = tee
	e.stderr = tee
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
	e.installRunTee()
	e.useRunLog()
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
	asJSON := e.jsonFlag(fs, "one JSON object on stdout: last run, ledger, spend, configuration")
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
