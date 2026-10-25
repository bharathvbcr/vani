package rkbresume

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/childenv"
	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// CodeFigures is the code for a resume that cannot be built without a figure
// rkb has not double-verified.
const CodeFigures = "figures"

type builder struct {
	cfg Config
	dir string
	env []string
	// bankPath is the build's own copy of bank.json, made once in load. Every rkb
	// step reads this copy (the selection's bank points at it), so the bank Vani
	// judged eligibility on is the bank rkb prints, even if the owner edits the
	// live one meanwhile.
	bankPath string
	bank     kbsync.Bank
	claims   kbsync.Claims
	el       kbsync.Eligibility
	base     *baseSel
	// cont: the bullets that continue the one before them (continuation.go).
	cont continuations
	// homes: for each entry, the base sections it may be placed in (entryHomes).
	homes map[string]map[int]bool
	// scrub removes the owner's header strings from text that came from rkb.
	scrub scrubber
	// fam groups entries that describe the same work (family.go).
	fam families
}

// isCont reports whether a bank bullet's text refers back to an earlier bullet.
func (b *builder) isCont(id string) bool {
	bl, ok := b.bank.Bullets[id]
	return ok && isContinuation(bl.Text)
}

// problem is a reason one build was rejected that the model can act on. kind is
// lint, fit, figures, size or pages.
type problem struct{ kind, msg string }

func (p problem) code() string {
	switch p.kind {
	case "lint":
		return CodeLint
	case "figures":
		return CodeFigures
	case "size":
		return CodeTooLarge
	case "pages":
		return CodePages
	}
	return CodeFit
}

// outcome is one finished build: a PDF, or the problems that rejected it.
type outcome struct {
	pdf      []byte
	pages    int
	dropped  []string
	problems []problem
	// notes are soft layout flaws rkb reported (see attempt).
	notes []string
	// softFit is set on a tailored build whose only flaw is a soft layout one:
	// pdf is filled, and problems holds that one "fit" problem.
	softFit string
}

func (b *builder) remaining(ctx context.Context) time.Duration {
	if dl, ok := ctx.Deadline(); ok {
		return time.Until(dl)
	}
	return MaxDeadline
}

func (b *builder) run(ctx context.Context, req Request) (Result, *Error) {
	if ctx.Err() != nil {
		return Result{}, ctxError(ctx)
	}
	env, err := b.childEnv()
	if err != nil {
		return Result{}, err
	}
	// rkb makes its Chrome profile and fit work folders in its temp folder, and
	// cleans them only when it exits normally: inside the build folder they go
	// with it, whatever happens to rkb.
	b.env = withTempDir(env, filepath.Join(b.dir, tmpDirName))
	if err := b.load(ctx); err != nil {
		return Result{}, err
	}
	baseCh := b.base.baseChoice(b.el.Allowed, b.isCont)
	if baseCh.count() == 0 {
		return Result{}, fail(CodeKB, "no bullet of the base selection is eligible (%d listed bullets are not)", len(b.el.Why))
	}
	// One export of the base: it proves rkb runs before a model turn is spent,
	// and names the figures rkb has not verified, so the model is not offered
	// bullets that would be dropped (badFigures).
	probeDir := filepath.Join(b.dir, "probe")
	if err := os.MkdirAll(probeDir, 0o700); err != nil {
		return Result{}, fail(CodeRender, "no private temp folder: %v", err)
	}
	cf, err := b.export(ctx, probeDir, baseCh)
	if err != nil {
		return Result{}, err
	}
	badClaims := b.badFigures(cf, baseCh)

	reason := b.cfg.NoModel
	switch {
	case b.cfg.Provider == nil:
		if reason == "" {
			reason = "no model is configured"
		}
	case b.remaining(ctx) < minForModel:
		reason = "not enough time was left for a model turn"
	default:
		res, why, err := b.tailor(ctx, req, baseCh, badClaims)
		if err != nil {
			return Result{}, err
		}
		if res != nil {
			return *res, nil
		}
		reason = why
	}
	if ctx.Err() != nil {
		return Result{}, ctxError(ctx)
	}
	if b.remaining(ctx) < minForBase {
		return Result{}, fail(CodeTimeout, "not enough time was left to build the base resume")
	}
	out, err := b.attempt(ctx, "base", baseCh, true)
	if err != nil {
		return Result{}, err
	}
	if len(out.problems) > 0 {
		p := out.problems[0]
		why := summarize(out.problems)
		if len(out.dropped) > 0 {
			why += "; left off as unverified: " + strings.Join(out.dropped, ", ")
		}
		return Result{}, fail(p.code(), "the base selection could not be built (%s)", why)
	}
	return Result{PDF: out.pdf, Pages: out.pages, Selection: SelectionBase, Reason: reason, UnverifiedDropped: out.dropped, Notes: out.notes}, nil
}

