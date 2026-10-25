// Package rkbresume builds the resume `vani resume` hands to an application
// form: rkb, the owner's resume tool, builds it; DevPrism only ranks postings.
//
// What it does, in order, for one job:
//
//  1. Loads rkb's bank, a fresh `rkb claims export`, and every selection the
//     owner keeps current (config kb.selections), and asks kbsync.Eligible
//     which bullets may reach an employer. That is the only owner of the rule.
//  2. Loads the base selection (kb.base_selection) raw. Its header, meta, layout,
//     "lines" sections and per-entry overrides are never touched, so the page
//     keeps the owner's template and header; the header is rkb's own, from the
//     bank, and never passes through Vani or a prompt.
//  3. Chooses content with one bounded, tool-less model turn on the configured
//     model. The model sees the posting framed as untrusted text, the base
//     skeleton (section titles, entry ids and titles) and the eligible pool
//     (bullet id, resolved plain text, entry id and title). It answers with
//     ids. The answer is validated strictly: a bullet must be Allowed under
//     its entry, ids are unique, counts are bounded, keywords occur in the
//     posting. Anything invalid falls back to the base selection (filtered to
//     eligible bullets), and the Result says which happened. A model that could
//     not run at all (no provider, no budget, unreachable, timed out) falls back
//     only when Config.AllowBase is set; otherwise Build fails with no_model.
//  4. Writes the chosen selection into a private temp dir and runs rkb on it:
//     `claims export --used-in`, `lint` (draft, not --final), `render` and
//     `fit --pages N --strict`. It does not use `rkb build`: that also writes
//     LaTeX into the career tree. A bullet whose figures rkb has not double
//     verified would print highlighted yellow in the PDF, so such bullets are
//     dropped (and listed in Result.UnverifiedDropped), never shipped.
//  5. A lint error or a fit failure of the model's choice gets at most one
//     retry in which the model is told the problem (ids and rkb's message, no
//     page text); then the base selection is built. If the base fails too, the
//     error is coded. A missing Chrome or pdftotext means fit could not run:
//     that is an error, never a pass.
//
// Render (the owner's edit, no model) and Pool (what the editor may place) are
// the other entry points; every Result carries the record of how its page was
// made (record.go) in the shape the resume store keeps.
//
// Personal data: the base selection's header is only copied into a file rkb
// reads; it is never put in a prompt. The tailored selection's
// meta.output_basename is a constant, so no rkb output line names the owner.
// Errors and retry feedback carry rkb's stderr and ERROR lines, clipped; every
// string of the header the base uses (its inline header, or the bank header it
// names, read from the bank file only to be masked) is replaced in rkb's output
// first (scrubber), so a message that quoted the header cannot carry it out.
package rkbresume

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/rkbexec"
)

// The rkb runner is rkbexec's: these names are what this package's callers and
// tests have always used for it.
type (
	// Command is one rkb invocation. Env is the complete environment.
	Command = rkbexec.Command
	// Output is what a finished rkb step left; a non-zero Exit is not an error.
	Output = rkbexec.Output
	// Runner runs one rkb step under a timeout. It is the seam tests fake.
	Runner = rkbexec.Runner
)

// ErrStepTimeout means one rkb step outlived its own bound.
var ErrStepTimeout = rkbexec.ErrStepTimeout

// Codes classify a failure for the caller (the Mac app, the browser extension).
const (
	CodeConfig   = "config"
	CodeKB       = "kb"
	CodeRender   = "render"
	CodeLint     = "lint"
	CodeFit      = "fit"
	CodeTooLarge = "too_large"
	CodePages    = "pages"
	CodeTimeout  = "timeout"
	CodeCancel   = "cancelled"
	// CodeBusy means another resume build holds the build lock (Config.LockDir).
	// Nothing was spent.
	CodeBusy = "busy"
	// CodeKBChanged means the knowledge base's claims changed after this build
	// read them; nothing was rendered from the mixed state.
	CodeKBChanged = "kb_changed"
	// CodeNoModel means the model could not run and Config.AllowBase is off, so no
	// base resume was built in its place.
	CodeNoModel = "no_model"
	// CodeIneligible means an edit names a bullet that is no longer eligible.
	CodeIneligible = "ineligible"
)

