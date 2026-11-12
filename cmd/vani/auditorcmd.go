package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/decide/wouldapply"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/track"
	"github.com/bharathvbcr/decide"
)

// `vani auditor` is the calibration step of the would-apply auditor
// (internal/decide/wouldapply): a language model is asked the owner's own audit
// question about every posting the owner has judged (a would_apply label) or
// marked (saved, applied, hidden), and `auditor report` measures how well its
// answers agree with the owner's, against the teacher's triage reading and the
// relevance score, within the postings the engine kept and within those it
// dropped. Its answers are kept in eval.AuditorFile, never in the labels, and
// nothing in the pipeline, the ranking or the drop policy reads them (Rule 7):
// until the gate passes they are evidence about the auditor, nothing more.
//
// What is sent, per posting: the would-apply prompt, the target summary, the
// knowledge-base digest (read only through eligibleBank, as `vani kbmatch`
// sends it) and the posting, framed as untrusted content. Before anything is
// sent, the summary, digest and prompt are checked against the identifying-word
// list (internal/track); a hit refuses the run.
//
// Spend is bounded three ways: --budget-usd, the auditor's own daily cap
// (budget/auditor.json, auditorDailyCapUSD) and the daily llm cap's headroom.

const (
	// auditorDailyCapUSD is the auditor's own daily cap, in its own file beside
	// the OpenRouter (jev.json) and notes (jev-notes.json) caps.
	auditorDailyCapUSD = 15.0
	auditorBudgetFile  = "auditor.json"
	auditorSpendKey    = "auditor"
	auditorLockWait    = 10 * time.Second
	// auditorTypicalOut is a typical reply (two slots, thinking included), for
	// the typical-cost line.
	auditorTypicalOut = 200
)

// auditorTimeout bounds one question beyond the backend's own timeout (a
// variable only so a test need not wait it out).
var auditorTimeout = 90 * time.Second

// auditorRoleFitScore orders the teacher's role_fit reading for the AUC
// baseline. mismatch_seniority ("the work matches but the level does not")
// ranks above adjacent and mismatch_domain because its work is the target
// work; the order is fixed here, before any comparison is run.
var auditorRoleFitScore = map[string]float64{
	"strong_fit": 4, "partial_fit": 3, "mismatch_seniority": 2, "adjacent": 1, "mismatch_domain": 0,
}

func (e *env) cmdAuditor(args []string) error {
	if len(args) == 0 {
		return usagef("auditor: expected subcommand \"calibrate\" or \"report\"")
	}
	switch args[0] {
	case "calibrate":
		return e.auditorCalibrate(args[1:])
	case "report":
		return e.auditorReport(args[1:])
	}
	return usagef("auditor: unknown subcommand %q", args[0])
}

func (e *env) auditorPath() string { return filepath.Join(e.stateDir, eval.AuditorFile) }