// badFigures names the claims the pool must not offer. rkb's export covers every
// claim, but double_verified is judged against the sentences the probed
// selection uses (claims.go: P2OK is len(sentences) > 0), so a claim the base
// does not use reads "not verified" only because nothing used it. A claim is
// therefore bad when rkb gives a reason against it (Problems: pass 1 missing, a
// failed validator, an unconfirmed sentence), or when the base uses it and it is
// not double-verified. A figure rkb does not know at all is bad too: it cannot
// render. Anything else is decided when the chosen bullets are exported
// (unverifiedBullets), which is exact because that export uses them.
func (b *builder) badFigures(cf claimsFile, baseCh choice) map[string]bool {
	bad := map[string]bool{}
	inBase := map[string]bool{}
	for _, id := range baseCh.bulletIDs() {
		for _, t := range tokens(b.bank.Bullets[id].Text) {
			inBase[t] = true
		}
	}
	for t, c := range cf.Claims {
		if !c.DoubleVerified && (len(c.Problems) > 0 || inBase[t]) {
			bad[t] = true
		}
	}
	for id, ok := range b.el.Bullets {
		if !ok {
			continue
		}
		for _, t := range tokens(b.bank.Bullets[id].Text) {
			if _, known := cf.Claims[t]; !known {
				bad[t] = true
			}
		}
	}
	return bad
}

func summarize(ps []problem) string {
	var parts []string
	for i, p := range ps {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("%d more", len(ps)-i))
			break
		}
		parts = append(parts, p.kind+": "+clip(p.msg, 200))
	}
	return strings.Join(parts, "; ")
}

// load reads the bank, fresh claims and every selection, and applies the
// eligibility rule.
func (b *builder) load(ctx context.Context) *Error {
	// The bank is read once, bounded, and kept as a private copy: the same bytes
	// are parsed here and handed to every rkb step.
	raw, err := readCapped(b.cfg.Bank, kbsync.MaxBankBytes)
	if err != nil {
		return fail(CodeKB, "bank: %v", err)
	}
	bank, err := kbsync.Parse(raw)
	if err != nil {
		return fail(CodeKB, "%v", err)
	}
	b.bankPath = filepath.Join(b.dir, "bank.json")
	if err := os.WriteFile(b.bankPath, raw, 0o600); err != nil {
		return fail(CodeRender, "write the bank snapshot: %v", err)
	}
	b.bank = bank
	// The base comes before the first rkb call so the owner's header strings are
	// known to the scrubber for everything rkb says from here on.
	base, berr := loadBase(b.cfg.BaseSelection, MaxPages)
	if berr != nil {
		return berr
	}
	b.base = base
	b.scrub = newScrubber(headerStrings(base, b.bankPath))
	claims, err := kbsync.RKB{Binary: b.cfg.RKB, CodeRoot: b.cfg.CodeRoot, Runner: claimsRunner{b.cfg.Runner, b.env, b.scrub}}.Claims(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctxError(ctx)
		}
		return fail(CodeKB, "claims: %v", err)
	}
	b.claims = claims
	sels, err := kbsync.LoadSelections(b.cfg.Selections)
	if err != nil {
		return fail(CodeKB, "%v", err)
	}
	b.el = kbsync.Eligible(bank, claims, sels)
	b.cont = buildContinuations(sels, func(id string) (string, bool) {
		bl, ok := bank.Bullets[id]
		return bl.Text, ok
	})
	b.homes = entryHomes(base, sels)
	b.fam = newFamilies(bank, base)
	return nil
}