// Bounds. All are ceilings.
const (
	// DefaultDeadline is the whole build: room for a tailored build, one retry
	// and nothing else (minForModel). The Mac app's agent allows a resume job
	// 375 s (Agent.swift JobKind.timeout), so the build ends before it.
	DefaultDeadline = 360 * time.Second
	MaxDeadline     = 10 * time.Minute
	DefaultMaxPDF   = 512 << 10
	// MaxPages bounds the base selection's meta.pages.
	MaxPages = 2

	exportTimeout = 30 * time.Second
	lintTimeout   = 30 * time.Second
	renderTimeout = 30 * time.Second
	fitTimeout    = 100 * time.Second

	maxReadHTML = 4 << 20
)

// Timing knobs; variables only so tests can shrink them.
var (
	// turnTimeout bounds one model turn. The turn runs at high effort
	// (llmbackend.ShapeHigh), which thinks before it answers.
	turnTimeout = 90 * time.Second
	// minForModel is the time a model turn and a tailored build need, with room
	// left for the base build that follows a failure.
	// It is a model turn (90 s) plus a tailored build (60 s) plus minForBase.
	minForModel = 195 * time.Second
	// minForBase is the least time the base build is started with: rkb's fit
	// alone took 15 s on an idle Mac and over a minute under load.
	minForBase = 45 * time.Second
)

// Error is a coded failure. Msg carries no personal data.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func fail(code, format string, a ...any) *Error {
	return &Error{Code: code, Msg: "resume: " + fmt.Sprintf(format, a...)}
}

// Model is the one model that answers a resume turn. It is the owner's
// decision: Claude Opus 5.5 is the only model for resumes, whatever llm.model
// configures for the rest of Vani. The caller wires it into Config.Model and
// checks the provider serves and prices it before any turn is spent.
const Model = "claude-opus-5-5"

// Request is the job to build for. Every field is untrusted page text.
type Request struct {
	Company     string
	Title       string
	Description string
}

// Config configures Build. Paths are absolute.
type Config struct {
	RKB           string   // rkb binary (kb.rkb)
	CodeRoot      string   // rkb's RKB_CODE_ROOT (kb.code_root); empty lets rkb search
	Bank          string   // rkb bank.json (kb.bank)
	Selections    []string // kb.selections
	BaseSelection string   // kb.base_selection; one of Selections
	// VMReport is the validator report `rkb claims export` reads, so figures
	// the validator backs count as verified. Empty exports without one, and
	// every figure that has no manual verification is then dropped as unverified.
	VMReport string

	// Provider answers the model turn on Model. Nil means no model: NoModel (a
	// short reason) says why. Without AllowBase Build then fails with CodeNoModel
	// before anything is spent; with it, the base selection is built and the Result
	// says Source "base" with that reason.
	Provider llm.Provider
	Model    string
	NoModel  string
	// AllowBase lets Build fall back to the base selection when the model could not
	// run (no provider, no time, a budget that refuses, a model that cannot be
	// reached or does not answer in time). Off, those are CodeNoModel errors: a
	// tailored resume was asked for and none is made silently. A model that answered
	// with a choice that was refused or rejected falls back to the base either way,
	// with the reason in the Result.
	AllowBase bool
	// Tailor is what a redraft adds to a build (redraft.go); the zero value is a
	// plain build. Render and Pool ignore it. It cannot be combined with AllowBase
	// when it keeps something (a scope or a lock).
	Tailor Tailor

	Runner   Runner        // nil means rkbexec.ExecRunner
	MaxPDF   int           // default DefaultMaxPDF
	Deadline time.Duration // default DefaultDeadline
	// ToolDirs are searched for pdftotext when the child's PATH lacks it. The
	// Mac app's agent runs with the system folders only, so nil means Homebrew's
	// two prefixes.
	ToolDirs []string

	// WorkDir, when set, is where build folders are made (mode 0700, prefix
	// buildDirPrefix), and where Build first removes such folders left by a
	// build that was killed. Empty uses the system temp folder and sweeps
	// nothing. rkb's own temp folders (its Chrome profile and fit work folders)
	// are made inside the build folder either way, so they go with it.
	WorkDir string
	// LockDir, when set, makes builds exclusive: Build takes a non-blocking
	// "resume build" lock there before anything is spent (held: CodeBusy), and the
	// fit step waits for a "chrome" lock, so no two resumes drive Chrome at once.
	// Empty takes no lock.
	LockDir string
}

