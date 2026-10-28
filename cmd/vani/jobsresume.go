package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resumestore"
	"github.com/bharathvbcr/Vani/internal/rkbresume"
)

// `vani jobs resume` is Resume Studio's command surface: one role's resume
// versions, kept by internal/resumestore beside the ledger, built by
// internal/rkbresume. Every subcommand answers with --json as one object
// ({"error","code"} on failure, exit 1; a usage error exits 2, as the other jobs
// commands do), and none puts the owner's name or contact lines, or any text of
// a resume, in a reply, a log line or an error: PDFs and HTML are files (0600),
// named by path.
//
//	build [--dry-run] [--allow-base] [--from vN] [--scope NAME] [--lock NAME ...]
//	      [--instruction TEXT] KEY         Opus picks the bullets (or re-picks, from
//	                                       a version); saves a version
//	render [--fit] [--from vN] KEY         the owner's edit (JSON on stdin), no model
//	show [--no-check] KEY [vN]             versions, and whether the bank still vouches
//	use KEY vN                             bind a freshly checked version for applying
//	revalidate KEY vN                      drop what went stale, rebuild, save
//	diff KEY vA vB                         what differs between two stored versions
//	current KEY vN                         make a stored version the one the Studio shows
//	coverage KEY [vN]                      posting terms hit, missed, and uncovered by the bank
//	gaps [--dry-run] KEY                   hand the uncovered terms to resumes/gaps.jsonl
//
// docs/RUNTIME.md ("Resume Studio") has the state layout, locks, deadlines and
// codes.

// Codes of an error reply, beside rkbresume's own (config, kb, render, lint, fit,
// too_large, pages, timeout, cancelled, busy, kb_changed, no_model, ineligible,
// bad_edit), which pass through as they are.
const (
	jrBadRequest  = "bad_request"
	jrUnknownKey  = "unknown_key"
	jrLedger      = "ledger"
	jrStore       = "store"
	jrNotFound    = "not_found"
	jrDamaged     = "damaged"
	jrUnsupported = "unsupported"
	jrFull        = "full"
	jrNotUsable   = "not_usable"
	jrStale       = "stale"
	jrUnchecked   = "unchecked"
	jrNotStale    = "not_stale"
	jrFailed      = "failed"
)

// Deadlines of each subcommand (the plan's table). Build's is rkbresume's own
// (DefaultDeadline, 360 s) and render's are Render's own; the others bound the
// whole command here. Variables only so tests need not wait.
var (
	resumeDryRunDeadline     = 45 * time.Second
	resumeShowDeadline       = 45 * time.Second
	resumeShowStoredDeadline = 5 * time.Second
	resumeUseDeadline        = 10 * time.Second
	resumeRevalidateDeadline = rkbresume.RenderFitDeadline
	// resumeSaveWait bounds writing a built version (and an attempt record), which
	// outlives a cancelled command: a build already paid for is saved even when Stop
	// arrives a moment after it finished.
	resumeSaveWait = 15 * time.Second
)

// resumeError is a failure with the code its reply carries. cost is set when
// model turns were sent (a paid failure), attempt when that was recorded in the
// job's attempts.jsonl, and elig when the failure is a verdict on a version.
type resumeError struct {
	code    string
	msg     string
	cost    *resumestore.Cost
	attempt bool
	elig    *resumeEligibility
}

func (e *resumeError) Error() string { return e.msg }

func jrErr(code, format string, a ...any) *resumeError {
	return &resumeError{code: code, msg: fmt.Sprintf(format, a...)}
}

type resumeErrorReply struct {
	Error           string             `json:"error"`
	Code            string             `json:"code"`
	Cost            *resumestore.Cost  `json:"cost,omitempty"`
	AttemptRecorded bool               `json:"attempt_recorded,omitempty"`
	Eligibility     *resumeEligibility `json:"eligibility,omitempty"`
}