// tailor asks the model for a choice and builds it, with at most one retry that
// tells the model what was wrong. It returns the result, or why the base
// selection is used instead; a non-nil error ends the build.
func (b *builder) tailor(ctx context.Context, req Request, baseCh choice, badClaims map[string]bool) (*Result, string, *Error) {
	p := b.buildPool(func(id string) bool {
		for _, t := range tokens(b.bank.Bullets[id].Text) {
			if badClaims[t] {
				return false
			}
		}
		return true
	})
	kw := keywordText(req)
	msgs := []llm.Message{userMsg(b.prompt(p, req, baseCh.count()))}
	// soft is the last tailored build whose only flaw was a soft layout one. It
	// is returned instead of the base when no better tailored build lands.
	var soft *Result
	keepSoft := func(why string) (*Result, string, *Error) {
		if soft != nil {
			return soft, "", nil
		}
		return nil, why, nil
	}
	for round := 0; round < 2; round++ {
		if round > 0 && b.remaining(ctx) < minForModel {
			return keepSoft("the tailored build was rejected and no time was left to retry")
		}
		tr := b.turn(ctx, msgs)
		if ctx.Err() != nil {
			return keepSoftOnTimeout(soft, ctx, ctxError(ctx))
		}
		if tr.text == "" {
			return keepSoft(tr.reason)
		}
		ch, err := b.parseChoice(tr.text, p, kw)
		if err != nil {
			// A refused choice shares the one retry: the model is told why.
			if round == 1 {
				return keepSoft("the model's choice was refused: " + err.Error())
			}
			msgs = append(msgs, assistantMsg(tr.text), userMsg(feedback([]problem{{"choice", err.Error()}}, nil)))
			continue
		}
		out, ferr := b.attempt(ctx, fmt.Sprintf("tailored%d", round), ch, false)
		if ferr != nil {
			if ferr.Code == CodeTimeout {
				return keepSoftOnTimeout(soft, ctx, ferr)
			}
			return nil, "", ferr
		}
		why := ""
		if round == 1 {
			why = "after one retry"
		}
		if len(out.problems) == 0 {
			return &Result{PDF: out.pdf, Pages: out.pages, Selection: SelectionTailored, Reason: why, UnverifiedDropped: out.dropped, Notes: out.notes}, "", nil
		}
		if out.softFit != "" && len(out.problems) == 1 && out.pdf != nil {
			soft = &Result{PDF: out.pdf, Pages: out.pages, Selection: SelectionTailored, Reason: strings.TrimSpace(why + " kept with a layout note"), UnverifiedDropped: out.dropped, Notes: out.notes}
		}
		if round == 1 {
			return keepSoft("the model's choice was rejected twice: " + kinds(out.problems))
		}
		msgs = append(msgs, assistantMsg(tr.text), userMsg(feedback(out.problems, out.dropped)))
	}
	return nil, "the model's choice was rejected", nil
}

// keepSoftOnTimeout ends tailoring when the build ran out of time. A finished
// soft-fit tailored PDF from an earlier round is a result; discarding it for an
// error would lose a good page. Only running out of time keeps it: a caller's
// cancellation, like any other failure, still ends the build with err.
func keepSoftOnTimeout(soft *Result, ctx context.Context, err *Error) (*Result, string, *Error) {
	if soft != nil && err.Code == CodeTimeout && !errors.Is(context.Cause(ctx), context.Canceled) {
		kept := *soft
		kept.Reason = strings.TrimSpace(kept.Reason + "; the retry ran out of time")
		return &kept, "", nil
	}
	return nil, "", err
}