// auditorPromptHash fingerprints everything a would-apply question sends apart
// from the posting and the digest (a row carries the digest's own hash): the
// system prompt and user message for an empty posting and an empty digest, and
// the target summary once more, as teacherKBMatchPromptHash does.
func auditorPromptHash(d kbmatch.Digest, summary string) (string, error) {
	// Frame, not a question built from a digest: it carries no knowledge-base text,
	// and refuses a digest the redactor did not make or a summary holding a term.
	q, err := wouldapply.Frame(summary, d)
	if err != nil {
		return "", err
	}
	sys, user := llmbackend.Messages(q)
	h := sha256.New()
	for _, s := range []string{sys, user, summary} {
		fmt.Fprintf(h, "%d:", len(s))
		io.WriteString(h, s)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// auditorTask is the would-apply question as an eval.TeacherTask, so the
// teacher's runner asks it (spend gate, bounded concurrency, resume, stop after
// a run of unread answers) and its rows carry the same provenance.
//
// Every question comes from wouldapply.Question, which is kbmatch.Prepare: a
// digest the redactor did not make, or a term that survived in the summary or
// digest, is refused per posting and nothing is sent. A digest that cannot be
// framed (the same refusals) makes every Ask fail with that error.
func auditorTask(summary string, d kbmatch.Digest) eval.TeacherTask {
	prompt, frameErr := auditorPromptHash(d, summary)
	return eval.TeacherTask{
		Name: wouldapply.Task, Fixture: wouldapply.Fixture(), Prompt: prompt, Digest: d.SHA256,
		Ask: func(ctx context.Context, dc decide.Decider, p posting.Posting) (decide.Answer, error) {
			if frameErr != nil {
				return decide.Answer{}, frameErr
			}
			q, _, err := wouldapply.Question(p, summary, d)
			if err != nil {
				return decide.Answer{}, err
			}
			return dc.Decide(ctx, q)
		},
	}
}

// auditorSetup is what calibrate and report both need: the config, the model
// that answers, the normalized target summary, the digest and the task. The
// digest is the redacted one every kb_match send uses (kbmatch.DigestOf with the
// owner's identifying-words list on top of the built-in one), so calibrate and
// report hash the same text. The list is read here without refusing when it is
// missing or empty: setup sends nothing, and calibrate refuses on that itself
// (its own flag, --no-identifying-words-file) before anything is sent; the same
// words are returned so calibrate's check uses the list the digest was made with.
// An unreadable list is an error. Setup does not run the identifying-word check;
// calibrate does (auditorIdentifying) before anything is sent.
func (e *env) auditorSetup(modelFlag string) (config.Config, string, string, kbmatch.Digest, eval.TeacherTask, error) {
	cfg, model, summary, d, _, task, err := e.auditorSetupWords(modelFlag)
	return cfg, model, summary, d, task, err
}

func (e *env) auditorSetupWords(modelFlag string) (config.Config, string, string, kbmatch.Digest, auditorIdent, eval.TeacherTask, error) {
	cfg, err := e.loadConfig()
	if err != nil {
		return cfg, "", "", kbmatch.Digest{}, auditorIdent{}, eval.TeacherTask{}, err
	}
	model := cmp.Or(modelFlag, cfg.LLM.EffectiveTriageModel())
	if cfg.LLM.Provider == "" || model == "" {
		return cfg, "", "", kbmatch.Digest{}, auditorIdent{}, eval.TeacherTask{}, errors.New("auditor: no llm provider and model are configured")
	}
	w, _, err := kbmatchWords(cfg, e.stateDir, false, false)
	if err != nil {
		return cfg, "", "", kbmatch.Digest{}, auditorIdent{}, eval.TeacherTask{}, fmt.Errorf("auditor: %w", err)
	}
	prof, d, summary, err := kbmatchDigest(cfg, w)
	if err != nil {
		return cfg, "", "", kbmatch.Digest{}, auditorIdent{}, eval.TeacherTask{}, fmt.Errorf("auditor: %w", err)
	}
	// The knowledge base before any redaction or cut, only for the auditor's own
	// check (see auditorIdent): never sent, never hashed.
	raw, err := kbmatch.RawDigestText(prof)
	if err != nil {
		return cfg, "", "", kbmatch.Digest{}, auditorIdent{}, eval.TeacherTask{}, fmt.Errorf("auditor: %w", err)
	}
	return cfg, model, summary, d, auditorIdent{words: w, rawDigest: raw}, auditorTask(summary, d), nil
}

// auditorIdent is what calibrate's identifying-word check needs: the list the
// digest was redacted with, and the digest's text before redaction.
//
// The auditor is stricter than kb_match: it asks the model to stand in for the
// owner, so a digest that held an identifying term is refused rather than
// stripped (the owner removes the term, or takes it off the list), as the
// reviewed auditor always did. What is sent is the redacted digest through
// kbmatch.Prepare either way, so a term that reached the check unseen is still
// stripped and refused at the send: no path sends one.
type auditorIdent struct {
	words     track.Words
	rawDigest string
}

// Which identifying-word lists a calibration checked (auditorPlanJSON).
const (
	auditorWordsBoth    = "the owner's list and the built-in list"
	auditorWordsBuiltin = "built-in list only"
)

// auditorFinding is one part of the shared text that holds identifying terms.
// The terms are the owner's private list (or the built-in one): String names
// the part and the count only, so a refusal, a dry run or a JSON reply never
// carries them; only --show-terms prints them, to a terminal's stderr.
type auditorFinding struct {
	part  string
	terms []string
}

func (f auditorFinding) String() string {
	s := "s"
	if len(f.terms) == 1 {
		s = ""
	}
	return fmt.Sprintf("%s holds %d identifying term%s", f.part, len(f.terms), s)
}

func auditorFindings(fs []auditorFinding) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = f.String()
	}
	return strings.Join(parts, "; ")
}

// auditorIdentifying checks the text every question carries (the prompt, the
// target summary and the knowledge-base digest) against the identifying-word
// list (internal/track: the built-in list plus the owner's
// identifying-words.txt). It returns one finding per part that holds a term,
// and personal false when the owner's list was missing, empty or only
// comments, so only the built-in list was checked. (A list that cannot be read
// is an error in auditorSetup: the check never gets here.)
//
// The digest part is the digest before redaction (auditorIdent), so a term the
// redactor would have stripped is still a refusal here; the summary and the
// prompt are checked, not redacted, as in kbmatch.Prepare.
func auditorIdentifying(id auditorIdent, summary string) (findings []auditorFinding, personal bool) {
	for _, part := range []struct{ name, text string }{
		{"the would-apply prompt", wouldapply.Prompt},
		{"the target summary", summary},
		{"the knowledge-base digest", id.rawDigest},
	} {
		if found := id.words.Found(part.text); len(found) > 0 {
			findings = append(findings, auditorFinding{part: part.name, terms: found})
		}
	}
	return findings, id.words.Personal
}