// asResumeError turns any failure of a resume command into its coded form:
// rkbresume's and resumeConfig's coded errors keep their codes, a finished
// context is the owner's Stop or the deadline, and anything else is "failed".
func asResumeError(err error) *resumeError {
	var re *resumeError
	var rk *rkbresume.Error
	var ce *codedError
	switch {
	case errors.As(err, &re):
		return re
	case errors.As(err, &rk):
		return &resumeError{code: rk.Code, msg: rk.Msg}
	case errors.As(err, &ce):
		return &resumeError{code: ce.code, msg: ce.msg}
	case errors.Is(err, context.DeadlineExceeded):
		return jrErr(rkbresume.CodeTimeout, "jobs resume: not finished in time")
	case errors.Is(err, context.Canceled):
		return jrErr(rkbresume.CodeCancel, "jobs resume: stopped")
	}
	return jrErr(jrFailed, "jobs resume: %s", oneLine(err.Error(), 300))
}

// storeError names a resumestore failure. Its message carries the store's own
// words (ids, versions, file names; never resume text).
func storeError(err error) *resumeError {
	code := jrStore
	switch {
	case errors.Is(err, resumestore.ErrInvalid):
		code = jrBadRequest
	case errors.Is(err, resumestore.ErrNotFound):
		code = jrNotFound
	case errors.Is(err, resumestore.ErrDamaged):
		code = jrDamaged
	case errors.Is(err, resumestore.ErrSchema):
		code = jrUnsupported
	case errors.Is(err, resumestore.ErrBusy):
		code = rkbresume.CodeBusy
	case errors.Is(err, resumestore.ErrFull):
		code = jrFull
	case errors.Is(err, resumestore.ErrNotUsable):
		code = jrNotUsable
	}
	return jrErr(code, "jobs resume: %s", oneLine(err.Error(), 300))
}

// resumeEmit writes v as the one JSON reply, or the coded error reply when err
// is set; without --json a failure is a plain error and text prints a success.
// The exit code is the house's: 1 for a reported failure.
func (e *env) resumeEmit(asJSON bool, v any, err error, text func()) error {
	var re *resumeError
	if err != nil {
		re = asResumeError(err)
	}
	if !asJSON {
		if re == nil {
			text()
			return nil
		}
		if re.cost != nil {
			fmt.Fprintf(e.stderr, "vani: the model turns cost about $%.4f%s\n", re.cost.USD, map[bool]string{true: "", false: " or more"}[re.cost.Complete])
		}
		return errors.New(re.msg)
	}
	if re != nil {
		v = resumeErrorReply{Error: re.msg, Code: re.code, Cost: re.cost, AttemptRecorded: re.attempt, Eligibility: re.elig}
	}
	enc := json.NewEncoder(e.stdout)
	enc.SetEscapeHTML(false)
	if werr := enc.Encode(v); werr != nil {
		return werr
	}
	if re != nil {
		return errAlreadyReported
	}
	return nil
}

func (e *env) jobsResume(args []string) error {
	if len(args) == 0 {
		return usagef("jobs resume: expected build, render, show, use, revalidate, diff, current, coverage or gaps")
	}
	switch args[0] {
	case "diff":
		return e.jobsResumeDiff(args[1:])
	case "current":
		return e.jobsResumeCurrent(args[1:])
	case "build":
		return e.jobsResumeBuild(args[1:])
	case "render":
		return e.jobsResumeRender(args[1:])
	case "show":
		return e.jobsResumeShow(args[1:])
	case "use":
		return e.jobsResumeUse(args[1:])
	case "revalidate":
		return e.jobsResumeRevalidate(args[1:])
	case "coverage":
		return e.jobsResumeCoverage(args[1:])
	case "gaps":
		return e.jobsResumeGaps(args[1:])
	}
	return usagef("jobs resume: unknown subcommand %q (expected build, render, show, use, revalidate, diff, current, coverage or gaps)", args[0])
}

// resumeFlags parses a subcommand's flags and positionals; flag.ErrHelp and
// usage errors come back as they are.
func parseResumeArgs(fs *flag.FlagSet, args []string, name string, want ...int) error {
	if err := parsePositional(fs, args); err != nil {
		return err
	}
	for _, n := range want {
		if fs.NArg() == n {
			return nil
		}
	}
	return usagef("jobs resume %s: %s", name, resumeUsage[name])
}

