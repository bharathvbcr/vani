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
	"slices"
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
	// fp names the inputs this build read (record.go), filled by load.
	fp Fingerprints
	// last is how the checks of the most recent attempt stood, for the Result an
	// error carries.
	last Checks
	// answered counts the model turns that returned a reply.
	answered int
	// cost sums the model turns (cost.go).
	cost costMeter
	// rd is the resolved redraft (nil for a build with no previous choice) and instr
	// the owner's instruction as it enters the prompt and the record (redraft.go).
	rd    *redraft
	instr string
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
	// ch is the choice that was built: the one attempt was given, less what it
	// dropped as unverified.
	ch choice
	// checks say how each check of this attempt ended (record.go).
	checks Checks
	// fitLines are rkb's per-bullet line counts for ch (fitLinesOf).
	fitLines map[string]int
	// htmlPath is rkb's page, set when the attempt rendered one.
	htmlPath string
	// orphans are the bullets of ch that rkb's fit reported ending in an orphan last
	// line (ids only, report order), for the retry's feedback.
	orphans []string
}

// finish is the Result of a built outcome: the choice written back in the
// owner-edit shape, its bullets with the hash of their wording, and the
// fingerprints of what was read.
func (b *builder) finish(source, reason string, out outcome) (Result, *Error) {
	sel, err := out.ch.editJSON(b.base)
	if err != nil {
		return Result{}, fail(CodeKB, "the built choice cannot be recorded: %v", err)
	}
	res := Result{
		PDF: out.pdf, HTMLPath: out.htmlPath, Pages: out.pages, Source: source, Reason: reason,
		Checks: out.checks, Notes: out.notes, UnverifiedDropped: out.dropped, Keywords: out.ch.keywords,
		SelectionJSON: sel, Bullets: b.bulletRefs(out.ch), FitLines: out.fitLines, Fingerprints: b.fp,
	}
	if source == SourceTailored {
		res.Instruction = b.instr // a base page was shaped by no model, so by no instruction
		res.Scope, res.Lock = b.cfg.Tailor.Scope, slices.Clone(b.cfg.Tailor.Lock)
	}
	return res, nil
}

// partial is the Result an error carries: nothing built, but what ran, and what
// was read.
func (b *builder) partial() Result {
	return Result{Checks: b.last, Fingerprints: b.fp}
}

func (b *builder) remaining(ctx context.Context) time.Duration {
	if dl, ok := ctx.Deadline(); ok {
		return time.Until(dl)
	}
	return MaxDeadline
}

// prepare readies a build: the environment rkb runs in (with a PATH that finds
// pdftotext when the build will fit a page), then everything load reads.
func (b *builder) prepare(ctx context.Context, needFit bool) *Error {
	if ctx.Err() != nil {
		return ctxError(ctx)
	}
	env, err := b.childEnv(needFit)
	if err != nil {
		return err
	}
	// rkb makes its Chrome profile and fit work folders in its temp folder, and
	// cleans them only when it exits normally: inside the build folder they go
	// with it, whatever happens to rkb.
	b.env = withTempDir(env, filepath.Join(b.dir, tmpDirName))
	return b.load(ctx)
}

