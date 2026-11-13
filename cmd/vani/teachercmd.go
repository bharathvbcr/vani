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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Manvi/manvi/prompt"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/track"
	"github.com/bharathvbcr/decide"
)

// `vani teacher` has the llm tier (Haiku) read, with the very question the
// OpenRouter decision model (d1-ultra) was asked, every posting that model
// triaged, and keeps its readings beside the ledger for Lappi's agreement
// filter (internal/eval/teacher.go). It runs only when asked with an explicit
// --budget-usd, because it sends each posting's text to the provider and spends
// from the daily llm cap; --dry-run says what it would send and cost and asks
// nothing.
//
// What is sent: the triage prompt, the target summary and the posting's title,
// location and description, framed as untrusted content (llmbackend). No
// vault, header, mark or note ever enters a prompt or a stored reading.
//
// --task kb_match asks the kb_match question instead (internal/decide/kbmatch)
// over the same candidates, draw and spend gate: the target summary, the full
// digest of the knowledge base (read only through eligibleBank, never the
// focused per-posting view) and the posting. Its readings go to the same log,
// tagged with the task and the digest's hash, so a knowledge-base change
// re-asks. --keys restricts a run to a saved list of postings, and every run
// saves the keys it will ask in order (--draw-out), so a capped or interrupted
// sample can be resumed as itself after the candidate list has grown.

const (
	defaultTeacherConcurrency = 4
	teacherTypicalOut         = 150 // a typical reply, for the typical-cost line
	// teacherPromptOverhead is the tokens added to the byte count of the exact
	// messages for the request envelope (role markers, framing the provider adds).
	teacherPromptOverhead = 256
	teacherLockWait       = 10 * time.Second
	// teacherMaxBudgetUSD bounds one teacher run's --budget-usd. It was 30 days
	// of the old $20 daily ceiling; it stays $600 now that ceiling is $50.
	teacherMaxBudgetUSD = 600.0
)

// teacherCeilingTokens is a physical upper bound on the input tokens of the
// messages sys and user: a token spans at least one byte, so the prompt cannot
// carry more tokens than bytes, whatever the text density (base64, minified
// code, non-Latin scripts). The estimate (prompt.EstimateTokens) is a typical
// figure and can be low for dense text; this bound cannot be.
func teacherCeilingTokens(sys, user string) int {
	return len(sys) + len(user) + teacherPromptOverhead
}

// teacherWorstNano is the most one question for q can cost at prices p: the
// ceiling of its prompt tokens plus every output token the request allows.
// A bound that cannot be computed admits nothing (fail closed).
func teacherWorstNano(p budget.Prices, q decide.Question) int64 {
	sys, user := llmbackend.Messages(q)
	w, err := p.Reserve(teacherCeilingTokens(sys, user), llmbackend.DefaultMaxTokens+llmbackend.ReasoningHeadroom)
	if err != nil {
		return math.MaxInt64 / 4
	}
	return w
}

// The seams tests replace.
var (
	// teacherDecider builds the decider that asks the model.
	teacherDecider = func(ctx context.Context, w *wired, model string) (decide.Decider, error) {
		p, err := w.provider(ctx)
		if err != nil {
			return nil, err
		}
		lb, err := llmbackend.New(p, llmbackend.Config{Model: model})
		if err != nil {
			return nil, fmt.Errorf("llm backend: %w", err)
		}
		return lb, nil
	}
	// teacherSpent reads the day's committed llm spend (nano-USD) from the
	// budget ledger at path, which every llm call is metered into: everything
	// counted against the daily cap, so the cap's headroom is the cap less it.
	teacherSpent = func(ctx context.Context, path string) (int64, error) {
		s, err := budget.Inspect(ctx, path)
		if err != nil {
			return 0, err
		}
		return s.CommittedNanoUSD(), nil
	}
	// teacherResolved reads the day's resolved llm spend (nano-USD): what the
	// ledger settled from provider usage plus the reservations it kept
	// unproven, leaving out attempts still in flight. It is a run's own meter
	// (eval.TeacherRun.Spent). Each of the run's attempts is resolved into it
	// before its question returns and it never falls, so its growth over a run
	// is at least what the run spent. The committed total is not: it counts
	// other callers' in-flight reservations, and one settling for less during
	// the run made its growth understate the run's spend.
	teacherResolved = func(ctx context.Context, path string) (int64, error) {
		s, err := budget.Inspect(ctx, path)
		if err != nil {
			return 0, err
		}
		return s.SpentNanoUSD + s.RetainedNanoUSD, nil
	}
	// teacherSettled reads the budget ledger at path as it stands, for the
	// run's report (settled spend, kept reservations).
	teacherSettled = func(ctx context.Context, path string) (budget.Snapshot, error) {
		return budget.Inspect(ctx, path)
	}
)