// Result is a built resume and the record of how it was built. Every field
// after Pages maps onto a resumestore.Meta field of the same JSON tag (see
// record.go); the CLI copies them across.
//
// On an error Build and Render still return a Result when something was
// learned or spent before the failure: no PDF, but Cost (a paid build that
// failed or was cancelled must be recorded), Model, Checks (what ran) and
// Fingerprints. Errors before the build began (bad config, a held lock) return
// the zero Result.
type Result struct {
	PDF []byte `json:"-"`
	// HTMLPath is where Render without fit left rkb's page (a file the caller
	// must remove); empty otherwise.
	HTMLPath string `json:"-"`
	// BuildDir is the folder HTMLPath is in, when Render left one for its caller to
	// remove (Render without fit); empty otherwise.
	BuildDir string `json:"-"`
	// Pages is 0 when the page was not fitted (Render without fit).
	Pages int `json:"pages,omitempty"`
	// Source is SourceTailored when the model's choice was built, SourceBase
	// when the owner's base selection was, SourceEdited for Render. Reason says
	// why base, in a few words (empty when tailored).
	Source string `json:"source"`
	Reason string `json:"reason,omitempty"`
	// Model is the model id when a model turn was sent.
	Model string `json:"model,omitempty"`
	Cost  Cost   `json:"cost"`
	// Instruction is the owner's preference a tailored build was asked with, as it
	// was sent (scrubbed of the header's strings); empty for a base or edited page,
	// which no model shaped.
	Instruction string `json:"instruction,omitempty"`
	// Scope and Lock are what the redraft kept, as normalized (names only): the
	// section title or entry id re-picked alone, and the sections locked. Empty for
	// a build that kept nothing and for a base or edited page, which no model shaped
	// (like Instruction).
	Scope string   `json:"scope,omitempty"`
	Lock  []string `json:"lock,omitempty"`
	// Checks says how each check ended; a check that did not run says so.
	Checks Checks `json:"checks"`
	// Notes are layout flaws accepted on the page (an orphan last line, an
	// inner page that ends early), as rkb reported them: ids and counts, no
	// page text.
	Notes []string `json:"notes,omitempty"`
	// UnverifiedDropped lists bullet ids left off because rkb has not double
	// verified a figure in them.
	UnverifiedDropped []string `json:"unverified_dropped,omitempty"`
	// Keywords are the job keywords the choice carries; none when the base
	// selection's own are kept.
	Keywords []string `json:"jd_keywords,omitempty"`
	// SelectionJSON is the choice that was built, in the owner-edit shape
	// (choice.editJSON): what Render accepts as its edit. Never a header, bank,
	// meta or path.
	SelectionJSON json.RawMessage `json:"selection"`
	// Bullets is every bullet on the page, in page order.
	Bullets []BulletRef `json:"bullets"`
	// FitLines is bullet id -> rendered lines, from rkb's fit report (ids and
	// counts only); nil when the page was not fitted or Chrome measured none.
	FitLines     map[string]int `json:"fit_lines,omitempty"`
	Fingerprints Fingerprints   `json:"fingerprints"`
}

func (c Config) normalize() (Config, *Error) {
	if !filepath.IsAbs(c.RKB) {
		return c, fail(CodeConfig, "kb.rkb must be the absolute path of the rkb binary")
	}
	if !filepath.IsAbs(c.Bank) {
		return c, fail(CodeConfig, "kb.bank must be the absolute path of rkb's bank.json")
	}
	if c.CodeRoot != "" && !filepath.IsAbs(c.CodeRoot) {
		return c, fail(CodeConfig, "kb.code_root must be an absolute path")
	}
	if len(c.Selections) == 0 {
		return c, fail(CodeConfig, "kb.selections names no rkb selection, so no bullet is eligible for a resume")
	}
	if len(c.Selections) > 32 {
		return c, fail(CodeConfig, "kb.selections names more than 32 selections")
	}
	base := ""
	for _, s := range c.Selections {
		if !filepath.IsAbs(s) {
			return c, fail(CodeConfig, "kb.selections holds a path that is not absolute")
		}
		if filepath.Clean(s) == filepath.Clean(c.BaseSelection) {
			base = s
		}
	}
	if c.BaseSelection == "" || base == "" {
		return c, fail(CodeConfig, "kb.base_selection must be one of kb.selections")
	}
	if c.VMReport != "" && !filepath.IsAbs(c.VMReport) {
		return c, fail(CodeConfig, "the validator report path must be absolute")
	}
	if c.WorkDir != "" && !filepath.IsAbs(c.WorkDir) {
		return c, fail(CodeConfig, "the resume work folder must be an absolute path")
	}
	if c.LockDir != "" && !filepath.IsAbs(c.LockDir) {
		return c, fail(CodeConfig, "the resume lock folder must be an absolute path")
	}
	if c.Runner == nil {
		c.Runner = rkbexec.ExecRunner{}
	}
	switch {
	case c.MaxPDF == 0:
		c.MaxPDF = DefaultMaxPDF
	case c.MaxPDF < 0:
		return c, fail(CodeConfig, "the PDF size cap is negative")
	}
	switch {
	case c.Deadline == 0:
		c.Deadline = DefaultDeadline
	case c.Deadline < 0 || c.Deadline > MaxDeadline:
		return c, fail(CodeConfig, "the build deadline is outside (0, %s]", MaxDeadline)
	}
	if c.Provider != nil && c.Model == "" {
		return c, fail(CodeConfig, "a provider needs a model")
	}
	t, terr := normalizeTailor(c.Tailor, c.AllowBase)
	if terr != nil {
		return c, terr
	}
	c.Tailor = t
	return c, nil
}