var resumeUsage = map[string]string{
	"build":      "expected one posting key",
	"render":     "expected one posting key (the edit is JSON on stdin)",
	"show":       "expected a posting key and, optionally, a version (v3)",
	"use":        "expected a posting key and a version (v3)",
	"revalidate": "expected a posting key and a version (v3)",
	"diff":       "expected a posting key and two versions (v1 v3)",
	"current":    "expected a posting key and a version (v3)",
}

var versionArg = regexp.MustCompile(`^v([1-9][0-9]{0,3})$`)

// parseVersion reads "v3". Anything else is a bad request, never a guess.
func parseVersion(s string) (int, *resumeError) {
	m := versionArg.FindStringSubmatch(s)
	if m == nil {
		return 0, jrErr(jrBadRequest, "jobs resume: %q is not a version (v1 to v9999)", oneLine(s, 40))
	}
	n, _ := strconv.Atoi(m[1])
	return n, nil
}

// resumeJob is the posting a command works on: its key, its ledger record and
// the request rkbresume builds for.
type resumeJob struct {
	key posting.Key
	rec ledger.Record
	req rkbresume.Request
}

// resumeJobFor checks the key (the store's rules) and that the ledger has the
// posting. needText also requires its description, which build and render send
// to rkb: a posting whose text is gone (cleared after the role closed, or never
// given) cannot be tailored for.
func (e *env) resumeJobFor(rawKey string, needText bool) (resumeJob, *resumeError) {
	if err := resumestore.CheckKey(rawKey); err != nil {
		return resumeJob{}, jrErr(jrBadRequest, "jobs resume: the posting key is not usable: %s", oneLine(err.Error(), 200))
	}
	k := posting.Key(rawKey)
	snap, err := ledger.ReadSnapshotScoped(filepath.Join(e.stateDir, "ledger"), rowScope(func(ledger.Snapshot) (posting.Key, bool) { return k, true }))
	if err != nil {
		return resumeJob{}, jrErr(jrLedger, "jobs resume: read the ledger: %s", oneLine(err.Error(), 200))
	}
	r, ok := findRecord(snap, k)
	switch {
	case !ok:
		return resumeJob{}, jrErr(jrUnknownKey, "jobs resume: no posting %q in the ledger", k)
	case needText && (r.BodyEvicted || strings.TrimSpace(r.Posting.Description) == ""):
		return resumeJob{}, jrErr(jrBadRequest, "jobs resume: the ledger holds no text for %s (it was cleared after the role closed, or the board never gave any), so there is nothing to tailor a resume to", k)
	}
	return resumeJob{key: k, rec: r, req: rkbresume.Request{Company: r.Posting.Company, Title: r.Posting.Title, Description: r.Posting.Description}}, nil
}

// resumeStore opens the store under the state dir, which must be absolute: the
// PDF paths a reply names and the folders builds work in are absolute.
func (e *env) resumeStore() (*resumestore.Store, *resumeError) {
	if !filepath.IsAbs(e.stateDir) {
		return nil, jrErr(codeConfig, "jobs resume: the state directory must be an absolute path")
	}
	st, err := resumestore.Open(e.stateDir)
	if err != nil {
		return nil, storeError(err)
	}
	return st, nil
}

// currentOf is the job's current version, 0 when it has none. A damaged store is
// an error, found before anything is paid for.
func currentOf(st *resumestore.Store, key posting.Key) (int, *resumeError) {
	m, err := st.Current(string(key))
	switch {
	case errors.Is(err, resumestore.ErrNotFound):
		return 0, nil
	case err != nil:
		return 0, storeError(err)
	}
	return m.Version, nil
}