func kinds(ps []problem) string {
	seen := map[string]bool{}
	var ks []string
	for _, p := range ps {
		if !seen[p.kind] {
			seen[p.kind] = true
			ks = append(ks, p.kind)
		}
	}
	return strings.Join(ks, ", ")
}

// keywordText is what the model's keywords must occur in: the posting as it was
// shown, without the labels this package adds.
func keywordText(r Request) string {
	return strings.Join([]string{oneLine(r.Company, 300), oneLine(r.Title, 300), runesPrefix(strings.TrimSpace(r.Description), MaxPostingRunes)}, "\n")
}

// claimsFile is the part of `rkb claims export` this package reads.
type claimsFile struct {
	Claims map[string]struct {
		DoubleVerified bool `json:"double_verified"`
		// Problems are rkb's reasons against the claim. Only their number is read:
		// they quote the owner's sentences.
		Problems []string `json:"problems"`
	} `json:"claims"`
}

// rkb runs one rkb command. A runner error is classified; a non-zero exit is
// the caller's to read.
func (b *builder) rkb(ctx context.Context, step string, timeout time.Duration, args ...string) (Output, *Error) {
	if ctx.Err() != nil {
		return Output{}, ctxError(ctx)
	}
	if rem := b.remaining(ctx); rem < timeout {
		timeout = rem
	}
	out, err := b.cfg.Runner.Run(ctx, Command{Path: b.cfg.RKB, Args: args, Env: b.env}, timeout)
	// Everything rkb says is scrubbed here, at the one place it enters, before it
	// is clipped, quoted in an error or put in front of the model.
	out.Stdout, out.Stderr = b.scrub.clean(out.Stdout), b.scrub.clean(out.Stderr)
	switch {
	case err == nil:
		return out, nil
	case ctx.Err() != nil:
		return out, ctxError(ctx)
	case errors.Is(err, ErrStepTimeout):
		return out, fail(CodeTimeout, "rkb %s did not finish in time", step)
	default:
		return out, fail(CodeRender, "rkb %s could not run: %s", step, clip(b.scrub.clean(err.Error()), 200))
	}
}

// export writes ch as a selection into d and runs `rkb claims export --used-in`
// on it.
func (b *builder) export(ctx context.Context, d string, ch choice) (claimsFile, *Error) {
	var cf claimsFile
	sel, err := b.base.selectionJSON(b.bankPath, ch)
	if err != nil {
		return cf, fail(CodeKB, "selection: %v", err)
	}
	selPath := filepath.Join(d, "sel.json")
	if err := os.WriteFile(selPath, sel, 0o600); err != nil {
		return cf, fail(CodeRender, "write selection: %v", err)
	}
	claimsPath := filepath.Join(d, "claims.json")
	_ = os.Remove(claimsPath)
	args := []string{"claims", "export", "--used-in", selPath, "--out", claimsPath}
	if b.cfg.VMReport != "" {
		args = append(args, "--vm-report", b.cfg.VMReport)
	}
	out, rerr := b.rkb(ctx, "claims export", exportTimeout, args...)
	if rerr != nil {
		return cf, rerr
	}
	if out.Exit != 0 {
		return cf, fail(CodeKB, "rkb claims export failed (exit %d): %s", out.Exit, clip(out.Stderr, 300))
	}
	raw, err := readCapped(claimsPath, kbsync.MaxClaimsBytes)
	if err != nil {
		return cf, fail(CodeKB, "rkb wrote no claims file: %v", err)
	}
	if err := json.Unmarshal(raw, &cf); err != nil || len(cf.Claims) == 0 {
		return cf, fail(CodeKB, "rkb's claims file holds no claims")
	}
	// The claims are rkb's, read live from its repos on every export (the bank is
	// pinned to the snapshot, the claims are not). A figure that now reads
	// differently from the one eligibility was judged on would print wording the
	// owner never reviewed.
	now, perr := kbsync.ParseClaims(raw)
	if perr != nil {
		return cf, fail(CodeKB, "rkb's claims file is not usable: %v", perr)
	}
	for id, v := range now.Values {
		if want, ok := b.claims.Values[id]; ok && v != want {
			return cf, fail(CodeKBChanged, "the knowledge base changed during the build; build again")
		}
	}
	return cf, nil
}