type deciderFunc func(context.Context, decide.Question) (decide.Answer, error)

func (f deciderFunc) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	return f(ctx, q)
}

func (e *env) cmdTeacher(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "freeze":
			return e.cmdTeacherFreeze(args[1:])
		case "cap":
			return e.cmdTeacherCap(args[1:])
		case "report":
			return e.cmdTeacherReport(args[1:])
		}
	}
	fs := e.newFlags("teacher")
	dry := fs.Bool("dry-run", false, "say what would be sent and what it would cost, and ask nothing")
	budgetUSD := fs.Float64("budget-usd", 0, "the most this run may spend, in dollars (required for a real run; no default)")
	modelFlag := fs.String("model", "", "the llm model that reads (default llm.triage_model, else llm.model)")
	of := fs.String("of", "", "the first teacher whose postings are read again (default the OpenRouter decision model's backend name)")
	conc := fs.Int("concurrency", defaultTeacherConcurrency, fmt.Sprintf("questions in flight (1-%d)", eval.MaxTeacherConcurrency))
	maxN := fs.Int("max", 0, "ask about at most this many postings this run (0: all)")
	sampleN := fs.Int("sample", 0, "ask about a uniform random sample of this many of the postings (0: no sampling); drawn before --max, deterministic for --seed")
	seed := fs.Uint64("seed", 0, "the seed of --sample's draw (only with --sample)")
	readingsOf := fs.String("with-readings-of", "", "keep only postings that already have a current reading by this model (this task, fixture and prompt, and for kb_match this knowledge-base digest), applied before --sample")
	taskFlag := fs.String("task", teacherTaskJD, fmt.Sprintf("the question asked: %s (the triage question), %s (does the work match your knowledge base; the full digest is sent) or %s (which of two postings of one stratum would you rather apply to; the frozen exemplar titles are sent; silver labels only)", teacherTaskJD, teacherTaskKB, teacherTaskPair))
	pairsN := fs.Int("pairs", defaultTeacherPairs, fmt.Sprintf("with --task %s: the most pairs to ask about this run (1-%d)", teacherTaskPair, eval.MaxPairsPerRun))
	bothOrders := fs.Bool("both-orders", false, fmt.Sprintf("with --task %s: ask each pair in both orders (doubles the questions; position bias averages out)", teacherTaskPair))
	keysFile := fs.String("keys", "", "ask only about the posting keys this file lists, one per line (an unknown or duplicate key is refused); without --sample they are asked in the file's order")
	allowMissing := fs.Bool(kbAllowMissingFlag, false, "proceed with the built-in identifying-words list alone when the tracks directory has no "+track.WordsFile+", or one with no terms, for the target_summary check and with --task kb_match for what is sent (without it, either refuses)")
	drawOut := fs.String("draw-out", "", "save the drawn keys, in the order they are asked, to this file (default: a new file under the state directory's teacher_draws/); pass it back with --keys to resume the same sample")
	asJSON := e.jsonFlag(fs, "one JSON object on stdout: the plan, the estimate and what the run did (the text goes to stderr)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *taskFlag != teacherTaskJD && *taskFlag != teacherTaskKB && *taskFlag != teacherTaskPair {
		return usagef("teacher: --task must be %s, %s or %s", teacherTaskJD, teacherTaskKB, teacherTaskPair)
	}
	budgetSet, sampleSet, seedSet := false, false, false
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		given[f.Name] = true
		switch f.Name {
		case "budget-usd":
			budgetSet = true
		case "sample":
			sampleSet = true
		case "seed":
			seedSet = true
		}
	})
	if err := checkTeacherPairFlags(*taskFlag, given); err != nil {
		return err
	}
	if sampleSet && *sampleN < 1 {
		return usagef("teacher: --sample must be at least 1")
	}
	if seedSet && !sampleSet && *taskFlag != teacherTaskPair {
		return usagef("teacher: --seed needs --sample")
	}
	if *taskFlag == teacherTaskPair && (*pairsN < 1 || *pairsN > eval.MaxPairsPerRun) {
		return usagef("teacher: --pairs must be 1-%d", eval.MaxPairsPerRun)
	}
	if !*dry {
		if !budgetSet {
			return usagef("teacher: a real run needs --budget-usd N (no default); use --dry-run to see the cost first")
		}
		if math.IsNaN(*budgetUSD) || math.IsInf(*budgetUSD, 0) || *budgetUSD <= 0 || *budgetUSD > teacherMaxBudgetUSD {
			return usagef("teacher: --budget-usd must be above 0 and at most %v", teacherMaxBudgetUSD)
		}
	}
	if *conc < 1 || *conc > eval.MaxTeacherConcurrency {
		return usagef("teacher: --concurrency must be 1-%d", eval.MaxTeacherConcurrency)
	}
	if *maxN < 0 {
		return usagef("teacher: --max must not be negative")
	}
	o := teacherOpts{dry: *dry, budgetUSD: *budgetUSD, model: *modelFlag, of: *of, conc: *conc, maxN: *maxN,
		sample: sampleSet, sampleN: *sampleN, seed: *seed, readingsOf: *readingsOf, task: *taskFlag, keys: *keysFile, drawOut: *drawOut,
		pairs: *pairsN, bothOrders: *bothOrders, seedSet: seedSet, allowMissingWords: *allowMissing}
	jr := &teacherReply{DryRun: *dry}
	run := e.teacherRun
	if o.task == teacherTaskPair {
		run = e.teacherPairs
	}
	err := e.textToStderr(*asJSON, func() error { return run(o, jr) })
	if !*asJSON {
		return err
	}
	return e.reply(true, jr, err, nil)
}