// resumeVersion is a version as a reply shows it: the record without its
// choice (the field below shadows Meta's, and stays out unless set).
type resumeVersion struct {
	resumestore.Meta
	Selection json.RawMessage `json:"selection,omitempty"`
	// PDFPath is the absolute path of the version's PDF (0600), when it has one.
	PDFPath     string             `json:"pdf_path,omitempty"`
	Current     bool               `json:"current,omitempty"`
	Used        bool               `json:"used,omitempty"`
	Eligibility *resumeEligibility `json:"eligibility,omitempty"`
}

// resumeSaved is the reply of a command that saved a version.
type resumeSaved struct {
	resumeVersion
	// CurrentMoved says the saved version is now the one the Studio shows;
	// CurrentNote says why not, when it is not.
	CurrentMoved bool   `json:"current_moved"`
	CurrentNote  string `json:"current_note,omitempty"`
	// DroppedStale lists the bullets a revalidation took off the page.
	DroppedStale []string `json:"dropped_stale,omitempty"`
}

// metaOf is the version record of a build or render: rkbresume's Result marshals
// into the store's Meta field for field (the contract record_test pins), so no
// copy is written here.
func metaOf(res rkbresume.Result) (resumestore.Meta, error) {
	raw, err := json.Marshal(res)
	if err != nil {
		return resumestore.Meta{}, err
	}
	var m resumestore.Meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return resumestore.Meta{}, err
	}
	return m, nil
}

func costOf(c rkbresume.Cost) resumestore.Cost {
	return resumestore.Cost{USD: c.USD, Complete: c.Complete, Turns: c.Turns}
}

// detached is a context for writing what a paid build produced: it outlives a
// Stop that came after the money was spent, but is bounded.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), resumeSaveWait)
}

// paidFailure is the error for a build or render that failed after res was
// learned: it carries the cost when model turns were sent, and records them in
// the job's attempts.jsonl (the money is spent and no version says so).
func (e *env) paidFailure(ctx context.Context, st *resumestore.Store, key posting.Key, res rkbresume.Result, err error) *resumeError {
	re := asResumeError(err)
	if res.Cost.Turns == 0 {
		return re
	}
	c := costOf(res.Cost)
	re.cost = &c
	sctx, cancel := detached(ctx)
	defer cancel()
	if aerr := st.AppendAttempt(sctx, string(key), resumestore.Attempt{Cost: c, Code: re.code}); aerr != nil {
		fmt.Fprintf(e.stderr, "vani: jobs resume: the failed build's cost could not be recorded: %s\n", oneLine(aerr.Error(), 200))
	} else {
		re.attempt = true
	}
	return re
}

// saveVersion keeps res as the next version of the job. The caller has run
// checkBuilt. parent is the version it was made from (0: none), so the current
// version moves only when parent is the current one (Save's rule); source and
// reason, when set, replace the build's own.
func (e *env) saveVersion(ctx context.Context, st *resumestore.Store, key posting.Key, res rkbresume.Result, parent int, source, reason string) (resumeSaved, *resumeError) {
	meta, err := metaOf(res)
	if err != nil {
		return resumeSaved{}, e.paidFailure(ctx, st, key, res, jrErr(jrStore, "jobs resume: the build could not be recorded: %s", oneLine(err.Error(), 200)))
	}
	meta.Parent = parent
	if source != "" {
		meta.Source = source
	}
	if reason != "" {
		meta.Reason = reason
	}
	var pdf []byte
	if len(res.PDF) > 0 {
		pdf = res.PDF
	}
	sctx, cancel := detached(ctx)
	defer cancel()
	saved, cur, err := st.Save(sctx, string(key), meta, pdf)
	if err != nil {
		re := storeError(err)
		if saved.Version != 0 { // committed, but the pointer did not move
			re.msg += fmt.Sprintf(" (v%d was saved)", saved.Version)
			return resumeSaved{}, re
		}
		return resumeSaved{}, e.paidFailure(ctx, st, key, res, re)
	}
	out := resumeSaved{resumeVersion: resumeVersion{Meta: saved}, CurrentMoved: cur}
	if saved.PDFSHA256 != "" {
		if out.PDFPath, err = st.PDFPath(string(key), saved.Version); err != nil {
			return resumeSaved{}, storeError(err)
		}
	}
	return out, nil
}