// auditorJudged is every posting the owner judged (a yes, maybe or no
// would_apply label) or marked, with the labels and marks.
type auditorJudged struct {
	labels   map[posting.Key]eval.Entry
	marks    map[posting.Key]marks.Entry
	keys     []posting.Key // sorted, each once
	labelled int
	marked   int
}

func (e *env) auditorJudgedSet() (auditorJudged, error) {
	s, err := e.openEvalStore("")
	if err != nil {
		return auditorJudged{}, err
	}
	done, _, err := s.Labels()
	if err != nil {
		return auditorJudged{}, fmt.Errorf("auditor: labels unreadable: %w", err)
	}
	m, err := marks.Read(marks.Dir(e.stateDir))
	if err != nil {
		return auditorJudged{}, fmt.Errorf("auditor: marks unreadable: %w", err)
	}
	j := auditorJudged{labels: map[posting.Key]eval.Entry{}, marks: map[posting.Key]marks.Entry{}}
	set := map[posting.Key]bool{}
	for k, en := range done {
		if slices.Contains(eval.WouldApplyOptions, en.Labels[eval.WouldApply]) {
			j.labels[k] = en
			j.labelled++
			set[k] = true
		}
	}
	for k, en := range m {
		if en.Mark != marks.None {
			j.marks[k] = en
			j.marked++
			set[k] = true
		}
	}
	for k := range set {
		j.keys = append(j.keys, k)
	}
	slices.Sort(j.keys)
	return j, nil
}

// auditorCalibrateReply is `vani auditor calibrate --json`.
type auditorCalibrateReply struct {
	DryRun bool              `json:"dry_run"`
	Plan   *auditorPlanJSON  `json:"plan,omitempty"`
	Run    *auditorRunResult `json:"run,omitempty"`
	failure
}

type auditorPlanJSON struct {
	Model        string `json:"model"`
	Provider     string `json:"provider"`
	Task         string `json:"task"`
	DigestSHA256 string `json:"kb_digest_sha256"`
	Labelled     int    `json:"labelled"`
	Marked       int    `json:"marked"`
	Judged       int    `json:"judged"`
	NotInLedger  int    `json:"not_in_ledger"`
	NoText       int    `json:"no_text"`
	Already      int    `json:"already_answered"`
	ToAsk        int    `json:"to_ask"`
	// The estimate, present when there is something to ask.
	InputTokens  *teacherTokens `json:"input_tokens,omitempty"`
	PriceSource  string         `json:"price_source,omitempty"`
	InputPerMTok float64        `json:"input_usd_per_mtok,omitempty"`
	OutputPerMTk float64        `json:"output_usd_per_mtok,omitempty"`
	TypicalUSD   float64        `json:"typical_usd"`
	MaxUSD       float64        `json:"max_usd"`
	AuditorCap   float64        `json:"auditor_daily_cap_usd"`
	LLMDailyCap  float64        `json:"llm_daily_cap_usd"`
	// IdentifyingWords says which lists the identifying-word check applied:
	// auditorWordsBoth, or auditorWordsBuiltin when the owner's list was
	// missing or empty (a real run then refuses without
	// --no-identifying-words-file).
	IdentifyingWords string `json:"identifying_words"`
	// BlockedBy says, per part of the shared text, how many identifying terms
	// it holds (never the terms) that make a real run refuse; empty when it
	// would send.
	BlockedBy []string `json:"blocked_by_identifying_terms,omitempty"`
}

type auditorRunResult struct {
	LimitUSD   float64 `json:"limit_usd"`
	Asked      int     `json:"asked"`
	Stored     int     `json:"stored"`
	Unusable   int     `json:"unusable"`
	NoReading  int     `json:"no_reading"`
	ChargedUSD float64 `json:"charged_usd"`
	NotAsked   int     `json:"not_asked"`
	Stopped    string  `json:"stopped,omitempty"`
}