// teacherOpts are teacher's checked flags.
type teacherOpts struct {
	dry                   bool
	budgetUSD             float64
	model, of, readingsOf string
	task, keys, drawOut   string
	conc, maxN, sampleN   int
	sample                bool
	seed                  uint64
	// pairs, bothOrders and seedSet belong to --task would_apply_pair.
	pairs      int
	bothOrders bool
	seedSet    bool
	// allowMissingWords is --allow-missing-identifying-words: the target_summary
	// check for every task, and the knowledge-base redaction for kb_match.
	allowMissingWords bool
}

// The --task names.
const (
	teacherTaskJD   = "jd_triage"
	teacherTaskKB   = "kb_match"
	teacherTaskPair = "would_apply_pair"
)

// teacherKBMatchFixture identifies the kb_match question's definition, the
// role the shared fixture's hash plays for jd_triage: the task, the slot, the
// prompt (kbmatch.Prompt) and the ordered options (jobinsight.KBMatchOrder).
func teacherKBMatchFixture() string {
	h := sha256.New()
	for _, s := range append([]string{kbmatch.Task, kbmatch.Slot, kbmatch.Prompt}, jobinsight.KBMatchOrder[:]...) {
		fmt.Fprintf(h, "%d:", len(s))
		io.WriteString(h, s)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// teacherKBMatchPromptHash fingerprints everything a kb_match question sends
// apart from the posting and the digest (which a row carries as its own hash):
// the system prompt and the user message of a full-digest question for an
// empty posting and an empty digest, so the frame, the section headings, the
// options and the target summary are all in it, and the summary once more, as
// eval.PromptHash does.
func teacherKBMatchPromptHash(d kbmatch.Digest, summary string) (string, error) {
	// Frame, not a question built from a digest: it carries no knowledge-base text,
	// and refuses a digest the redactor did not make or a summary holding a term.
	q, err := d.Frame(summary)
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

// teacherKBMatchTask is the kb_match question in full-digest mode (`vani
// kbmatch run --no-retrieve`): the target summary, the whole digest, then the
// posting, never the compact digest with embedding-picked lines, so every
// question for one digest shares one head. The model's answer is kept as it
// is, an abstention included (kbmatch.Judge would turn that into an error).
func teacherKBMatchTask(summary string, d kbmatch.Digest) (eval.TeacherTask, error) {
	prompt, err := teacherKBMatchPromptHash(d, summary)
	if err != nil {
		return eval.TeacherTask{}, err
	}
	return eval.TeacherTask{
		Name: kbmatch.Task, Fixture: teacherKBMatchFixture(), Prompt: prompt, Digest: d.SHA256,
		Ask: func(ctx context.Context, dc decide.Decider, p posting.Posting) (decide.Answer, error) {
			// Prepare, the only way to a question: a digest not built through the redactor, or a
			// term that survived it, is refused here, per posting, as well as once
			// before the run (teacherRun).
			q, _, err := kbmatch.Prepare(p, summary, d, nil)
			if err != nil {
				return decide.Answer{}, err
			}
			return dc.Decide(ctx, q)
		},
	}, nil
}

// teacherReply is `vani teacher --json`. Plan is what would be asked and what
// it could cost; Run is what a real run did. A run that stopped carries the
// failure beside them.
type teacherReply struct {
	DryRun bool `json:"dry_run"`
	// IdentifyingWords and Redactions are set for --task kb_match only (the
	// jd_triage question carries no knowledge base): the identifying-words
	// file's state and how many terms the digest had removed (never which).
	IdentifyingWords string           `json:"identifying_words,omitempty"`
	Redactions       *int             `json:"redactions,omitempty"`
	Plan             *teacherPlanJSON `json:"plan,omitempty"`
	Run              *teacherRunJSON  `json:"run,omitempty"`
	// Pairs is what a --task would_apply_pair run says beyond the shared plan.
	Pairs *teacherPairsJSON `json:"pairs,omitempty"`
	failure
}

type teacherPlanJSON struct {
	// Task is the --task asked; DigestSHA256 is the knowledge-base digest's
	// hash a kb_match question carries (never its text).
	Task         string `json:"task"`
	DigestSHA256 string `json:"kb_digest_sha256,omitempty"`
	First        string `json:"first"`
	Model        string `json:"model"`
	Provider     string `json:"provider"`
	// KeptByKeys, KeptWithReadings and Sampled are set by --keys,
	// --with-readings-of and --sample.
	KeptByKeys       *int `json:"kept_by_keys,omitempty"`
	KeptWithReadings *int `json:"kept_with_readings,omitempty"`
	Sampled          *int `json:"sampled,omitempty"`
	// DrawFile holds the DrawKeys keys of the sample in the order they are
	// asked (sha256 of the file is DrawSHA256); --keys DrawFile resumes it.
	DrawFile   string `json:"draw_file,omitempty"`
	DrawKeys   int    `json:"draw_keys,omitempty"`
	DrawSHA256 string `json:"draw_sha256,omitempty"`
	Candidates int    `json:"candidates"`
	NoText     int    `json:"left_out_no_text"`
	Inherited  int    `json:"left_out_copied"`
	Already    int    `json:"already_stored"`
	ToAsk      int    `json:"to_ask"`
	// The estimate, present when there is something to ask.
	InputTokens *teacherTokens `json:"input_tokens,omitempty"`
	PriceSource string         `json:"price_source,omitempty"`
	TypicalUSD  float64        `json:"typical_usd"`
	MaxUSD      float64        `json:"max_usd"`
	DailyCapUSD float64        `json:"daily_cap_usd"`
}

type teacherTokens struct {
	Mean  int `json:"mean"`
	Min   int `json:"min"`
	Max   int `json:"max"`
	Total int `json:"total"`
}

type teacherRunJSON struct {
	LimitUSD  float64 `json:"limit_usd"`
	Asked     int     `json:"asked"`
	Stored    int     `json:"stored"`
	Unusable  int     `json:"unusable"`
	NoReading int     `json:"no_reading"`
	Already   int     `json:"already_stored"`
	// SettledUSD is the budget ledger's own delta; when it could not be read
	// back, MeteredUSD is the gate's count instead and LedgerError says why.
	SettledUSD        *float64 `json:"settled_usd,omitempty"`
	RetainedUSD       *float64 `json:"retained_usd,omitempty"`
	RemainingTodayUSD *float64 `json:"remaining_today_usd,omitempty"`
	MeteredUSD        *float64 `json:"metered_usd,omitempty"`
	LedgerError       string   `json:"ledger_error,omitempty"`
	NotAsked          int      `json:"not_asked"`
	Stopped           string   `json:"stopped,omitempty"`
}

func (e *env) teacherRun(o teacherOpts, jr *teacherReply) error {
	cfg, err := e.loadPromptConfig(o.allowMissingWords)
	if err != nil {
		return err
	}
	model := o.model
	if model == "" {
		model = cfg.LLM.EffectiveTriageModel()
	}
	if cfg.LLM.Provider == "" || model == "" {
		return errors.New("teacher: no llm provider and model are configured")
	}
	first := o.of
	if first == "" {
		if !cfg.OpenRouterConfigured() {
			return errors.New("teacher: no OpenRouter decision model is configured; name the first teacher with --of")
		}
		first = cfg.Decide.OpenRouterName()
	}
	summary, err := jdtriage.NormalizeSummary(cfg.TargetSummary)
	if err != nil {
		return err
	}
	task := eval.JDTriageTask(summary)
	var digestBytes int
	if o.task == teacherTaskKB {
		// The knowledge base only through eligibleBank, which fails closed: with
		// no selections or no rkb there is no digest and nothing is asked, a dry
		// run included (its prompt hash and measure need the digest).
		// Redacted with the owner's identifying-words list, which must exist
		// (or --allow-missing-identifying-words), and checked once here so a term
		// in the summary or digest stops the run before anything is asked.
		w, words, werr := kbmatchWords(cfg, e.stateDir, true, o.allowMissingWords)
		jr.IdentifyingWords = words
		if werr != nil {
			return fmt.Errorf("teacher: %w", werr)
		}
		_, d, s, derr := kbmatchDigest(cfg, w)
		if derr != nil {
			return fmt.Errorf("teacher: %w", derr)
		}
		jr.Redactions = &d.Redactions
		if cerr := kbmatch.CheckOwner(s, d); cerr != nil {
			return fmt.Errorf("teacher: %w", cerr)
		}
		kt, terr := teacherKBMatchTask(s, d)
		if terr != nil {
			return fmt.Errorf("teacher: %w", terr)
		}
		summary, task, digestBytes = s, kt, len(d.Text)
	}
	var keys []posting.Key
	if o.keys != "" {
		keys, err = readTeacherKeys(o.keys)
		if err != nil {
			return err
		}
	}
	names, err := e.evalNames()
	if err != nil {
		return err
	}
	prices, source, err := teacherPrices(cfg, model)
	if err != nil {
		return fmt.Errorf("teacher: %w", err)
	}
	recs, err := e.ledgerRecords()
	if err != nil {
		return err
	}
	sel := eval.TeacherCandidates(recs, names, first, nil)
	var keptByKeys *int
	if keys != nil {
		kept, missing := eval.RestrictToKeys(sel.Candidates, keys)
		if len(missing) > 0 {
			return teacherUnknownKeys(o.keys, recs, missing, first)
		}
		fmt.Fprintf(e.stdout, "teacher: kept the %d postings %s lists, of %d read by %s\n", len(kept), filepath.Base(o.keys), len(sel.Candidates), first)
		sel.Candidates = kept
		n := len(kept)
		keptByKeys = &n
	}

	ctx, cancel := signalContext()
	defer cancel()
	path := filepath.Join(e.stateDir, eval.TeacherFile)
	if !o.dry {
		unlock, lerr := eval.LockTeacher(ctx, path, teacherLockWait)
		if lerr != nil {
			return lerr
		}
		defer unlock()
	}
	store, err := eval.OpenTeacherStore(path)
	if err != nil {
		return err
	}
	if store.Skipped > 0 {
		fmt.Fprintf(e.stderr, "vani: teacher readings: %d unreadable lines skipped (a torn tail or a hand edit)\n", store.Skipped)
	}
	// The candidates are narrowed before anything is asked: to --keys (above),
	// then to those the named model already read under this task (so a sample can
	// be compared against it), then to a uniform sample of what is left; --max and
	// the already-stored check apply to the sample, in the order it was drawn.
	var kept, sampled *int
	if o.readingsOf != "" {
		before := len(sel.Candidates)
		sel.Candidates = eval.WithCurrentReading(sel.Candidates, store.CurrentOf(o.readingsOf, task))
		n := len(sel.Candidates)
		kept = &n
		fmt.Fprintf(e.stdout, "teacher: kept %d of %d postings that have a current reading by %s\n", len(sel.Candidates), before, o.readingsOf)
	}
	if o.sample {
		// A draw is over the candidates sorted by key, so it depends on which keys
		// --keys names and never on the order the file lists them in.
		slices.SortFunc(sel.Candidates, func(a, b eval.TeacherCandidate) int { return cmp.Compare(a.Posting.Key, b.Posting.Key) })
		drawn, err := eval.SampleCandidates(sel.Candidates, o.sampleN, o.seed)
		if err != nil {
			return usagef("teacher: %v", err)
		}
		fmt.Fprintf(e.stdout, "teacher: sampled %d of %d postings (seed %d)\n", len(drawn), len(sel.Candidates), o.seed)
		sel.Candidates = drawn
		n := len(drawn)
		sampled = &n
	}
	var pending []eval.TeacherCandidate
	for _, c := range sel.Candidates {
		if !store.Has(c.Posting.Key, task.Ident(c.Posting, model)) {
			pending = append(pending, c)
		}
	}
	already := len(sel.Candidates) - len(pending)
	if o.maxN > 0 && len(pending) > o.maxN {
		pending = pending[:o.maxN]
	}

	// Measure what each question would send: the task builds it exactly as a
	// real run does, and the capture stops it there.
	toks := make([]teacherPrompt, len(pending))
	for i, c := range pending {
		toks[i] = teacherMeasure(c, task)
	}
	capNano := int64(cfg.Decide.DailyUSD.LLM * 1e9)
	jr.Plan = e.teacherReport(cfg, model, first, source, prices, sel, already, toks, capNano, o.task, digestBytes)
	jr.Plan.KeptWithReadings, jr.Plan.Sampled, jr.Plan.KeptByKeys = kept, sampled, keptByKeys
	jr.Plan.Task = o.task
	if o.task == teacherTaskKB {
		jr.Plan.DigestSHA256 = task.Digest
	}
	// The sample as it will be asked (the stored ones are skipped, --max takes a
	// prefix), saved before anything is asked so an interrupted or capped run can
	// be resumed as the same sample with --keys. A dry run saves it too: the
	// sample it priced is then the sample a real run asks.
	if len(sel.Candidates) > 0 {
		path, err := e.saveTeacherDraw(o, model, sel.Candidates)
		if err != nil {
			return fmt.Errorf("teacher: saving the drawn keys: %w", err)
		}
		jr.Plan.DrawFile, jr.Plan.DrawKeys, jr.Plan.DrawSHA256 = path, len(sel.Candidates), eval.HashText(string(eval.KeyListBytes(sel.Candidates)))
		fmt.Fprintf(e.stdout, "teacher: the %d drawn keys, in the order they are asked, are saved to %s (resume this sample with --keys)\n", len(sel.Candidates), path)
	}
	if o.dry || len(pending) == 0 {
		return nil
	}

	// One budget day for the whole run: the provider's ledger file and the
	// gate's spend reads both come from w.budgetDay, so a run that crosses UTC
	// midnight never reads one day's cap and meters into another's.
	w := &wired{cfg: cfg, stateDir: e.stateDir, warn: e.stderr}
	ledgerPath := w.budgetLedgerPath()
	d, err := teacherDecider(ctx, w, model)
	if err != nil {
		return err
	}
	committed, err := teacherSpent(ctx, ledgerPath)
	if err != nil {
		return fmt.Errorf("teacher: reading today's llm spend: %w", err)
	}
	spent := func(c context.Context) (int64, error) { return teacherResolved(c, ledgerPath) }
	before, beforeErr := teacherSettled(ctx, ledgerPath)
	limit := min(int64(o.budgetUSD*1e9), capNano-committed)
	if limit <= 0 {
		return fmt.Errorf("teacher: today's llm cap ($%.2f) is already spent; nothing was asked", cfg.Decide.DailyUSD.LLM)
	}
	fmt.Fprintf(e.stdout, "teacher: spending at most %s (the lesser of --budget-usd and the $%.2f daily cap's headroom)\n", usd(limit), cfg.Decide.DailyUSD.LLM)
	run := eval.TeacherRun{
		D: d, Model: model, Summary: summary, Store: store, Task: task, Concurrency: o.conc,
		LimitNano: limit, Spent: spent, Now: now,
		Worst: func(q decide.Question) int64 { return teacherWorstNano(prices, q) },
	}
	res, err := run.Run(ctx, pending)
	after, afterErr := teacherSettled(context.WithoutCancel(ctx), ledgerPath)
	jr.Run = e.teacherSummary(res, already, len(pending), before, beforeErr, after, afterErr, capNano)
	jr.Run.LimitUSD = float64(limit) / 1e9
	if err != nil {
		return fmt.Errorf("teacher: %w", err)
	}
	if res.Stopped != "" && res.Stopped != eval.TeacherStoppedAtLimit {
		return errors.New("teacher: " + res.Stopped)
	}
	return nil
}

// teacherSummary reports what the run did. Spend is the Manvi ledger's own
// settled delta (provider-reported usage), not an estimate; reservations kept
// because a charge could not be proven are reported beside it, and the budget
// remaining is the day's cap less everything the ledger counts against it.
// "Run again" is said only when something remains to ask.
func (e *env) teacherSummary(res eval.TeacherResult, already, planned int, before budget.Snapshot, beforeErr error, after budget.Snapshot, afterErr error, capNano int64) *teacherRunJSON {
	remaining := planned - res.Stored // an unread question stored nothing and is asked again
	out := &teacherRunJSON{Asked: res.Asked, Stored: res.Stored, Unusable: res.Unusable, NoReading: res.Unread, Already: res.Skipped + already,
		NotAsked: max(remaining, 0), Stopped: res.Stopped}
	dollars := func(nano int64) *float64 { v := float64(nano) / 1e9; return &v }
	fmt.Fprintf(e.stdout, "teacher: asked %d, stored %d (%d with an unusable slot), no reading %d, already stored %d\n",
		res.Asked, res.Stored, res.Unusable, res.Unread, res.Skipped+already)
	if err := cmp.Or(beforeErr, afterErr); err != nil {
		out.MeteredUSD, out.LedgerError = dollars(res.SpentNano), err.Error()
		fmt.Fprintf(e.stdout, "teacher: the budget ledger could not be read back (%v); gate-metered spend (resolved delta: settled plus kept reservations, at least this run's own) %s\n", err, usd(res.SpentNano))
	} else {
		out.SettledUSD, out.RetainedUSD = dollars(after.SpentNanoUSD-before.SpentNanoUSD), dollars(after.RetainedNanoUSD-before.RetainedNanoUSD)
		out.RemainingTodayUSD = dollars(max(capNano-after.CommittedNanoUSD(), 0))
		fmt.Fprintf(e.stdout, "teacher: settled spend %s (ledger delta, provider-reported usage), plus %s of reservations kept unproven; budget remaining today %s of %s\n",
			usd(after.SpentNanoUSD-before.SpentNanoUSD), usd(after.RetainedNanoUSD-before.RetainedNanoUSD),
			usd(max(capNano-after.CommittedNanoUSD(), 0)), usd(capNano))
	}
	switch {
	case res.Stopped != "":
		fmt.Fprintf(e.stdout, "teacher: stopped: %s; %d of the %d planned postings were not asked", res.Stopped, max(remaining, 0), planned)
		if remaining > 0 {
			fmt.Fprint(e.stdout, "; run again to resume")
		}
		fmt.Fprintln(e.stdout)
	case remaining > 0:
		fmt.Fprintf(e.stdout, "teacher: %d planned postings were not asked; run again to resume\n", remaining)
	}
	return out
}

// teacherPrompt measures one question's prompt: Estimate is the typical input
// tokens, the system prompt and user message the backend would send counted
// with Manvi's documented BPE estimator (prompt.EstimateTokens), an estimate and
// not a tokenizer; Ceiling is the physical upper bound the spend gate reserves.
type teacherPrompt struct{ Estimate, Ceiling int }

// teacherMeasure builds the question for c exactly as a real run does and
// measures it.
func teacherMeasure(c eval.TeacherCandidate, t eval.TeacherTask) teacherPrompt {
	var m teacherPrompt
	_, _ = t.Ask(context.Background(), deciderFunc(func(_ context.Context, q decide.Question) (decide.Answer, error) {
		sys, user := llmbackend.Messages(q)
		m = teacherPrompt{Estimate: prompt.EstimateTokens(sys + "\n" + user), Ceiling: teacherCeilingTokens(sys, user)}
		return decide.Answer{}, nil
	}), c.Posting)
	return m
}

// readTeacherKeys reads --keys (eval.ReadKeyList's rules), refusing anything
// but a regular file of bounded size.
func readTeacherKeys(path string) ([]posting.Key, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, usagef("teacher: --keys: %v", err)
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, usagef("teacher: --keys %s is not a regular file", filepath.Base(path))
	}
	keys, err := eval.ReadKeyList(f)
	if err != nil {
		return nil, usagef("teacher: --keys %s: %v", filepath.Base(path), err)
	}
	return keys, nil
}

// teacherUnknownKeys refuses a --keys file that names postings the run cannot
// ask about, naming them and saying which are not in the ledger at all and
// which are but were left out (not read by first, a copied reading, or no
// text to ask about).
func teacherUnknownKeys(path string, recs []ledger.Record, missing []posting.Key, first string) error {
	inLedger := make(map[posting.Key]bool, len(recs))
	for _, r := range recs {
		inLedger[r.Key] = true
	}
	var absent, leftOut []posting.Key
	for _, k := range missing {
		if inLedger[k] {
			leftOut = append(leftOut, k)
		} else {
			absent = append(absent, k)
		}
	}
	var parts []string
	if len(absent) > 0 {
		parts = append(parts, fmt.Sprintf("%d not in the ledger: %s", len(absent), eval.NamedKeys(absent)))
	}
	if len(leftOut) > 0 {
		parts = append(parts, fmt.Sprintf("%d in the ledger but not askable (not read by %s, a copied reading, or no text): %s", len(leftOut), first, eval.NamedKeys(leftOut)))
	}
	return usagef("teacher: --keys %s names %d posting(s) this run cannot ask about; nothing was asked: %s", filepath.Base(path), len(missing), strings.Join(parts, "; "))
}

// saveTeacherDraw writes the drawn keys to --draw-out, or to a new file under
// the state directory's teacher_draws/ named for the time, task, model and
// the list's hash, and returns the path.
func (e *env) saveTeacherDraw(o teacherOpts, model string, cands []eval.TeacherCandidate) (string, error) {
	path := o.drawOut
	if path == "" {
		sum := eval.HashText(string(eval.KeyListBytes(cands)))
		name := fmt.Sprintf("%s-%s-%s-%s.keys", now().UTC().Format("20060102T150405Z"), o.task, teacherFileWord(model), sum[:12])
		path = filepath.Join(e.stateDir, teacherDrawsDir, name)
	}
	return path, eval.WriteKeyList(path, cands)
}

// teacherDrawsDir holds the saved draws inside the state directory.
const teacherDrawsDir = "teacher_draws"

// teacherFileWord keeps the letters, digits, dots and dashes of s, so a model
// name can be part of a file name.
func teacherFileWord(s string) string {
	b := []byte(s)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-') {
			b[i] = '_'
		}
	}
	if len(b) > 64 {
		b = b[:64]
	}
	return string(b)
}