// ---- build -----------------------------------------------------------------

// resumeEstimate is a build's estimate (llmestimate.go): its Turns, the most model
// turns a build sends (the first and one retry), and the shared fields.
// InputTokens is an upper bound over those turns (rkbresume.PromptBound, and what a
// redraft adds); CapReached says what is left of today's cap is less than the
// first turn reserves at most.
type resumeEstimate struct {
	Turns int `json:"turns"`
	llmEstimate
}

type resumeDryReply struct {
	Key    string `json:"key"`
	DryRun bool   `json:"dry_run"`
	Model  string `json:"model"`
	// OpusUsable says a build would run its model turn; when not, NoModel says why
	// and a build without --allow-base is refused (no_model).
	OpusUsable bool            `json:"opus_usable"`
	NoModel    string          `json:"no_model,omitempty"`
	Current    int             `json:"current,omitempty"`
	Budget     llmBudget       `json:"budget"`
	Estimate   *resumeEstimate `json:"estimate,omitempty"`
	// The redraft the flags ask for (all absent for a plain build): the version it
	// starts from, the scope and locks as given, and the instruction's length in
	// characters, never its text. A dry run checks what needs no rkb (the
	// instruction, the shape of the names, the version); which section or entry a
	// name means is known only once a build reads the bank (bad_scope).
	From             int      `json:"from,omitempty"`
	Scope            string   `json:"scope,omitempty"`
	Lock             []string `json:"lock,omitempty"`
	InstructionRunes int      `json:"instruction_runes,omitempty"`
}

// resumeBuildOpts are the flags of `build` that shape a redraft.
type resumeBuildOpts struct {
	AllowBase bool
	// From is the version ("v3") a redraft starts from. With a scope, a lock or an
	// instruction and no From, the job's current version is the one (an instruction
	// alone on a job with no version is a plain build with a preference).
	From        string
	Scope       string
	Lock        []string
	Instruction string
}

func (e *env) jobsResumeBuild(args []string) error {
	fs := e.newFlags("jobs resume build")
	asJSON := e.jsonFlag(fs, "one JSON object on stdout ({error,code} on failure)")
	dry := fs.Bool("dry-run", false, "say whether Opus is usable and what a build would cost at most, and spend nothing")
	allowBase := fs.Bool("allow-base", false, "when Opus cannot run, build and keep the owner's base selection instead of refusing (it never becomes the current version of a job that has one)")
	opts := resumeBuildOpts{}
	fs.StringVar(&opts.From, "from", "", "redraft from this version (v3): Opus is shown its choice by ids and chooses differently where the posting is better served (default the current version when --scope, --lock or --instruction is given)")
	fs.StringVar(&opts.Scope, "scope", "", "re-pick only this section (its title) or entry (its id) of the version; everything else is kept as it was")
	var locks stringList
	fs.Var(&locks, "lock", "keep this section (its title) as it is (repeatable)")
	fs.StringVar(&opts.Instruction, "instruction", "", "the owner's one-line preference for Opus (at most 500 characters, no email address or phone number); it cannot override the rules, is never printed on the page, and is kept with the version")
	if err := parseResumeArgs(fs, args, "build", 1); err != nil {
		return err
	}
	if *dry && *allowBase {
		return usagef("jobs resume build: --dry-run and --allow-base are exclusive")
	}
	opts.AllowBase, opts.Lock = *allowBase, locks
	ctx, cancel := signalContext()
	defer cancel()
	var out any
	var err error
	if *dry {
		dctx, dcancel := context.WithTimeout(ctx, resumeDryRunDeadline)
		defer dcancel()
		out, err = e.resumeDryRun(dctx, fs.Arg(0), opts)
	} else {
		out, err = e.resumeBuild(ctx, fs.Arg(0), opts)
	}
	return e.resumeEmit(*asJSON, out, err, func() {
		switch v := out.(type) {
		case resumeDryReply:
			if !v.OpusUsable {
				fmt.Fprintf(e.stdout, "%s: %s cannot run (%s); a build is refused unless --allow-base\n", v.Key, v.Model, v.NoModel)
				return
			}
			est := v.Estimate
			fmt.Fprintf(e.stdout, "%s: would ask %s, at most %d turns, at most $%.4f (%s); daily cap $%.2f\n", v.Key, v.Model, est.Turns, est.MaxUSD, est.PriceSource, v.Budget.DailyCapUSD)
			if est.CapReached {
				fmt.Fprintln(e.stdout, "note: what is left of today's cap is less than a turn reserves; the build would be refused")
			}
		case resumeSaved:
			printSaved(e, v)
		}
	})
}