func (e *env) auditorCalibrate(args []string) error {
	fs := e.newFlags("auditor calibrate")
	dry := fs.Bool("dry-run", false, "say what would be sent and what it would cost, and ask nothing")
	budgetUSD := fs.Float64("budget-usd", 0, fmt.Sprintf("the most this run may spend, in dollars, at most %v (required for a real run; no default)", auditorDailyCapUSD))
	modelFlag := fs.String("model", "", "the llm model that answers (default llm.triage_model, else llm.model)")
	conc := fs.Int("concurrency", defaultTeacherConcurrency, fmt.Sprintf("questions in flight (1-%d)", eval.MaxTeacherConcurrency))
	builtinOnly := fs.Bool("no-identifying-words-file", false, "send even though your identifying-words list ("+track.WordsFile+") is missing or empty, so only the built-in list was checked")
	showTerms := fs.Bool("show-terms", false, "name the identifying terms found, on stderr, for your own terminal (never with --json)")
	asJSON := e.jsonFlag(fs, "one JSON object on stdout: the plan, the estimate and what the run did (the text goes to stderr)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *showTerms && *asJSON {
		return usagef("auditor calibrate: --show-terms is for your own terminal and cannot be combined with --json")
	}
	budgetSet := false
	fs.Visit(func(f *flag.Flag) { budgetSet = budgetSet || f.Name == "budget-usd" })
	if !*dry {
		if !budgetSet {
			return usagef("auditor calibrate: a real run needs --budget-usd N (no default); use --dry-run to see the cost first")
		}
		if math.IsNaN(*budgetUSD) || math.IsInf(*budgetUSD, 0) || *budgetUSD <= 0 || *budgetUSD > auditorDailyCapUSD {
			return usagef("auditor calibrate: --budget-usd must be above 0 and at most %v", auditorDailyCapUSD)
		}
	}
	if *conc < 1 || *conc > eval.MaxTeacherConcurrency {
		return usagef("auditor calibrate: --concurrency must be 1-%d", eval.MaxTeacherConcurrency)
	}
	jr := &auditorCalibrateReply{DryRun: *dry}
	o := auditorCalibrateOpts{dry: *dry, budgetUSD: *budgetUSD, model: *modelFlag, conc: *conc, builtinOnly: *builtinOnly, showTerms: *showTerms}
	err := e.textToStderr(*asJSON, func() error { return e.auditorCalibrateBody(jr, o) })
	if !*asJSON {
		return err
	}
	return e.reply(true, jr, err, nil)
}