// rkbFit runs `rkb fit`. With a lock folder it first waits, until the step's own
// time is up, for the Chrome lock: fit starts a headless Chrome, and two at once
// (two resumes, or a resume and an editor's fit) starve each other past rkb's 90 s
// Chrome limit.
func (b *builder) rkbFit(ctx context.Context, args ...string) (Output, *Error) {
	if b.cfg.LockDir == "" {
		return b.rkb(ctx, "fit", fitTimeout, args...)
	}
	waitCtx, cancel := context.WithTimeout(ctx, fitTimeout)
	l, err := filelock.Acquire(waitCtx, filepath.Join(b.cfg.LockDir, chromeLockName))
	cancel()
	switch {
	case err == nil:
		defer l.Release()
	case ctx.Err() != nil:
		return Output{}, ctxError(ctx)
	case errors.Is(err, filelock.ErrWouldBlock):
		return Output{}, fail(CodeTimeout, "rkb fit waited %s for another page fit to release Chrome", fitTimeout)
	default:
		return Output{}, fail(CodeRender, "the Chrome lock could not be taken: %v", err)
	}
	return b.rkb(ctx, "fit", fitTimeout, args...)
}

// readCapped reads a regular file of at most max bytes.
func readCapped(path string, max int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > max {
		return nil, fmt.Errorf("larger than %d bytes", max)
	}
	return raw, nil
}

// unverifiedBullets lists the chosen bullets that use a figure rkb has not
// double-verified for this selection.
func (b *builder) unverifiedBullets(ch choice, cf claimsFile) []string {
	var bad []string
	seen := map[string]bool{}
	for _, id := range ch.bulletIDs() {
		for _, t := range tokens(b.bank.Bullets[id].Text) {
			if c, ok := cf.Claims[t]; (!ok || !c.DoubleVerified) && !seen[id] {
				seen[id] = true
				bad = append(bad, id)
			}
		}
	}
	return bad
}

var unverifiedRe = regexp.MustCompile(`class="unverified" title="(clm[A-Za-z]+)"`)

// fitReport is the part of `rkb fit --report` this package reads.
type fitReport struct {
	Status  int    `json:"status"`
	Advice  string `json:"advice"`
	Pages   int    `json:"pages"`
	Orphans []struct {
		ID string `json:"id"`
	} `json:"orphan_last_lines"`
	Long []struct {
		ID string `json:"id"`
	} `json:"over_line_limit"`
}