func printSaved(e *env, v resumeSaved) {
	fmt.Fprintf(e.stdout, "%s: saved v%d (%s, fit %s, %d pages)", v.Key, v.Version, v.Source, v.Checks.Fit, v.Pages)
	if v.CurrentMoved {
		fmt.Fprint(e.stdout, "; now current")
	} else if v.CurrentNote != "" {
		fmt.Fprintf(e.stdout, "; %s", v.CurrentNote)
	}
	fmt.Fprintln(e.stdout)
	if v.PDFPath != "" {
		fmt.Fprintf(e.stdout, "pdf: %s\n", v.PDFPath)
	}
}

// redraftOf is the redraft the flags ask for: the version it starts from (0 for a
// plain build) and what rkbresume is told. Everything that needs no rkb is checked
// here, before anything is spent: the version (it must read), the combination of
// flags, and (through rkbresume.CheckTailor) the instruction and the names.
func redraftOf(st *resumestore.Store, key posting.Key, cur int, o resumeBuildOpts) (rkbresume.Tailor, int, *resumeError) {
	t := rkbresume.Tailor{Scope: o.Scope, Lock: o.Lock, Instruction: o.Instruction}
	from := 0
	switch {
	case o.From != "":
		n, perr := parseVersion(o.From)
		if perr != nil {
			return t, 0, perr
		}
		from = n
	case o.Scope != "" || len(o.Lock) > 0 || o.Instruction != "":
		from = cur
	}
	if from != 0 {
		m, _, err := st.Get(string(key), from)
		if err != nil {
			return t, 0, storeError(err)
		}
		t.Previous = m.Selection
	}
	if from == 0 && (o.Scope != "" || len(o.Lock) > 0) {
		return t, 0, jrErr(jrBadRequest, "jobs resume build: --scope and --lock keep parts of an existing version, and this job has none (--from names one)")
	}
	t, err := rkbresume.CheckTailor(t, o.AllowBase)
	if err != nil {
		return t, 0, asResumeError(err)
	}
	return t, from, nil
}

// resumeBuild is the paid command: Opus picks the bullets and the version is
// saved. A damaged store is found before anything is spent. With a redraft (--from,
// --scope, --lock, --instruction) the new version is made from the one it started
// from, so it takes the current one's place only when that was the current one.
func (e *env) resumeBuild(ctx context.Context, rawKey string, o resumeBuildOpts) (resumeSaved, error) {
	job, jerr := e.resumeJobFor(rawKey, true)
	if jerr != nil {
		return resumeSaved{}, jerr
	}
	st, serr := e.resumeStore()
	if serr != nil {
		return resumeSaved{}, serr
	}
	cur, serr := currentOf(st, job.key)
	if serr != nil {
		return resumeSaved{}, serr
	}
	tailor, from, rerr := redraftOf(st, job.key, cur, o)
	if rerr != nil {
		return resumeSaved{}, rerr
	}
	w, err := e.resumeConfig(ctx, true)
	if err != nil {
		return resumeSaved{}, err
	}
	rc := w.RC
	rc.AllowBase, rc.Tailor = o.AllowBase, tailor
	res, err := rkbresume.Build(ctx, rc, job.req)
	if err != nil {
		return resumeSaved{}, e.paidFailure(ctx, st, job.key, res, err)
	}
	if err := checkBuilt(res); err != nil {
		return resumeSaved{}, e.paidFailure(ctx, st, job.key, res, err)
	}
	// A new version is made from the current one, so it takes its place. The base
	// selection is a fallback, not a choice: it is kept but never displaces a
	// current version (no Parent, so the store leaves the pointer where it is).
	parent, note := cur, ""
	if from != 0 {
		parent = from // a redraft is made from the version it started from
	}
	if res.Source == rkbresume.SourceBase && cur != 0 {
		parent = 0
		note = fmt.Sprintf("v%d stays current: this is the owner's base selection, a fallback (%s)", cur, oneLine(res.Reason, 200))
	}
	out, serr := e.saveVersion(ctx, st, job.key, res, parent, "", "")
	if serr != nil {
		return resumeSaved{}, serr
	}
	if !out.CurrentMoved {
		out.CurrentNote = note
		switch {
		case out.CurrentNote != "":
		case parent != 0 && parent != cur:
			out.CurrentNote = fmt.Sprintf("v%d stays current: this version was made from v%d", cur, parent)
		default:
			out.CurrentNote = fmt.Sprintf("v%d stays current: a newer version was made while this one was built", cur)
		}
	}
	if w.VMNote != "" {
		out.Notes = append(out.Notes, w.VMNote)
	}
	return out, nil
}