// auditorCandidates reads, without the ledger lock, the records of keys only
// (their bodies included, read back when evicted) and returns those that can be
// asked about, in key order.
func (e *env) auditorCandidates(keys []posting.Key) (cands []eval.TeacherCandidate, notInLedger, noText int, err error) {
	want := make(map[posting.Key]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	snap, err := ledger.ReadSnapshotScoped(filepath.Join(e.stateDir, "ledger"), func(ledger.Snapshot) func(ledger.Record) bool {
		return func(r ledger.Record) bool { return want[r.Key] }
	})
	if err != nil {
		return nil, 0, 0, fmt.Errorf("auditor: read the ledger: %w", err)
	}
	if snap.Torn {
		fmt.Fprintln(e.stderr, "vani: the ledger's last line is still being written; it is left out")
	}
	byKey := make(map[posting.Key]ledger.Record, len(keys))
	for _, r := range snap.Records {
		if want[r.Key] {
			byKey[r.Key] = r
		}
	}
	for _, k := range keys {
		r, ok := byKey[k]
		switch {
		case !ok:
			notInLedger++
		case strings.TrimSpace(r.Posting.Description) == "":
			noText++
		default:
			p := r.Posting
			p.Key = r.Key
			cands = append(cands, eval.TeacherCandidate{Posting: p})
		}
	}
	return cands, notInLedger, noText, nil
}

// auditorCalibrateOpts is `vani auditor calibrate`'s flags.
type auditorCalibrateOpts struct {
	dry         bool
	budgetUSD   float64
	model       string
	conc        int
	builtinOnly bool // --no-identifying-words-file
	showTerms   bool
}

func (e *env) auditorCalibrateBody(jr *auditorCalibrateReply, o auditorCalibrateOpts) error {
	dry, budgetUSD, conc := o.dry, o.budgetUSD, o.conc
	cfg, model, summary, digest, ident, task, err := e.auditorSetupWords(o.model)
	if err != nil {
		return err
	}
	identifying, personal := auditorIdentifying(ident, summary)
	wordsChecked := auditorWordsBoth
	if !personal {
		// Fail closed: with the owner's list missing or empty, only the built-in
		// list was checked, so the owner's names, employers and schools could be
		// in the digest unseen. A real run needs the owner to say so explicitly.
		wordsChecked = auditorWordsBuiltin
		fmt.Fprintf(e.stderr, "vani: identifying-word check: built-in list only; your own terms were NOT checked (%s in %s is missing or empty)\n", track.WordsFile, track.Dir(cfg, e.stateDir))
		if !dry && !o.builtinOnly {
			return fmt.Errorf("auditor: your identifying-words list (%s in %s) is missing or empty, so only the built-in list was checked; nothing was sent (add your terms to it, or pass --no-identifying-words-file to send anyway)", track.WordsFile, track.Dir(cfg, e.stateDir))
		}
	}
	if o.showTerms {
		for _, f := range identifying {
			fmt.Fprintf(e.stderr, "vani: %s: %s (shown because of --show-terms)\n", f.part, strings.Join(f.terms, ", "))
		}
	}
	if len(identifying) > 0 && !dry {
		return fmt.Errorf("auditor: the text every question carries holds identifying terms (%s); nothing was sent (--show-terms names them in your terminal)", auditorFindings(identifying))
	}
	judged, err := e.auditorJudgedSet()
	if err != nil {
		return err
	}
	cands, notInLedger, noText, err := e.auditorCandidates(judged.keys)
	if err != nil {
		return err
	}
	prices, source, err := teacherPrices(cfg, model)
	if err != nil {
		return fmt.Errorf("auditor: %w", err)
	}

	ctx, cancel := signalContext()
	defer cancel()
	path := e.auditorPath()
	if !dry {
		unlock, lerr := eval.LockTeacher(ctx, path, auditorLockWait)
		if lerr != nil {
			return fmt.Errorf("auditor: %w", lerr)
		}
		defer unlock()
	}
	store, err := eval.OpenTeacherStore(path)
	if err != nil {
		return err
	}
	if store.Skipped > 0 {
		fmt.Fprintf(e.stderr, "vani: auditor readings: %d unreadable lines skipped (a torn tail or a hand edit)\n", store.Skipped)
	}
	var pending []eval.TeacherCandidate
	for _, c := range cands {
		if !store.Has(c.Posting.Key, task.Ident(c.Posting, model)) {
			pending = append(pending, c)
		}
	}
	plan := &auditorPlanJSON{Model: model, Provider: cfg.LLM.Provider, Task: wouldapply.Task, DigestSHA256: digest.SHA256,
		Labelled: judged.labelled, Marked: judged.marked, Judged: len(judged.keys), NotInLedger: notInLedger, NoText: noText,
		Already: len(cands) - len(pending), ToAsk: len(pending), AuditorCap: auditorDailyCapUSD, LLMDailyCap: cfg.Decide.DailyUSD.LLM,
		PriceSource: source, InputPerMTok: float64(prices.Input) / 1000, OutputPerMTk: float64(prices.Output) / 1000,
		IdentifyingWords: wordsChecked}
	for _, f := range identifying {
		plan.BlockedBy = append(plan.BlockedBy, f.String())
	}
	jr.Plan = plan
	if len(identifying) > 0 {
		fmt.Fprintf(e.stdout, "auditor: a real run would REFUSE: the text every question carries holds identifying terms (%s); remove them, or take them off the list in %s, before anything is sent (--show-terms names them in your terminal)\n",
			auditorFindings(identifying), track.WordsFile)
	}
	if !personal && !o.builtinOnly {
		fmt.Fprintf(e.stdout, "auditor: a real run would REFUSE without --no-identifying-words-file: only the built-in identifying-word list was checked\n")
	}
	fmt.Fprintf(e.stdout, "auditor: %d postings judged or marked (%d with a would_apply answer, %d marked); left out: %d not in the ledger, %d with no text; %d already answered by %s under this question and digest; %d to ask\n",
		len(judged.keys), judged.labelled, judged.marked, notInLedger, noText, plan.Already, model, len(pending))
	fmt.Fprintf(e.stdout, "auditor: each question carries the would-apply prompt, your target summary, the full %d-byte digest of your knowledge base (titles, stack terms and bullets, with employers, dates, places and contact details already removed) and one posting, all framed as untrusted content, sent to %s %s\n",
		len(digest.Text), cfg.LLM.Provider, model)
	if len(pending) > 0 {
		var sum, lo, hi int
		lo = math.MaxInt
		var typical, worst int64
		for _, c := range pending {
			m := teacherMeasure(c, task)
			sum += m.Estimate
			lo, hi = min(lo, m.Estimate), max(hi, m.Estimate)
			if ch, err := prices.Charge(llm.Usage{InputTokens: max(m.Estimate, 1), OutputTokens: auditorTypicalOut}); err == nil {
				typical += ch
			}
			if w, err := prices.Reserve(m.Ceiling, llmbackend.DefaultMaxTokens+llmbackend.ReasoningHeadroom); err == nil {
				worst += w
			}
		}
		plan.InputTokens = &teacherTokens{Mean: sum / len(pending), Min: lo, Max: hi, Total: sum}
		plan.TypicalUSD, plan.MaxUSD = float64(typical)/1e9, float64(worst)/1e9
		fmt.Fprintf(e.stdout, "auditor: input tokens per posting (Manvi's prompt.EstimateTokens over the exact messages): mean %d, min %d, max %d, total %d\n", sum/len(pending), lo, hi, sum)
		fmt.Fprintf(e.stdout, "auditor: price from %s: input $%.4f and output $%.4f per million tokens\n", source, plan.InputPerMTok, plan.OutputPerMTk)
		fmt.Fprintf(e.stdout, "auditor: typically about %s (replies of %d tokens), at most %s counting every reply at its full budget and every prompt at its physical ceiling; capped at $%.2f a day for the auditor and by the $%.2f daily llm cap\n",
			usd(typical), auditorTypicalOut, usd(worst), auditorDailyCapUSD, cfg.Decide.DailyUSD.LLM)
	}
	if dry || len(pending) == 0 {
		return nil
	}

	w := &wired{cfg: cfg, stateDir: e.stateDir, warn: e.stderr}
	ledgerPath := w.budgetLedgerPath()
	capFile := filepath.Join(e.stateDir, "budget", auditorBudgetFile)
	auditorCap, err := decide.NewDailyCap(capFile, auditorDailyCapUSD)
	if err != nil {
		return fmt.Errorf("auditor budget: %w", err)
	}
	// decide.DailyCap keys its days by the wall clock's UTC date.
	spentToday, err := readOpenRouterSpend(capFile, time.Now().UTC().Format("2006-01-02"), auditorSpendKey)
	if err != nil {
		return fmt.Errorf("auditor budget: %w", err)
	}
	committed, err := teacherSpent(ctx, ledgerPath)
	if err != nil {
		return fmt.Errorf("auditor: reading today's llm spend: %w", err)
	}
	llmCap := int64(cfg.Decide.DailyUSD.LLM * 1e9)
	limit := min(int64(budgetUSD*1e9), int64((auditorDailyCapUSD-spentToday)*1e9), llmCap-committed)
	if limit <= 0 {
		return fmt.Errorf("auditor: today's budget is spent (auditor $%.2f of $%.2f, llm %s of $%.2f); nothing was asked", spentToday, auditorDailyCapUSD, usd(committed), cfg.Decide.DailyUSD.LLM)
	}
	d, err := teacherDecider(ctx, w, model)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "auditor: spending at most %s (the least of --budget-usd, the auditor's cap headroom and the daily llm cap's headroom)\n", usd(limit))
	// The auditor's cap is charged as questions settle, with what the ledger's
	// resolved spend (settled plus kept reservations; never in-flight ones) grew
	// by since the run began. Every question of the run is resolved into it
	// before it returns and it never falls, so the charge is never less than
	// what the run spent; spend other callers resolved meanwhile is charged too,
	// so it can be more. Charging as it goes means a run that dies part-way has
	// already put what its finished questions cost on the cap; only a question
	// in flight at that moment is unrecorded here (the llm cap's ledger keeps it).
	var charged int64
	run := eval.TeacherRun{
		D: d, Model: model, Summary: summary, Store: store, Task: task, Concurrency: conc, Timeout: auditorTimeout,
		LimitNano: limit, Now: now,
		Spent: func(c context.Context) (int64, error) { return teacherResolved(c, ledgerPath) },
		Worst: func(q decide.Question) int64 { return teacherWorstNano(prices, q) },
		OnSpent: func(c context.Context, since int64) error {
			if since <= charged {
				return nil
			}
			if err := auditorCap.Charge(c, auditorSpendKey, float64(since-charged)/1e9); err != nil {
				return fmt.Errorf("charging the auditor's cap: %w", err)
			}
			charged = since
			return nil
		},
	}
	res, runErr := run.Run(ctx, pending)
	jr.Run = &auditorRunResult{LimitUSD: float64(limit) / 1e9, Asked: res.Asked, Stored: res.Stored, Unusable: res.Unusable,
		NoReading: res.Unread, ChargedUSD: float64(charged) / 1e9, NotAsked: max(len(pending)-res.Stored, 0), Stopped: res.Stopped}
	fmt.Fprintf(e.stdout, "auditor: asked %d, stored %d (%d with an unusable slot), no reading %d; charged %s to the auditor's cap\n",
		res.Asked, res.Stored, res.Unusable, res.Unread, usd(charged))
	if res.Stopped != "" {
		fmt.Fprintf(e.stdout, "auditor: stopped: %s; %d of the %d planned postings were not asked; run again to resume\n", res.Stopped, jr.Run.NotAsked, len(pending))
	}
	if runErr != nil {
		return fmt.Errorf("auditor: %w", runErr)
	}
	if res.Stopped != "" && res.Stopped != eval.TeacherStoppedAtLimit {
		return errors.New("auditor: " + res.Stopped)
	}
	return nil
}

