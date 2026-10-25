package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"path/filepath"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Manvi/manvi/prompt"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/eval"
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
	// budget ledger at path, which every llm call is metered into.
	teacherSpent = func(ctx context.Context, path string) (int64, error) {
		s, err := budget.Inspect(ctx, path)
		if err != nil {
			return 0, err
		}
		return s.CommittedNanoUSD(), nil
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
	fs := e.newFlags("teacher")
	dry := fs.Bool("dry-run", false, "say what would be sent and what it would cost, and ask nothing")
	budgetUSD := fs.Float64("budget-usd", 0, "the most this run may spend, in dollars (required for a real run; no default)")
	modelFlag := fs.String("model", "", "the llm model that reads (default llm.triage_model, else llm.model)")
	of := fs.String("of", "", "the first teacher whose postings are read again (default the OpenRouter decision model's backend name)")
	conc := fs.Int("concurrency", defaultTeacherConcurrency, fmt.Sprintf("questions in flight (1-%d)", eval.MaxTeacherConcurrency))
	maxN := fs.Int("max", 0, "ask about at most this many postings this run (0: all)")
	sampleN := fs.Int("sample", 0, "ask about a uniform random sample of this many of the postings (0: no sampling); drawn before --max, deterministic for --seed")
	seed := fs.Uint64("seed", 0, "the seed of --sample's draw (only with --sample)")
	readingsOf := fs.String("with-readings-of", "", "keep only postings that already have a stored reading by this model (this fixture and prompt), applied before --sample")
	if err := parse(fs, args); err != nil {
		return err
	}
	budgetSet, sampleSet, seedSet := false, false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "budget-usd":
			budgetSet = true
		case "sample":
			sampleSet = true
		case "seed":
			seedSet = true
		}
	})
	if sampleSet && *sampleN < 1 {
		return usagef("teacher: --sample must be at least 1")
	}
	if seedSet && !sampleSet {
		return usagef("teacher: --seed needs --sample")
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
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	model := *modelFlag
	if model == "" {
		model = cfg.LLM.EffectiveTriageModel()
	}
	if cfg.LLM.Provider == "" || model == "" {
		return errors.New("teacher: no llm provider and model are configured")
	}
	first := *of
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

	ctx, cancel := signalContext()
	defer cancel()
	path := filepath.Join(e.stateDir, eval.TeacherFile)
	if !*dry {
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
	// The candidates are narrowed before anything is asked: first to those the
	// named model already read (so a sample can be compared against it), then to
	// a uniform sample of what is left; --max and the already-stored check apply
	// to the sample, in the order it was drawn.
	if *readingsOf != "" {
		before := len(sel.Candidates)
		sel.Candidates = eval.WithCurrentReading(sel.Candidates, store.Current(*readingsOf, summary))
		fmt.Fprintf(e.stdout, "teacher: kept %d of %d postings that have a current reading by %s\n", len(sel.Candidates), before, *readingsOf)
	}
	if sampleSet {
		drawn, err := eval.SampleCandidates(sel.Candidates, *sampleN, *seed)
		if err != nil {
			return usagef("teacher: %v", err)
		}
		fmt.Fprintf(e.stdout, "teacher: sampled %d of %d postings (seed %d)\n", len(drawn), len(sel.Candidates), *seed)
		sel.Candidates = drawn
	}
	var pending []eval.TeacherCandidate
	fixture, ph := eval.FixtureSHA(), eval.PromptHash(summary)
	for _, c := range sel.Candidates {
		if !store.Has(c.Posting.Key, eval.TeacherIdent{Text: eval.HashText(c.Posting.Description), Model: model, Fixture: fixture, Prompt: ph}) {
			pending = append(pending, c)
		}
	}
	already := len(sel.Candidates) - len(pending)
	if *maxN > 0 && len(pending) > *maxN {
		pending = pending[:*maxN]
	}

	// Measure what each question would send: the Triager builds it exactly as a
	// real run does, and the capture stops it there.
	toks := make([]teacherPrompt, len(pending))
	for i, c := range pending {
		toks[i] = teacherMeasure(c, summary)
	}
	capNano := int64(cfg.Decide.DailyUSD.LLM * 1e9)
	e.teacherReport(cfg, model, first, source, prices, sel, already, toks, capNano)
	if *dry || len(pending) == 0 {
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
	spent := func(c context.Context) (int64, error) { return teacherSpent(c, ledgerPath) }
	committed, err := spent(ctx)
	if err != nil {
		return fmt.Errorf("teacher: reading today's llm spend: %w", err)
	}
	before, beforeErr := teacherSettled(ctx, ledgerPath)
	limit := min(int64(*budgetUSD*1e9), capNano-committed)
	if limit <= 0 {
		return fmt.Errorf("teacher: today's llm cap ($%.2f) is already spent; nothing was asked", cfg.Decide.DailyUSD.LLM)
	}
	fmt.Fprintf(e.stdout, "teacher: spending at most %s (the lesser of --budget-usd and the $%.2f daily cap's headroom)\n", usd(limit), cfg.Decide.DailyUSD.LLM)
	run := eval.TeacherRun{
		D: d, Model: model, Summary: summary, Store: store, Concurrency: *conc,
		LimitNano: limit, Spent: spent, Now: now,
		Worst: func(q decide.Question) int64 { return teacherWorstNano(prices, q) },
	}
	res, err := run.Run(ctx, pending)
	after, afterErr := teacherSettled(context.WithoutCancel(ctx), ledgerPath)
	e.teacherSummary(res, already, len(pending), before, beforeErr, after, afterErr, capNano)
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
func (e *env) teacherSummary(res eval.TeacherResult, already, planned int, before budget.Snapshot, beforeErr error, after budget.Snapshot, afterErr error, capNano int64) {
	fmt.Fprintf(e.stdout, "teacher: asked %d, stored %d (%d with an unusable slot), no reading %d, already stored %d\n",
		res.Asked, res.Stored, res.Unusable, res.Unread, res.Skipped+already)
	if err := cmp.Or(beforeErr, afterErr); err != nil {
		fmt.Fprintf(e.stdout, "teacher: the budget ledger could not be read back (%v); gate-metered spend (committed delta, not settled) %s\n", err, usd(res.SpentNano))
	} else {
		fmt.Fprintf(e.stdout, "teacher: settled spend %s (ledger delta, provider-reported usage), plus %s of reservations kept unproven; budget remaining today %s of %s\n",
			usd(after.SpentNanoUSD-before.SpentNanoUSD), usd(after.RetainedNanoUSD-before.RetainedNanoUSD),
			usd(max(capNano-after.CommittedNanoUSD(), 0)), usd(capNano))
	}
	remaining := planned - res.Stored // an unread question stored nothing and is asked again
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
}

// teacherPrompt measures one question's prompt: Estimate is the typical input
// tokens, the system prompt and user message the backend would send counted
// with Manvi's documented BPE estimator (prompt.EstimateTokens), an estimate and
// not a tokenizer; Ceiling is the physical upper bound the spend gate reserves.
type teacherPrompt struct{ Estimate, Ceiling int }

// teacherMeasure builds the question for c exactly as a real run does and
// measures it.
func teacherMeasure(c eval.TeacherCandidate, summary string) teacherPrompt {
	var m teacherPrompt
	tr := jdtriage.Triager{TargetSummary: summary, D: deciderFunc(func(_ context.Context, q decide.Question) (decide.Answer, error) {
		sys, user := llmbackend.Messages(q)
		m = teacherPrompt{Estimate: prompt.EstimateTokens(sys + "\n" + user), Ceiling: teacherCeilingTokens(sys, user)}
		return decide.Answer{}, nil
	})}
	_, _ = tr.Triage(context.Background(), c.Posting)
	return m
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

func (e *env) teacherReport(cfg config.Config, model, first, source string, p budget.Prices, sel eval.TeacherSelection, already int, toks []teacherPrompt, capNano int64) {
	fmt.Fprintf(e.stdout, "teacher: %d postings read by %s (left out: %d with no text, %d copied readings); %d already stored for this model, fixture and prompt; %d to ask\n",
		len(sel.Candidates), first, sel.NoText, sel.Inherited, already, len(toks))
	if len(toks) == 0 {
		return
	}
	var sum, lo, hi, long int
	lo = math.MaxInt
	var typical, worst int64
	for _, m := range toks {
		n := m.Estimate
		sum += n
		lo, hi = min(lo, n), max(hi, n)
		if p.LongPromptAbove > 0 && n > p.LongPromptAbove {
			long++
		}
		if c, err := p.Charge(llm.Usage{InputTokens: max(n, 1), OutputTokens: teacherTypicalOut}); err == nil {
			typical += c
		}
		if w, err := p.Reserve(m.Ceiling, llmbackend.DefaultMaxTokens+llmbackend.ReasoningHeadroom); err == nil {
			worst += w
		}
	}
	fmt.Fprintf(e.stdout, "teacher: each question carries the triage prompt, your target summary and one posting framed as untrusted content, sent to %s %s\n", cfg.LLM.Provider, model)
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
}