func (b *builder) run(ctx context.Context, req Request) (Result, *Error) {
	if err := b.prepare(ctx, true); err != nil {
		return Result{}, err
	}
	// The previous choice, scope and locks are read against the bank before the
	// first rkb export of the build and long before a turn is spent.
	if err := b.resolveTailor(req); err != nil {
		return Result{}, err
	}
	baseCh := b.baseChoice()
	if baseCh.count() == 0 {
		return Result{}, fail(CodeKB, "no bullet of the base selection is eligible (%d listed bullets are not)", len(b.el.Why))
	}
	// One export of the base: it proves rkb runs before a model turn is spent,
	// and names the bullets/figures rkb has not verified, so the model is not
	// offered bullets that would be dropped.
	probeDir := filepath.Join(b.dir, "probe")
	if err := os.MkdirAll(probeDir, 0o700); err != nil {
		return Result{}, fail(CodeRender, "no private temp folder: %v", err)
	}
	cf, err := b.export(ctx, probeDir, baseCh)
	if err != nil {
		return Result{}, err
	}
	keepBullet := b.probeKeep(cf, baseCh)

	// noModel: the model could not run at all (none configured, no time for a
	// turn, the budget refused it, it could not be reached or timed out before
	// it answered). A model that answered with a choice that was then refused or
	// rejected did run: that falls back to the base with its reason whatever
	// AllowBase says, as the cost of the turns is spent either way.
	reason, noModel := b.cfg.NoModel, false
	switch {
	case b.cfg.Provider == nil:
		noModel = true
		if reason == "" {
			reason = "no model is configured"
		}
	case b.remaining(ctx) < minForModel:
		noModel = true
		reason = "not enough time was left for a model turn"
	default:
		res, why, err := b.tailor(ctx, req, baseCh, keepBullet)
		if err != nil {
			return Result{}, err
		}
		if res != nil {
			return *res, nil
		}
		reason, noModel = why, b.answered == 0
	}
	if ctx.Err() != nil {
		return Result{}, ctxError(ctx)
	}
	if noModel && !b.cfg.AllowBase {
		return Result{}, fail(CodeNoModel, "the model could not run (%s), so no tailored resume was built", reason)
	}
	if b.rd.keeps() {
		// The base selection would not keep what the owner kept: no page is better
		// than one that undoes the lock.
		code := b.rd.refusal
		if code == "" {
			code = CodeRedraft
		}
		return Result{}, fail(code, "the redraft was not made (%s); the base selection is not built in its place, because it would not keep what is kept", reason)
	}
	if b.remaining(ctx) < minForBase {
		return Result{}, fail(CodeTimeout, "not enough time was left to build the base resume")
	}
	out, err := b.attempt(ctx, "base", baseCh, true, true)
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
	return b.finish(SourceBase, reason, out)
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

// probeKeep decides per bullet whether the probe export vouches for offering it
// to the model. When two base bullets share a claim and only one sentence is
// Pass-2-confirmed, only the unconfirmed sentence is hidden while its confirmed
// sibling remains available.
func (b *builder) probeKeep(cf claimsFile, baseCh choice) func(id string) bool {
	bad := b.badFigures(cf, baseCh)
	inBaseBullet := map[string]bool{}
	for _, id := range baseCh.bulletIDs() {
		inBaseBullet[id] = true
	}
	return func(id string) bool {
		raw := b.bank.Bullets[id].Text
		for _, t := range tokens(raw) {
			c, known := cf.Claims[t]
			if !known {
				return false
			}
			if c.DoubleVerified {
				continue
			}
			if len(c.ConfirmedSentences) > 0 && !hasPass1Problem(c.Problems) {
				if inBaseBullet[id] && !slices.Contains(c.ConfirmedSentences, sentenceHash(raw, t)) {
					return false
				}
				continue
			}
			if bad[t] {
				return false
			}
		}
		return true
	}
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
	b.fp.Bank = sha256Hex(raw)
	b.fp.RKB = rkbFingerprint(b.cfg.RKB)
	if b.cfg.VMReport != "" {
		// The validator report decides which figures count as verified, and rkb reads
		// it itself: an unreadable, truncated, crashed or quiet (problems-only)
		// report fails closed before rkb treats every figure as unverified.
		st, err := os.Stat(b.cfg.VMReport)
		if err != nil || !st.Mode().IsRegular() {
			return fail(CodeConfig, "the validator report %s cannot be read (it must be a readable file of at most %d bytes)", filepath.Base(b.cfg.VMReport), kbsync.MaxClaimsBytes)
		}
		vmRaw, err := readCapped(b.cfg.VMReport, kbsync.MaxClaimsBytes)
		if err != nil {
			return fail(CodeConfig, "the validator report %s cannot be read (it must be a readable file of at most %d bytes)", filepath.Base(b.cfg.VMReport), kbsync.MaxClaimsBytes)
		}
		if why := CheckVMReport(vmRaw); why != "" {
			return fail(CodeConfig, "the validator report %s is not usable (%s)", filepath.Base(b.cfg.VMReport), why)
		}
		b.fp.VMReport = sha256Hex(vmRaw)
	}
	// The base comes before the first rkb call so the owner's header strings are
	// known to the scrubber for everything rkb says from here on.
	base, berr := loadBase(b.cfg.BaseSelection, MaxPages)
	if berr != nil {
		return berr
	}
	b.base = base
	b.fp.BaseSelection = base.sha
	b.scrub = newScrubber(headerStrings(base, b.bankPath))
	claims, err := kbsync.RKB{Binary: b.cfg.RKB, CodeRoot: b.cfg.CodeRoot, Timeout: exportTimeout, Runner: claimsRunner{b.cfg.Runner, b.env, b.scrub}}.Claims(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctxError(ctx)
		}
		return fail(CodeKB, "claims: %v", err)
	}
	b.claims = claims
	b.fp.Claims = claimsDigest(claims)
	sels, err := kbsync.LoadSelections(b.cfg.Selections)
	if err != nil {
		return fail(CodeKB, "%v", err)
	}
	b.fp.Selections = selectionsDigest(sels)
	b.el = kbsync.Eligible(bank, claims, sels)
	b.cont = buildContinuations(sels, func(id string) (string, bool) {
		bl, ok := bank.Bullets[id]
		return bl.Text, ok
	})
	b.homes = entryHomes(base, sels)
	b.fam = newFamilies(bank, base)
	b.last.Eligibility = checkCurrent
	return nil
}