// attempt builds ch into a PDF in its own folder: claims export, drop what is
// unverified, lint, render, fit --strict, then the PDF's own checks. A tool
// that cannot run ends the build (*Error); content rkb rejects comes back as
// problems.
func (b *builder) attempt(ctx context.Context, label string, ch choice, acceptLayout bool) (outcome, *Error) {
	var out outcome
	d := filepath.Join(b.dir, label)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return out, fail(CodeRender, "no private temp folder: %v", err)
	}
	for round := 0; ; round++ {
		cf, err := b.export(ctx, d, ch)
		if err != nil {
			return out, err
		}
		bad := b.unverifiedBullets(ch, cf)
		if len(bad) == 0 {
			break
		}
		out.dropped = append(out.dropped, bad...)
		drop := map[string]bool{}
		for _, id := range bad {
			drop[id] = true
		}
		var dependents []string
		ch, dependents = ch.without(drop, b.isCont)
		// Continuations that referred to a dropped bullet go with it.
		out.dropped = append(out.dropped, dependents...)
		if ch.count() == 0 || round == 1 {
			out.problems = append(out.problems, problem{"figures", "figures in " + strings.Join(bad, ", ") + " are not double-verified in rkb"})
			return out, nil
		}
	}
	sort.Strings(out.dropped)

	selPath := filepath.Join(d, "sel.json")
	claimsPath := filepath.Join(d, "claims.json")
	lint, err := b.rkb(ctx, "lint", lintTimeout, "lint", selPath, "--claims", claimsPath)
	if err != nil {
		return out, err
	}
	switch lint.Exit {
	case 0:
	case 1:
		out.problems = append(out.problems, lintProblems(lint.Stdout)...)
		return out, nil
	default:
		return out, fail(CodeRender, "rkb lint could not run (exit %d): %s", lint.Exit, clip(lint.Stderr, 300))
	}

	outDir := filepath.Join(d, "out")
	render, err := b.rkb(ctx, "render", renderTimeout, "render", selPath, "--claims", claimsPath, "--out-dir", outDir)
	if err != nil {
		return out, err
	}
	if render.Exit != 0 {
		return out, fail(CodeRender, "rkb render failed (exit %d): %s", render.Exit, clip(render.Stderr, 300))
	}
	htmlPath := filepath.Join(outDir, outputBasename+".html")
	page, rerr := readCapped(htmlPath, maxReadHTML)
	if rerr != nil {
		return out, fail(CodeRender, "rkb render wrote no page: %v", rerr)
	}
	if ids := unverifiedRe.FindAllStringSubmatch(string(page), -1); len(ids) > 0 {
		seen := map[string]bool{}
		var claimIDs []string
		for _, m := range ids {
			if !seen[m[1]] {
				seen[m[1]] = true
				claimIDs = append(claimIDs, m[1])
			}
		}
		out.problems = append(out.problems, problem{"figures", "the page still marks figures as unverified: " + strings.Join(claimIDs, ", ")})
		return out, nil
	}

	pdfPath := filepath.Join(outDir, outputBasename+".pdf")
	reportPath := filepath.Join(outDir, "fit.json")
	fit, err := b.rkbFit(ctx, "fit", htmlPath, "--pages", fmt.Sprint(b.base.pages), "--strict", "--out", pdfPath, "--report", reportPath)
	if err != nil {
		return out, err
	}
	// rkb's die() exits 2, the same code as OVERFLOW, and writes no report: a
	// verdict counts only when the report exists and carries the exit code.
	var rep fitReport
	raw, rerr := readCapped(reportPath, 1<<20)
	if rerr != nil || json.Unmarshal(raw, &rep) != nil || rep.Status != fit.Exit {
		return out, fail(CodeRender, "rkb fit did not run to a verdict (exit %d; is Chrome or poppler missing?): %s", fit.Exit, clip(fit.Stderr, 300))
	}
	if rep.Status != 0 {
		msg := clip(b.scrub.clean(rep.Advice), 300)
		if ids := fitIDs(rep); ids != "" {
			msg += " (" + b.scrub.clean(ids) + ")"
		}
		// Soft flaws leave every page in use and all content in place: GAP (4),
		// STRICT (5), and SHORT (3) when the content still reaches the last page
		// (rkb's own guidance: report a surviving orphan, do not loop). OVERFLOW
		// and a SHORT that ends a page early are hard. The base accepts soft
		// flaws with a note. A tailored choice reports them as a "fit" problem
		// (so the model may retry) but still gets its PDF, which tailor keeps
		// over the base: a tailored page with an orphan beats an untailored one.
		soft := rep.Status == 4 || rep.Status == 5 || (rep.Status == 3 && rep.Pages == b.base.pages)
		if !soft {
			out.problems = append(out.problems, problem{"fit", msg})
			return out, nil
		}
		out.notes = append(out.notes, msg)
		if !acceptLayout {
			out.softFit = msg
		}
	}
	pdf, perr := readCapped(pdfPath, b.cfg.MaxPDF)
	if perr != nil {
		if st, serr := os.Stat(pdfPath); serr == nil && st.Size() > int64(b.cfg.MaxPDF) {
			out.problems = append(out.problems, problem{"size", fmt.Sprintf("the PDF is over %d KB", b.cfg.MaxPDF>>10)})
			return out, nil
		}
		return out, fail(CodeRender, "rkb wrote no PDF: %v", perr)
	}
	if len(pdf) == 0 || !strings.HasPrefix(string(pdf[:min(5, len(pdf))]), "%PDF-") {
		return out, fail(CodeRender, "rkb's PDF is damaged")
	}
	if rep.Pages <= 0 {
		return out, fail(CodeRender, "rkb did not report a page count")
	}
	if rep.Pages > b.base.pages {
		out.problems = append(out.problems, problem{"pages", fmt.Sprintf("the PDF has %d pages, over the %d-page budget", rep.Pages, b.base.pages)})
		return out, nil
	}
	out.pdf, out.pages = pdf, rep.Pages
	if out.softFit != "" {
		out.problems = append(out.problems, problem{"fit", out.softFit})
	}
	return out, nil // out.problems is empty here, or holds only the soft fit
}