// resumeDryRun says what a build would do and cost, and spends nothing: it
// builds the provider (which resolves a credential, sends nothing) to know
// whether Opus is usable and at what price, and runs no rkb.
func (e *env) resumeDryRun(ctx context.Context, rawKey string, o resumeBuildOpts) (resumeDryReply, error) {
	job, jerr := e.resumeJobFor(rawKey, true)
	if jerr != nil {
		return resumeDryReply{}, jerr
	}
	st, serr := e.resumeStore()
	if serr != nil {
		return resumeDryReply{}, serr
	}
	cur, serr := currentOf(st, job.key)
	if serr != nil {
		return resumeDryReply{}, serr
	}
	tailor, from, rerr := redraftOf(st, job.key, cur, o)
	if rerr != nil {
		return resumeDryReply{}, rerr
	}
	w, err := e.resumeConfig(ctx, true)
	if err != nil {
		return resumeDryReply{}, err
	}
	rep := resumeDryReply{Key: string(job.key), DryRun: true, Model: rkbresume.Model, Current: cur,
		From: from, Scope: tailor.Scope, Lock: tailor.Lock, InstructionRunes: len([]rune(tailor.Instruction))}
	wd := &wired{cfg: w.Cfg, stateDir: e.stateDir, warn: e.stderr}
	rep.Budget = readBudget(ctx, wd.budgetLedgerPath(), w.Cfg.Decide.DailyUSD.LLM)
	p := w.RC.Provider
	if p == nil {
		rep.NoModel = w.RC.NoModel
		return rep, nil
	}
	prices, ok := tablePrices(p, rkbresume.Model)
	if !ok { // resumeModel checked this; a provider that changed its mind is not usable
		rep.NoModel = "the provider has no price for " + rkbresume.Model
		return rep, nil
	}
	rep.OpusUsable = true
	req := llm.Request{Model: rkbresume.Model, MaxTokens: rkbresume.TurnMaxTokens}
	llmbackend.ShapeHigh(p, &req)
	runes := len([]rune(job.req.Description))
	est := &resumeEstimate{Turns: rkbresume.MaxTurns}
	est.MaxOutputTokens, est.PriceSource = int64(req.MaxTokens), "Anthropic's published rates for "+rkbresume.Model
	var firstMax float64
	for turn := 1; turn <= est.Turns; turn++ {
		in := int64(rkbresume.PromptBound(turn, runes)+tailor.PromptBytes()+kbMatchBytesPerTok-1) / kbMatchBytesPerTok
		est.InputTokens += in
		usd := float64(in*prices.Rates.Input+est.MaxOutputTokens*prices.Rates.Output) / nanoPerUSD
		est.MaxUSD += usd
		if turn == 1 {
			firstMax = usd
		}
	}
	est.CapReached = rep.Budget.reached(firstMax)
	rep.Estimate = est
	return rep, nil
}