// Build builds one resume for req. The returned error, when not nil, is an
// *Error.
func Build(ctx context.Context, cfg Config, req Request) (Result, error) {
	cfg, nerr := cfg.normalize()
	if nerr != nil {
		return nothing(), nerr
	}
	if cfg.Provider == nil && !cfg.AllowBase {
		reason := cfg.NoModel
		if reason == "" {
			reason = "no model is configured"
		}
		return nothing(), fail(CodeNoModel, "the model could not run (%s), so no tailored resume was built", reason)
	}
	ctx, b, done, berr := begin(ctx, cfg, true)
	if berr != nil {
		return nothing(), berr
	}
	defer done(false)
	res, berr := b.run(ctx, req)
	if berr != nil {
		res = b.partial()
	}
	b.cost.stamp(&res)
	if berr != nil {
		return res, berr
	}
	return res, nil
}

// begin readies one build on a normalized cfg: it takes the build lock when lock
// is set (held elsewhere: CodeBusy, nothing spent), bounds ctx by cfg.Deadline and makes the
// private build folder (sweeping stale ones first). done releases it all: it
// removes the folder unless keep (Render without fit hands the page to its
// caller in it).
func begin(ctx context.Context, cfg Config, lock bool) (context.Context, *builder, func(keep bool), *Error) {
	var unlock func()
	if lock && cfg.LockDir != "" {
		l, err := filelock.TryAcquire(filepath.Join(cfg.LockDir, buildLockName))
		switch {
		case errors.Is(err, filelock.ErrWouldBlock):
			return ctx, nil, nil, fail(CodeBusy, "another resume is being built; wait for it to finish")
		case err != nil:
			return ctx, nil, nil, fail(CodeConfig, "the resume build lock could not be taken: %v", err)
		}
		unlock = func() { _ = l.Release() }
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Deadline)
	dir, err := makeBuildDir(cfg.WorkDir, time.Now())
	if err != nil {
		cancel()
		if unlock != nil {
			unlock()
		}
		return ctx, nil, nil, fail(CodeRender, "no private temp folder: %v", err)
	}
	done := func(keep bool) {
		cancel()
		if !keep {
			_ = os.RemoveAll(dir)
		}
		if unlock != nil {
			unlock()
		}
	}
	return ctx, &builder{cfg: cfg, dir: dir, last: notRun()}, done, nil
}

// ctxError turns a finished context into the coded error for it.
func ctxError(ctx context.Context) *Error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fail(CodeTimeout, "the resume was not finished in time")
	}
	return fail(CodeCancel, "the resume build was cancelled")
}

// claimsRunner is the Runner kbsync.RKB runs `rkb claims export` through: the
// build's own Runner, in the environment every other rkb step gets (a PATH that
// finds pdftotext), with everything rkb says scrubbed of the owner's header
// before kbsync quotes it in an error.
type claimsRunner struct {
	r     Runner
	env   []string
	scrub scrubber
}

func (a claimsRunner) Run(ctx context.Context, c Command, timeout time.Duration) (Output, error) {
	if a.env != nil {
		c.Env = a.env
	}
	out, err := a.r.Run(ctx, c, timeout)
	out.Stdout, out.Stderr = a.scrub.clean(out.Stdout), a.scrub.clean(out.Stderr)
	if err != nil {
		return out, errors.New(a.scrub.clean(err.Error()))
	}
	return out, nil
}

// clip shortens s to n runes on one line, for an error message.
func clip(s string, n int) string {
	s = strings.ToValidUTF8(strings.TrimSpace(s), "?")
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