// teacherPrices are the prices the llm provider meters model at: the operator's
// configured rates for the configured model and the provider's published table
// (which prices Haiku in two tiers by prompt length) for any other model
// (newPricedProvider.PriceOf). source says which.
func teacherPrices(cfg config.Config, model string) (budget.Prices, string, error) {
	if cfg.LLM.Provider == "anthropic" && model != cfg.LLM.Model {
		if p, ok := anthropic.PriceOf(model); ok && p.Validate() == nil {
			return p, "Anthropic's published rate table (Manvi llm/anthropic) for " + model, nil
		}
	}
	in, out, err := cfg.LLM.Prices()
	if err != nil {
		return budget.Prices{}, "", err
	}
	p := budget.Prices{Rates: budget.Rates{Input: in, Output: out, CacheWrite: in, CacheRead: in}}
	return p, "the configured llm prices", p.Validate()
}

// usd formats nano-USD with the four decimals a per-posting price needs.
func usd(nano int64) string { return fmt.Sprintf("$%.4f", float64(nano)/1e9) }

// teacherCost is what a list of measured questions would cost at some prices:
// the input tokens' sum, smallest and largest, how many are over the long-prompt
// tier, and the typical and worst-case cost in nano-USD (typical counts a reply
// of teacherTypicalOut tokens, worst every reply at its full budget and every
// prompt at its physical ceiling).
type teacherCost struct {
	sum, lo, hi, long int
	typical, worst    int64
}