// tailor asks the model for a choice and builds it, with at most one retry that
// tells the model what was wrong. It returns the result, or why the base
// selection is used instead; a non-nil error ends the build.
func (b *builder) tailor(ctx context.Context, req Request, baseCh choice, keepBullet func(string) bool) (*Result, string, *Error) {
	var onKept map[string]bool
	if b.rd.keeps() {
		onKept = b.keptBullets()
	}
	p := b.buildPool(func(id string) bool {
		if onKept[id] {
			return false // a kept part already uses it, and an id is used once
		}
		return keepBullet(id)
	})
	if b.rd.keeps() {
		p = b.restrictPool(p)
	}
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
	// A reply that validated is kept (choicekeep.go) for as long as the build is
	// failing after it: a build that ends any other way (built, or the choice was
	// rejected) forgets what it kept, so only a failure that is not the model's
	// buys a retry the same reply.
	kept := choiceKeep{dir: b.cfg.ChoiceDir}
	var keys []string
	retain := false
	defer func() {
		if !retain {
			for _, k := range keys {
				kept.remove(k)
			}
		}
	}()
	for round := 0; round < 2; round++ {
		if round > 0 && b.remaining(ctx) < minForModel {
			return keepSoft("the tailored build was rejected and no time was left to retry")
		}
		key := b.keptChoiceKey(msgs)
		tr, reused := turnResult{}, false
		if text, ok := kept.load(key, time.Now()); ok {
			tr, reused = turnResult{text: text}, true
		} else {
			tr = b.turn(ctx, msgs)
		}
		if ctx.Err() != nil {
			retain = true
			return keepSoftOnTimeout(soft, ctx, ctxError(ctx))
		}
		if tr.text == "" {
			return keepSoft(tr.reason)
		}
		b.answered++
		ch, err := b.parseTailored(tr.text, p, kw)
		if err == nil {
			keys = append(keys, key)
			if !reused {
				kept.save(key, tr.text, time.Now())
			}
		}
		if err != nil {
			if b.rd != nil {
				b.rd.refusal = refusalCode(err)
			}
			// A refused choice shares the one retry: the model is told why.
			if round == 1 {
				return keepSoft("the model's choice was refused: " + err.Error())
			}
			msgs = append(msgs, assistantMsg(tr.text), userMsg(feedback([]problem{{"choice", err.Error()}}, nil, nil)))
			continue
		}
		out, ferr := b.attempt(ctx, fmt.Sprintf("tailored%d", round), ch, false, true)
		if ferr != nil {
			retain = true
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
			res, rerr := b.finish(SourceTailored, why, out)
			if rerr != nil {
				return nil, "", rerr
			}
			return &res, "", nil
		}
		if out.softFit != "" && len(out.problems) == 1 && out.pdf != nil {
			res, rerr := b.finish(SourceTailored, strings.TrimSpace(why+" kept with a layout note"), out)
			if rerr != nil {
				return nil, "", rerr
			}
			soft = &res
		}
		if round == 1 {
			return keepSoft("the model's choice was rejected twice: " + kinds(out.problems))
		}
		if b.rd != nil {
			b.rd.refusal = CodeRedraft
		}
		msgs = append(msgs, assistantMsg(tr.text), userMsg(feedback(out.problems, out.dropped, out.orphans)))
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

// exportedClaim is one claim's verification status from `rkb claims export`.
type exportedClaim struct {
	DoubleVerified     bool     `json:"double_verified"`
	ConfirmedSentences []string `json:"confirmed_sentences"`
	// Problems are rkb's reasons against the claim. Their prefixes ("pass 1 ",
	// "validator ") say whether Pass 1 failed; they are never echoed out because
	// Pass 2 messages quote the owner's sentences.
	Problems []string `json:"problems"`
}

// claimsFile is the part of `rkb claims export` this package reads.
type claimsFile struct {
	Claims map[string]exportedClaim `json:"claims"`
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
// double-verified for this selection (or whose specific sentence is not
// Pass-2-confirmed when siblings share a claim).
func (b *builder) unverifiedBullets(ch choice, cf claimsFile) []string {
	var bad []string
	seen := map[string]bool{}
	for _, id := range ch.bulletIDs() {
		raw := b.bank.Bullets[id].Text
		for _, t := range tokens(raw) {
			if c, ok := cf.Claims[t]; (!ok || !sentenceVerified(raw, t, c)) && !seen[id] {
				seen[id] = true
				bad = append(bad, id)
			}
		}
	}
	return bad
}

var unverifiedRe = regexp.MustCompile(`class="unverified" title="(clm[A-Za-z]+)"`)

// fitUnmeasured is rkb fit's exit code when --strict could not measure the
// bullets' lines: the page fits, but the orphan and line-length checks did not
// run.
const fitUnmeasured = 6

// fitReport is the part of `rkb fit --report` this package reads. Bullets holds
// ids and line counts only: the report's own text for each bullet is never
// decoded.
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
	Bullets []struct {
		ID    string `json:"id"`
		Lines int    `json:"lines"`
	} `json:"bullets"`
}

// attempt builds ch in its own folder: claims export, drop what is unverified,
// lint, render, and with fit set `rkb fit --strict` and the PDF's own checks. A
// tool that cannot run ends the build (*Error); content rkb rejects comes back
// as problems. Without fit the attempt stops after the page is rendered: out.pdf
// is nil, out.pages 0, out.htmlPath is rkb's page and the Fit check is not_run.
// out always says how far the checks got (out.checks), error or not.
func (b *builder) attempt(ctx context.Context, label string, ch choice, acceptLayout, fit bool) (out outcome, aerr *Error) {
	out.ch = ch
	out.checks = Checks{Eligibility: checkCurrent, Lint: checkNotRun, Fit: checkNotRun, Figures: checkNotRun}
	defer func() { b.last = out.checks }()
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
			out.ch = ch
			out.checks.Figures = checkDropped
			out.problems = append(out.problems, problem{"figures", "figures in " + strings.Join(bad, ", ") + " are not double-verified in rkb"})
			return out, nil
		}
		// Dropping unverified impact bullets can leave count-only or unquantified_ok
		// bullets past rkb lint's >=75% impact and >=85% figured thresholds; trim
		// excess ones before round 1 exports sel.json and claims.json.
		rebalanced, trimmed := b.rebalanceShares(ch)
		ch = rebalanced
		if len(trimmed) > 0 {
			out.notes = append(out.notes, "left off to keep figure shares: "+strings.Join(trimmed, ", "))
		}
	}
	sort.Strings(out.dropped)
	out.ch = ch
	out.checks.Figures = checkVerified
	if len(out.dropped) > 0 {
		out.checks.Figures = checkDropped
	}

	selPath := filepath.Join(d, "sel.json")
	claimsPath := filepath.Join(d, "claims.json")
	lint, err := b.rkb(ctx, "lint", lintTimeout, "lint", selPath, "--claims", claimsPath)
	if err != nil {
		return out, err
	}
	switch lint.Exit {
	case 0:
		out.checks.Lint = checkPassed
	case 1:
		out.checks.Lint = checkFailed
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
		out.checks.Figures = checkDropped
		out.problems = append(out.problems, problem{"figures", "the page still marks figures as unverified: " + strings.Join(claimIDs, ", ")})
		return out, nil
	}
	out.htmlPath = htmlPath
	if !fit {
		return out, nil
	}

	pdfPath := filepath.Join(outDir, outputBasename+".pdf")
	reportPath := filepath.Join(outDir, "fit.json")
	fitOut, err := b.rkbFit(ctx, "fit", htmlPath, "--pages", fmt.Sprint(b.base.pages), "--strict", "--out", pdfPath, "--report", reportPath)
	if err != nil {
		out.checks.Fit = checkCouldNotRun
		return out, err
	}
	// rkb's die() exits 2, the same code as OVERFLOW, and writes no report: a
	// verdict counts only when the report exists and carries the exit code.
	var rep fitReport
	raw, rerr := readCapped(reportPath, 1<<20)
	if rerr != nil || json.Unmarshal(raw, &rep) != nil || rep.Status != fitOut.Exit {
		out.checks.Fit = checkCouldNotRun
		return out, fail(CodeRender, "rkb fit did not run to a verdict (exit %d; is Chrome or poppler missing?): %s", fitOut.Exit, clip(fitOut.Stderr, 300))
	}
	out.fitLines = fitLinesOf(rep, ch)
	out.orphans = orphanIDs(rep, ch)
	out.checks.Fit = checkPassed
	if rep.Status == fitUnmeasured {
		// The pages fit, but rkb could not measure the bullets' lines (Chrome
		// under load), so its orphan and line-length checks did not run. The
		// most they could have found is a soft flaw (STRICT, 5), which the
		// owner accepts with a note, so it is "soft" with rkb's UNMEASURED note,
		// never "passed". Asking the model again would not make Chrome measure.
		out.checks.Fit = checkSoft
		out.notes = append(out.notes, clip(b.scrub.clean(rep.Advice), 300))
	} else if rep.Status != 0 {
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
			out.checks.Fit = checkFailed
			out.problems = append(out.problems, problem{"fit", msg})
			return out, nil
		}
		out.checks.Fit = checkSoft
		out.notes = append(out.notes, msg)
		if !acceptLayout {
			out.softFit = msg
		}
	}
	pdf, perr := readCapped(pdfPath, b.cfg.MaxPDF)
	if perr != nil {
		if st, serr := os.Stat(pdfPath); serr == nil && st.Size() > int64(b.cfg.MaxPDF) {
			out.checks.Fit = checkFailed
			out.problems = append(out.problems, problem{"size", fmt.Sprintf("the PDF is over %d KB", b.cfg.MaxPDF>>10)})
			return out, nil
		}
		out.checks.Fit = checkCouldNotRun
		return out, fail(CodeRender, "rkb wrote no PDF: %v", perr)
	}
	if len(pdf) == 0 || !strings.HasPrefix(string(pdf[:min(5, len(pdf))]), "%PDF-") {
		out.checks.Fit = checkCouldNotRun
		return out, fail(CodeRender, "rkb's PDF is damaged")
	}
	if rep.Pages <= 0 {
		out.checks.Fit = checkCouldNotRun
		return out, fail(CodeRender, "rkb did not report a page count")
	}
	if rep.Pages > b.base.pages {
		out.checks.Fit = checkFailed
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
// and, when needFit, a PATH that finds pdftotext. A missing pdftotext is an error
// here, before a model turn is spent: rkb fit cannot measure a page without it.
// A build that fits no page does not need it.
func (b *builder) childEnv(needFit bool) ([]string, *Error) {
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
	if !needFit || pdfToolOnPath(path) {
		return env, nil
	}
	dirs := b.cfg.ToolDirs
	if dirs == nil {
		dirs = []string{"/opt/homebrew/bin", "/usr/local/bin"}
	}
	for _, d := range dirs {
		if pdfToolInDir(d) {
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

// pdfToolNames are the file names of poppler's pdftotext. Windows installs it
// as pdftotext.exe; the bare name is still accepted when it is executable.
func pdfToolNames(goos string) []string {
	if goos == "windows" {
		return []string{"pdftotext.exe", "pdftotext"}
	}
	return []string{"pdftotext"}
}

func pdfToolOnPath(path string) bool {
	for _, name := range pdfToolNames(runtime.GOOS) {
		if onPath(path, name) {
			return true
		}
	}
	return false
}

func pdfToolInDir(dir string) bool {
	for _, name := range pdfToolNames(runtime.GOOS) {
		if isExecutable(filepath.Join(dir, name)) {
			return true
		}
	}
	return false
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
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	return modeExecutable(runtime.GOOS, filepath.Base(p), st.Mode())
}

// modeExecutable reports whether a regular file may be run. On Windows a
// file whose name ends in .exe needs no Unix execute bit. Everywhere else
// the execute bit is required, including for a file named pdftotext.exe.
func modeExecutable(goos, base string, mode os.FileMode) bool {
	if goos == "windows" && strings.HasSuffix(strings.ToLower(base), ".exe") {
		return true
	}
	return mode.Perm()&0o111 != 0
}