// auditorReportReply is `vani auditor report --json`.
type auditorReportReply struct {
	Model        string `json:"model"`
	TeacherModel string `json:"teacher_model"`
	// TeacherBaseline says which teacher readings the comparison scored.
	TeacherBaseline auditorBaseline        `json:"teacher_baseline"`
	Labelled        int                    `json:"labelled"`
	Marked          int                    `json:"marked"`
	NotInLedger     int                    `json:"not_in_ledger"`
	AuditorAnswered int                    `json:"auditor_answered"`
	Reasons         map[string]int         `json:"auditor_reasons"`
	MissedReasons   map[string]int         `json:"auditor_reasons_when_owner_wanted_and_auditor_said_no"`
	Agreement       *eval.AuditorAgreement `json:"agreement,omitempty"`
	failure
}

// auditorBaseline is what the teacher baseline was made of, over the postings
// the comparison scored (a label item or a mark item that holds a teacher
// reading): how many readings, and how many of them were asked under the
// current question (fixture and target-summary prompt). A reading asked under
// an older summary answered a different question, which handicaps the
// baseline; CurrentOnly restricts the baseline to current readings.
type auditorBaseline struct {
	Scored        int  `json:"scored"`
	ScoredCurrent int  `json:"scored_current_prompt"`
	CurrentOnly   bool `json:"current_only"`
}

