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
//     posting. Anything invalid, no provider, an exhausted budget or a timeout
//     falls back to the base selection (filtered to eligible bullets), and the
//     Result says which happened.
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
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/kbsync"
)

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

	// Provider answers the model turn on Model. Nil means no model: the base
	// selection is built and NoModel (a short reason) is reported.
	Provider llm.Provider
	Model    string
	NoModel  string

	Runner   Runner        // nil means ExecRunner
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
		c.Runner = ExecRunner{}
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
	return c, nil
}

// Build builds one resume for req. The returned error, when not nil, is an
// *Error.
func Build(ctx context.Context, cfg Config, req Request) (Result, error) {
	cfg, nerr := cfg.normalize()
	if nerr != nil {
		return Result{}, nerr
	}
	if cfg.LockDir != "" {
		l, err := filelock.TryAcquire(filepath.Join(cfg.LockDir, buildLockName))
		switch {
		case errors.Is(err, filelock.ErrWouldBlock):
			return Result{}, fail(CodeBusy, "another resume is being built; wait for it to finish")
		case err != nil:
			return Result{}, fail(CodeConfig, "the resume build lock could not be taken: %v", err)
		}
		defer l.Release()
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Deadline)
	defer cancel()
	dir, err := makeBuildDir(cfg.WorkDir, time.Now())
	if err != nil {
		return Result{}, fail(CodeRender, "no private temp folder: %v", err)
	}
	defer os.RemoveAll(dir)
	b := &builder{cfg: cfg, dir: dir, last: notRun()}
	res, berr := b.run(ctx, req)
	if berr != nil {
		return b.partial(), berr
	}
	return res, nil
}

// ctxError turns a finished context into the coded error for it.
func ctxError(ctx context.Context) *Error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fail(CodeTimeout, "the resume was not finished in time")
	}
	return fail(CodeCancel, "the resume build was cancelled")
}

// claimsRunner lets kbsync.RKB run rkb through this package's Runner, in the
// environment every other rkb step gets (a PATH that finds pdftotext).
type claimsRunner struct {
	r     Runner
	env   []string
	scrub scrubber
}

func (a claimsRunner) Run(ctx context.Context, c kbsync.Command) error {
	if a.env != nil {
		c.Env = a.env
	}
	out, err := a.r.Run(ctx, c, exportTimeout)
	if err != nil {
		return fmt.Errorf("%w: rkb: %s", kbsync.ErrResolve, a.scrub.clean(err.Error()))
	}
	if out.Exit != 0 {
		return fmt.Errorf("%w: rkb exited %d: %s", kbsync.ErrResolve, out.Exit, clip(a.scrub.clean(out.Stderr), 300))
	}
	return nil
}