func fitIDs(r fitReport) string {
	var parts []string
	for i, o := range r.Orphans {
		if i == 8 {
			break
		}
		parts = append(parts, "orphan "+o.ID)
	}
	for i, o := range r.Long {
		if i == 8 {
			break
		}
		parts = append(parts, "long "+o.ID)
	}
	return strings.Join(parts, ", ")
}

// lintProblems reads the ERROR lines of rkb lint's stdout.
func lintProblems(stdout string) []problem {
	var ps []problem
	for _, l := range strings.Split(stdout, "\n") {
		if msg, ok := strings.CutPrefix(strings.TrimSpace(l), "ERROR "); ok {
			ps = append(ps, problem{"lint", clip(msg, 300)})
		}
	}
	if len(ps) == 0 {
		ps = append(ps, problem{"lint", "rkb lint reported errors"})
	}
	return ps
}

// childEnv is the environment rkb runs with: the minimal one plus RKB_CODE_ROOT,
// and a PATH that finds pdftotext. A missing pdftotext is an error here, before
// a model turn is spent: rkb fit cannot measure a page without it.
func (b *builder) childEnv() ([]string, *Error) {
	var extra []string
	if b.cfg.CodeRoot != "" {
		extra = append(extra, "RKB_CODE_ROOT="+b.cfg.CodeRoot)
	}
	env := childenv.Minimal(extra...)
	idx, path := -1, ""
	for i, e := range env {
		if v, ok := strings.CutPrefix(e, "PATH="); ok {
			idx, path = i, v
		}
	}
	if onPath(path, "pdftotext") {
		return env, nil
	}
	dirs := b.cfg.ToolDirs
	if dirs == nil {
		dirs = []string{"/opt/homebrew/bin", "/usr/local/bin"}
	}
	for _, d := range dirs {
		if isExecutable(filepath.Join(d, "pdftotext")) {
			np := d
			if path != "" {
				np = path + string(os.PathListSeparator) + d
			}
			if idx >= 0 {
				env[idx] = "PATH=" + np
			} else {
				env = append(env, "PATH="+np)
			}
			return env, nil
		}
	}
	return nil, fail(CodeRender, "pdftotext (poppler) was not found on PATH or in %s; rkb cannot measure the page without it", strings.Join(dirs, ", "))
}

// withTempDir returns env with every temp-folder variable the platform reads
// (TMPDIR; also TEMP and TMP on Windows) set to dir.
func withTempDir(env []string, dir string) []string {
	keys := []string{"TMPDIR"}
	if runtime.GOOS == "windows" {
		keys = append(keys, "TEMP", "TMP")
	}
	out := make([]string, 0, len(env)+len(keys))
next:
	for _, e := range env {
		for _, k := range keys {
			if strings.HasPrefix(e, k+"=") {
				continue next
			}
		}
		out = append(out, e)
	}
	for _, k := range keys {
		out = append(out, k+"="+dir)
	}
	return out
}

func onPath(path, name string) bool {
	for _, d := range filepath.SplitList(path) {
		if d != "" && filepath.IsAbs(d) && isExecutable(filepath.Join(d, name)) {
			return true
		}
	}
	return false
}

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}