func (e *env) auditorReport(args []string) error {
	fs := e.newFlags("auditor report")
	modelFlag := fs.String("model", "", "the auditor model whose answers are measured (default llm.triage_model, else llm.model)")
	teacherFlag := fs.String("teacher-model", "", "the teacher whose triage reading is the baseline (default the model with the most readings of the judged postings)")
	currentOnly := fs.Bool("teacher-current-only", false, "use only teacher readings asked under the current question (fixture and target summary) as the baseline")
	seed := fs.Uint64("seed", 1, "the bootstrap's seed")
	asJSON := e.jsonFlag(fs, "one JSON object on stdout")
	if err := parse(fs, args); err != nil {
		return err
	}
	out := &auditorReportReply{}
	err := e.auditorReportBody(out, *modelFlag, *teacherFlag, *currentOnly, *seed)
	return e.reply(*asJSON, out, err, func() {
		fmt.Fprintf(e.stdout, "# Would-apply auditor: agreement with your answers\n\n")
		fmt.Fprintf(e.stdout, "- auditor %s: %d of the judged postings answered under the current question and digest\n", out.Model, out.AuditorAnswered)
		b := out.TeacherBaseline
		which := "newest reading, whatever question it was asked under"
		if b.CurrentOnly {
			which = "current-question readings only (--teacher-current-only)"
		}
		fmt.Fprintf(e.stdout, "- baseline teacher %s (triage role_fit, %s): %d of the %d scored teacher readings were asked under the current prompt; and the relevance score\n",
			cmp.Or(out.TeacherModel, "(none)"), which, b.ScoredCurrent, b.Scored)
		fmt.Fprintf(e.stdout, "- %d would_apply answers, %d marks; %d not in the ledger\n\n", out.Labelled, out.Marked, out.NotInLedger)
		if out.Agreement != nil {
			fmt.Fprint(e.stdout, out.Agreement.Markdown())
		}
		if len(out.MissedReasons) > 0 {
			fmt.Fprintf(e.stdout, "\nWhen you wanted a posting and the auditor said no, its reason: %s\n", countsLine(out.MissedReasons))
		}
	})
}

// countsLine renders counts as "a 3, b 1" in descending count order.
func countsLine(m map[string]int) string {
	type kv struct {
		k string
		v int
	}
	var xs []kv
	for k, v := range m {
		xs = append(xs, kv{k, v})
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i].v > xs[j].v || xs[i].v == xs[j].v && xs[i].k < xs[j].k })
	var parts []string
	for _, x := range xs {
		parts = append(parts, fmt.Sprintf("%s %d", x.k, x.v))
	}
	return strings.Join(parts, ", ")
}

// auditorGroup is where a judged posting sits: the stratum it was drawn from
// when it was labelled (what the engine had done with it then), else its
// status now.
func auditorGroup(en eval.Entry, status posting.Status) string {
	switch {
	case strings.HasPrefix(en.Source, "dropped:"):
		return eval.GroupDropped
	case strings.HasPrefix(en.Source, "closed:"):
		return eval.GroupClosed
	case strings.HasPrefix(en.Source, "decile:"):
		return eval.GroupKept
	}
	switch status {
	case posting.Dropped:
		return eval.GroupDropped
	case posting.Dead, posting.Duplicate:
		return eval.GroupClosed
	}
	return eval.GroupKept
}