func teacherCostOf(p budget.Prices, toks []teacherPrompt) teacherCost {
	c := teacherCost{lo: math.MaxInt}
	for _, m := range toks {
		n := m.Estimate
		c.sum += n
		c.lo, c.hi = min(c.lo, n), max(c.hi, n)
		if p.LongPromptAbove > 0 && n > p.LongPromptAbove {
			c.long++
		}
		if ch, err := p.Charge(llm.Usage{InputTokens: max(n, 1), OutputTokens: teacherTypicalOut}); err == nil {
			c.typical += ch
		}
		if w, err := p.Reserve(m.Ceiling, llmbackend.DefaultMaxTokens+llmbackend.ReasoningHeadroom); err == nil {
			c.worst += w
		}
	}
	return c
}

func (e *env) teacherReport(cfg config.Config, model, first, source string, p budget.Prices, sel eval.TeacherSelection, already int, toks []teacherPrompt, capNano int64, task string, digestBytes int) *teacherPlanJSON {
	plan := &teacherPlanJSON{First: first, Model: model, Provider: cfg.LLM.Provider, Candidates: len(sel.Candidates), NoText: sel.NoText,
		Inherited: sel.Inherited, Already: already, ToAsk: len(toks), DailyCapUSD: cfg.Decide.DailyUSD.LLM}
	ident := "this model, fixture and prompt"
	if task == teacherTaskKB {
		ident = "this task, model, fixture, prompt and knowledge-base digest"
	}
	fmt.Fprintf(e.stdout, "teacher: %d postings read by %s (left out: %d with no text, %d copied readings); %d already stored for %s; %d to ask\n",
		len(sel.Candidates), first, sel.NoText, sel.Inherited, already, ident, len(toks))
	if len(toks) == 0 {
		return plan
	}
	c := teacherCostOf(p, toks)
	sum, lo, hi, long, typical, worst := c.sum, c.lo, c.hi, c.long, c.typical, c.worst
	plan.InputTokens = &teacherTokens{Mean: sum / len(toks), Min: lo, Max: hi, Total: sum}
	plan.PriceSource, plan.TypicalUSD, plan.MaxUSD = source, float64(typical)/1e9, float64(worst)/1e9
	if task == teacherTaskKB {
		fmt.Fprintf(e.stdout, "teacher: each question carries the kb_match prompt, your target summary, the full %d-byte digest of your knowledge base (titles, stack terms and bullets, with employers, dates, places and contact details already removed; no embedding-picked lines) and one posting, all framed as untrusted content, sent to %s %s\n", digestBytes, cfg.LLM.Provider, model)
	} else {
		fmt.Fprintf(e.stdout, "teacher: each question carries the triage prompt, your target summary and one posting framed as untrusted content, sent to %s %s\n", cfg.LLM.Provider, model)
	}
	fmt.Fprintf(e.stdout, "teacher: input tokens per posting (estimated with Manvi's prompt.EstimateTokens over the exact messages): mean %d, min %d, max %d, total %d\n", sum/len(toks), lo, hi, sum)
	fmt.Fprintf(e.stdout, "teacher: price from %s: input $%.4f and output $%.4f per million tokens", source, float64(p.Input)/1000, float64(p.Output)/1000)
	if p.LongPromptAbove > 0 {
		fmt.Fprintf(e.stdout, "; above %d prompt tokens input $%.4f and output $%.4f (%d postings are that long)", p.LongPromptAbove, float64(p.LongRates.Input)/1000, float64(p.LongRates.Output)/1000, long)
	}
	fmt.Fprintln(e.stdout)
	fmt.Fprintf(e.stdout, "teacher: typically about %s (replies of %d tokens), at most %s counting every reply at its full budget and every prompt at its physical ceiling (one token per byte, plus %d for the envelope), the bound the spend gate reserves\n",
		usd(typical), teacherTypicalOut, usd(worst), teacherPromptOverhead)
	if capNano > 0 {
		days := (worst + capNano - 1) / capNano
		fmt.Fprintf(e.stdout, "teacher: the daily llm cap is $%.2f: at most %d day(s) at the worst case if nothing else spends from it\n", cfg.Decide.DailyUSD.LLM, days)
	}
	return plan
}