func (e *env) auditorReportBody(out *auditorReportReply, modelFlag, teacherFlag string, currentOnly bool, seed uint64) error {
	_, model, summary, _, task, err := e.auditorSetup(modelFlag)
	if err != nil {
		return err
	}
	out.Model = model
	judged, err := e.auditorJudgedSet()
	if err != nil {
		return err
	}
	out.Labelled, out.Marked = judged.labelled, judged.marked
	aud, err := eval.OpenTeacherStore(e.auditorPath())
	if err != nil {
		return err
	}
	teach, err := eval.OpenTeacherStore(filepath.Join(e.stateDir, eval.TeacherFile))
	if err != nil {
		return err
	}
	teacherModel := teacherFlag
	if teacherModel == "" {
		best := 0
		for m, n := range teach.ModelsOf(judged.keys, "") {
			if n > best || n == best && m < teacherModel {
				teacherModel, best = m, n
			}
		}
	}
	out.TeacherModel = teacherModel
	b, err := e.browse()
	if err != nil {
		return err
	}
	byKey := make(map[posting.Key]ledger.Record, len(b.snap.Records))
	for _, r := range b.snap.Records {
		byKey[r.Key] = r
	}
	current := aud.CurrentOf(model, task)
	curPrompt, curFixture := eval.PromptHash(summary), eval.FixtureSHA()
	teachCur := teach.Current(teacherModel, summary)
	out.TeacherBaseline.CurrentOnly = currentOnly
	out.Reasons, out.MissedReasons = map[string]int{}, map[string]int{}
	score := func(k posting.Key, it *eval.AuditorItem) {
		r := byKey[k]
		res := b.in.rc.ScoreOf(r)
		if !math.IsNaN(res.Relevance) && !math.IsInf(res.Relevance, 0) {
			it.Relevance, it.HasRelevance = res.Relevance, true
		}
		if teacherModel != "" {
			// The baseline is the teacher's newest reading, or with currentOnly
			// its newest reading under the current question; either way the
			// disclosure counts the readings actually scored, with how many were
			// asked under the current question.
			value, isCurrent, ok := "", false, false
			if currentOnly {
				if rd, has := teachCur.Reading(string(k)); has {
					value, isCurrent, ok = rd["role_fit"], true, true
				}
			} else if row, has := teach.Latest(k, "", teacherModel); has {
				value, isCurrent, ok = answeredValue(row, "role_fit"), row.PromptHash == curPrompt && row.FixtureSHA == curFixture, true
			}
			if v, scored := auditorRoleFitScore[value]; ok && scored {
				it.Teacher, it.HasTeacher = v, true
				out.TeacherBaseline.Scored++
				if isCurrent {
					out.TeacherBaseline.ScoredCurrent++
				}
			}
		}
		if rd, ok := current.Reading(string(k)); ok {
			if v, ok := wouldapply.Score(rd[wouldapply.Slot]); ok {
				it.Auditor, it.HasAuditor = v, true
			}
		}
	}
	var items []eval.AuditorItem
	answered := map[posting.Key]bool{}
	for _, k := range judged.keys {
		r, ok := byKey[k]
		if !ok {
			out.NotInLedger++
			continue
		}
		if rd, ok := current.Reading(string(k)); ok {
			if _, ok := wouldapply.Score(rd[wouldapply.Slot]); ok {
				answered[k] = true
				if rs := rd[wouldapply.ReasonSlot]; rs != "" {
					out.Reasons[rs]++
				}
			}
		}
		if en, ok := judged.labels[k]; ok {
			ans := en.Labels[eval.WouldApply]
			it := eval.AuditorItem{Key: k, Group: auditorGroup(en, r.Status), Wanted: ans == "yes" || ans == "maybe", Yes: ans == "yes"}
			score(k, &it)
			items = append(items, it)
			if it.Wanted && it.HasAuditor && it.Auditor == 0 {
				if rd, ok := current.Reading(string(k)); ok && rd[wouldapply.ReasonSlot] != "" {
					out.MissedReasons[rd[wouldapply.ReasonSlot]]++
				}
			}
		}
		if mk, ok := judged.marks[k]; ok && (mk.Mark == marks.Saved || mk.Mark == marks.Applied || mk.Mark == marks.Hidden) {
			it := eval.AuditorItem{Key: k, Group: eval.GroupMarks, Liked: mk.Mark != marks.Hidden}
			score(k, &it)
			items = append(items, it)
		}
	}
	out.AuditorAnswered = len(answered)
	a, err := eval.BuildAuditorAgreement(items, seed)
	if err != nil {
		return err
	}
	out.Agreement = &a
	return nil
}

// answeredValue is a stored row's answered value for slot, "" otherwise.
func answeredValue(r eval.TeacherRow, slot string) string {
	if s, ok := r.Slots[slot]; ok && s.Reading == string(decide.Answered) {
		return s.Value
	}
	return ""
}
