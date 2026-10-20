package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
	"github.com/bharathvbcr/decide"
	"github.com/bharathvbcr/decide/jev"
)

// `vani kbmatch` asks a model, for the postings that lead the Jobs list, whether
// the work each describes is the work in the owner's knowledge base
// (internal/decide/kbmatch). The answers are kept beside the ledger and only
// order and explain: the ranker reads them as one more part (Rule 7). It runs
// only when asked, because it sends the posting, the target summary and a
// bounded view of the knowledge base to the model's provider and spends from a
// daily cap; `--dry-run` says what it would send and cost.
//
// Three models can take part, each doing what it is good at:
//   - the embedding model (EmbeddingGemma, on this machine) picks, for each
//     posting, the knowledge-base lines closest to it, so the question carries
//     the evidence that bears on that posting and is shorter;
//   - the decision model (OpenRouter, kbmatch.backend "openrouter") or the llm (kbmatch.backend
//     "llm", on kbmatch.model, else llm.triage_model, else llm.model) reads the
//     duties and answers.

const (
	defaultKBMatchTop  = 40
	maxKBMatchTop      = 500
	kbMatchStopAfter   = 3  // consecutive unreachable or unusable answers before the run stops
	kbMatchSaveEvery   = 10 // answers between saves
	kbMatchMinWords    = 30 // a posting with fewer words is too thin to judge
	kbMatchBytesPerTok = 4  // the rough byte-per-token ratio of English text, for the estimate
	kbMatchOverhead    = 400
	kbMatchTypicalOut  = 150 // a typical reply, thinking included, for the typical-cost line
)

// The seams tests replace.
var (
	// kbmatchProvider builds the metered llm provider.
	kbmatchProvider = func(ctx context.Context, w *wired) (llm.Provider, error) { return w.provider(ctx) }
	// kbmatchEndpoint points the decision-model client at a test server; empty is the client's own.
	kbmatchEndpoint string
	// kbmatchDoer is the egress client the OpenRouter backend uses.
	kbmatchDoer = func(cfg config.Config) (egress.Doer, error) {
		return egress.New(egress.Config{AllowedHosts: cfg.AllowedHosts})
	}
	// kbmatchDecider builds the decision chain for backend (config.KBMatchLLM or
	// config.KBMatchOpenRouter).
	kbmatchDecider = func(ctx context.Context, w *wired, backend string) (decide.Decider, error) {
		switch backend {
		case config.KBMatchLLM:
			p, err := kbmatchProvider(ctx, w)
			if err != nil {
				return nil, err
			}
			lb, err := llmbackend.New(p, llmbackend.Config{Model: w.cfg.KBMatchModel()})
			if err != nil {
				return nil, fmt.Errorf("llm backend: %w", err)
			}
			return decide.NewRouter(logErrors(lb, w.warnf, nil)), nil
		case config.KBMatchOpenRouter:
			spend, err := newBudgetCap(w.stateDir, openRouterBudgetFile, w.cfg.Decide.DailyUSD.OpenRouter)
			if err != nil {
				return nil, fmt.Errorf("openrouter budget: %w", err)
			}
			d, err := kbmatchDoer(w.cfg)
			if err != nil {
				return nil, err
			}
			jb, err := jev.New(jev.Config{
				Name:         w.cfg.Decide.OpenRouterName(),
				Endpoint:     kbmatchEndpoint,
				Model:        w.cfg.Decide.OpenRouterModel,
				KeyEnv:       w.cfg.Decide.OpenRouterKeyEnv,
				Descriptions: kbmatch.Descriptions(),
				Instructions: kbmatch.Instructions(),
				Spend:        spend,
				SpendKey:     openRouterSpendKey,
			}, d)
			if err != nil {
				return nil, fmt.Errorf("decision model: %w", err)
			}
			return decide.NewRouter(logErrors(jb, w.warnf, nil)), nil
		}
		return nil, fmt.Errorf("kbmatch: unknown backend %q", backend)
	}
	// kbmatchFocus opens the embedding model and returns the function that picks a
	// posting's closest knowledge-base lines, and what closes it. An error means
	// the run goes on with the full digest.
	kbmatchFocus = func(ctx context.Context, cfg config.Config, stateDir string, prof semantic.Profile) (func(context.Context, posting.Posting) ([]kbmatch.Focus, error), func(), error) {
		if !cfg.SemanticOn() {
			return nil, nil, errors.New("semantic matching is off (no semantic.snapshot or kb.bank)")
		}
		emb, closeEmb, err := openEmbedder(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		m, _, err := openScoreCache(emb, prof, semanticDir(stateDir))
		if err != nil {
			if closeEmb != nil {
				_ = closeEmb()
			}
			return nil, nil, err
		}
		focus := func(ctx context.Context, p posting.Posting) ([]kbmatch.Focus, error) {
			ns, err := m.Nearest(ctx, semantic.Doc{Key: string(p.Key), Title: p.Title, Description: p.Description}, kbmatch.MaxFocus)
			if err != nil {
				return nil, err
			}
			out := make([]kbmatch.Focus, len(ns))
			for i, n := range ns {
				out[i] = kbmatch.Focus{Label: n.Label, Text: n.Text}
			}
			return out, nil
		}
		return focus, func() {
			if closeEmb != nil {
				_ = closeEmb()
			}
		}, nil
	}
)

func (e *env) kbmatchPath() string { return filepath.Join(e.stateDir, kbmatch.FileName) }

func (e *env) cmdKBMatch(args []string) error {
	if len(args) == 0 {
		return usagef("kbmatch: expected subcommand \"run\" or \"status\"")
	}
	switch args[0] {
	case "run":
		return e.kbmatchRun(args[1:])
	case "status":
		return e.kbmatchStatus(args[1:])
	}
	return usagef("kbmatch: unknown subcommand %q", args[0])
}

// kbmatchSetup is what a run and a status both need: the config, the knowledge
// base's profile and digest, and the normalized target summary.
func (e *env) kbmatchSetup() (config.Config, semantic.Profile, kbmatch.Digest, string, error) {
	cfg, err := e.loadConfig()
	if err != nil {
		return cfg, semantic.Profile{}, kbmatch.Digest{}, "", err
	}
	if strings.TrimSpace(cfg.TargetSummary) == "" {
		return cfg, semantic.Profile{}, kbmatch.Digest{}, "", errors.New("kbmatch: the config has no target_summary to judge the postings against")
	}
	summary, err := jdtriage.NormalizeSummary(cfg.TargetSummary)
	if err != nil {
		return cfg, semantic.Profile{}, kbmatch.Digest{}, "", err
	}
	prof, err := semanticProfile(cfg)
	if err != nil {
		return cfg, semantic.Profile{}, kbmatch.Digest{}, "", fmt.Errorf("kbmatch: the knowledge base: %w", err)
	}
	d, err := kbmatch.DigestOf(prof)
	if err != nil {
		return cfg, semantic.Profile{}, kbmatch.Digest{}, "", err
	}
	return cfg, prof, d, summary, nil
}

// kbmatchCandidate is a posting to ask about and where it stands.
type kbmatchCandidate struct {
	rec      ledger.Record
	priority float64
	text     string // kbmatch.TextSHA256
}

// kbmatchCandidates are the browsable postings the model has not read in their
// current form, best first by priority, at most top. A mismatch reading, a
// posting too thin to judge and one already answered under the same text and
// digest are left out; the counts say how many were left out why.
func kbmatchCandidates(snap ledger.Snapshot, m map[posting.Key]marks.Entry, in *insight, st *kbmatch.Store, d kbmatch.Digest, summary string, top int) (out []kbmatchCandidate, mismatch, thin, current int) {
	for _, r := range snap.Records {
		if !jobview.InView("all", r, m[r.Key].Mark) {
			continue
		}
		if strings.HasPrefix(jobinsight.SlotOf(r.Annotations, "role_fit").Value, "mismatch_") {
			mismatch++
			continue
		}
		if len(strings.Fields(r.Posting.Description)) < kbMatchMinWords {
			thin++
			continue
		}
		text := kbmatch.TextSHA256(r.Posting, summary, d)
		if _, ok := st.Lookup(string(r.Key), d.SHA256, text); ok {
			current++
			continue
		}
		out = append(out, kbmatchCandidate{rec: r, priority: jobinsight.Score(jobinsight.InputsFor(r, in.rc)).Score, text: text})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].priority != out[j].priority {
			return out[i].priority > out[j].priority
		}
		return out[i].rec.Key < out[j].rec.Key
	})
	if len(out) > top {
		out = out[:top]
	}
	return out, mismatch, thin, current
}

// kbmatchPrices are the per-token prices (nano-USD) the llm backend would be
// metered at for model: the provider's own table when it publishes one for the
// model, else the configured prices (which meter the configured model, and any
// model the table does not know). source says which.
func kbmatchPrices(cfg config.Config, model string) (in, out int64, source string, err error) {
	if cfg.LLM.Provider == "anthropic" && model != cfg.LLM.Model {
		if p, ok := anthropic.PriceOf(model); ok && p.Rates.Input > 0 && p.Rates.Output > 0 {
			return p.Rates.Input, p.Rates.Output, "Anthropic's published rates for " + model, nil
		}
	}
	in, out, err = cfg.LLM.Prices()
	return in, out, "the configured llm prices", err
}

func (e *env) kbmatchRun(args []string) error {
	fs := e.newFlags("kbmatch run")
	top := fs.Int("top", defaultKBMatchTop, fmt.Sprintf("ask about at most this many postings, best first by priority (1-%d)", maxKBMatchTop))
	dry := fs.Bool("dry-run", false, "say what would be sent and the most it could cost, and ask nothing")
	backendFlag := fs.String("backend", "", "llm or openrouter (default kbmatch.backend, else llm)")
	modelFlag := fs.String("model", "", "the llm model that answers (default kbmatch.model, else llm.triage_model, else llm.model)")
	noRetrieve := fs.Bool("no-retrieve", false, "do not have the embedding model pick the closest knowledge-base lines; send the full digest")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *top < 1 || *top > maxKBMatchTop {
		return usagef("kbmatch run: --top must be 1-%d", maxKBMatchTop)
	}
	cfg, prof, digest, summary, err := e.kbmatchSetup()
	if err != nil {
		return err
	}
	if *backendFlag != "" {
		*backendFlag = config.NormalizeKBMatchBackend(*backendFlag) // "jev" is the pre-2026-10-20 spelling
		if *backendFlag != config.KBMatchLLM && *backendFlag != config.KBMatchOpenRouter {
			return usagef("kbmatch run: --backend must be %q or %q", config.KBMatchLLM, config.KBMatchOpenRouter)
		}
		cfg.KBMatch.Backend = *backendFlag
	}
	if *modelFlag != "" {
		cfg.KBMatch.Model = *modelFlag
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("kbmatch: %w", err)
	}
	backend, model := cfg.KBMatch.EffectiveBackend(), ""
	switch backend {
	case config.KBMatchLLM:
		if cfg.LLM.Provider == "" || cfg.LLM.Model == "" {
			return errors.New("kbmatch: no llm provider is configured (or use --backend openrouter)")
		}
		model = cfg.KBMatchModel()
	case config.KBMatchOpenRouter:
		model = cfg.Decide.OpenRouterModel
	}

	snap, m, err := e.jobsState()
	if err != nil {
		return err
	}
	store, err := kbmatch.OpenStore(e.kbmatchPath())
	if errors.Is(err, kbmatch.ErrCorrupt) {
		// Nothing in it is irreplaceable: the model can be asked again. A dry run
		// only reads, so it starts from an empty store in memory.
		fmt.Fprintf(e.stderr, "vani: kbmatch store unreadable (%v); starting it again\n", err)
		if !*dry {
			if rerr := os.Remove(e.kbmatchPath()); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
				return rerr
			}
		}
		store, err = kbmatch.NewStore(e.kbmatchPath()), nil
	}
	if err != nil {
		return err
	}
	in, err := e.newInsight(jobinsight.DefaultScoreHalfLifeDays, snap, m)
	if err != nil {
		return err
	}
	cands, mismatch, thin, current := kbmatchCandidates(snap, m, in, store, digest, summary, *top)

	var inBytes int
	for _, c := range cands {
		q, _ := kbmatch.Question(c.rec.Posting, summary, digest, nil)
		inBytes += len(q.Context) + len(q.Prompt) + kbMatchOverhead
	}
	inTok := int64(inBytes / kbMatchBytesPerTok)
	retrieve := !*noRetrieve && cfg.SemanticOn()
	fmt.Fprintf(e.stdout, "kbmatch: %d postings to ask about (left out: %d mismatch readings, %d too thin, %d already answered)\n", len(cands), mismatch, thin, current)
	where := cfg.LLM.Provider + " model " + model
	if backend == config.KBMatchOpenRouter {
		where = "openrouter.ai decision model " + model
	}
	view := fmt.Sprintf("a %d-byte digest of your knowledge base", len(digest.Text))
	if retrieve {
		view = fmt.Sprintf("a %d-byte compact digest of your knowledge base plus up to %d lines of it that the embedding model finds closest to that posting", len(digest.Compact), kbmatch.MaxFocus)
	}
	fmt.Fprintf(e.stdout, "kbmatch: each question carries the posting, your target summary and %s (titles, stack terms and bullets, with employers, dates, places and contact details already removed), sent to %s\n", view, where)
	switch backend {
	case config.KBMatchOpenRouter:
		fmt.Fprintf(e.stdout, "kbmatch: about %d input tokens at most (focused questions are shorter); charged to the openrouter cap ($%.2f a day)\n", inTok, cfg.Decide.DailyUSD.OpenRouter)
	default:
		pin, pout, source, perr := kbmatchPrices(cfg, model)
		if perr == nil {
			capUSD := float64(inTok*pin+int64(len(cands))*int64(llmbackend.DefaultMaxTokens+llmbackend.ReasoningHeadroom)*pout) / 1e9
			typical := float64(inTok*pin+int64(len(cands))*kbMatchTypicalOut*pout) / 1e9
			fmt.Fprintf(e.stdout, "kbmatch: about %d input tokens at most (focused questions are shorter); %s: typically about $%.3f, at most $%.3f counting every reply at its full budget (daily cap $%.2f)\n", inTok, source, typical, capUSD, cfg.Decide.DailyUSD.LLM)
		}
	}
	if *dry || len(cands) == 0 {
		return nil
	}

	unlock, err := kbmatch.Lock(e.kbmatchPath())
	if err != nil {
		return err
	}
	defer unlock()
	ctx, cancel := signalContext()
	defer cancel()
	w := &wired{cfg: cfg, stateDir: e.stateDir, warn: e.stderr}
	d, err := kbmatchDecider(ctx, w, backend)
	if err != nil {
		return err
	}
	j := kbmatch.Judge{D: d, Target: summary, Digest: digest}
	if retrieve {
		focus, closeFocus, ferr := kbmatchFocus(ctx, cfg, e.stateDir, prof)
		if ferr != nil {
			fmt.Fprintf(e.stderr, "vani: the embedding model could not pick evidence (%s); sending the full digest\n", oneLine(ferr.Error(), 200))
		} else {
			defer closeFocus()
			j.Focus = focus
		}
	}
	return e.kbmatchAsk(ctx, j, store, cands, digest, backend, model)
}

// kbmatchAsk asks about each candidate in turn, keeps what was answered and
// saves as it goes. It stops when the context ends or after kbMatchStopAfter
// answers in a row went unread because the backend was unreachable or gave
// nothing usable (a spent budget, a revoked key): the rest would fail the same
// way. A model that abstained on one thin posting is not such a failure.
func (e *env) kbmatchAsk(ctx context.Context, j kbmatch.Judge, st *kbmatch.Store, cands []kbmatchCandidate, d kbmatch.Digest, backend, model string) error {
	counts := map[string]int{}
	var asked, unread, streak, sinceSave, focused int
	var stopped string
	save := func() error {
		if sinceSave == 0 {
			return nil
		}
		sinceSave = 0
		return st.Save()
	}
	for _, c := range cands {
		if ctx.Err() != nil {
			stopped = "interrupted"
			break
		}
		asked++
		v, err := j.Ask(ctx, c.rec.Posting)
		if err != nil {
			unread++
			if rd, ok := kbmatch.ReadingOf(err); ok && rd == decide.Abstained {
				streak = 0
				continue
			}
			if streak++; streak >= kbMatchStopAfter {
				stopped = fmt.Sprintf("the last %d questions got no usable answer (see the warnings above)", streak)
				break
			}
			continue
		}
		streak = 0
		mode := kbmatch.ModeFull
		if v.Focused {
			mode = kbmatch.ModeFocused
			focused++
		}
		if err := st.Put(string(c.rec.Key), kbmatch.Entry{Value: v.Value, Model: model, KB: d.SHA256, Text: c.text, At: now(), P: v.P, Backend: backend, Mode: mode}); err != nil {
			return err
		}
		counts[v.Value]++
		if sinceSave++; sinceSave >= kbMatchSaveEvery {
			if err := save(); err != nil {
				return err
			}
		}
	}
	if err := save(); err != nil {
		return err
	}
	var parts []string
	for _, o := range jobinsight.KBMatchOrder {
		parts = append(parts, fmt.Sprintf("%s %d", o, counts[o]))
	}
	fmt.Fprintf(e.stdout, "kbmatch: asked %d, answered %d (%s), not read %d; %d of the questions carried embedding-picked evidence\n", asked, asked-unread, strings.Join(parts, ", "), unread, focused)
	if stopped != "" {
		fmt.Fprintf(e.stdout, "kbmatch: stopped: %s\n", stopped)
		return errors.New("kbmatch: " + stopped)
	}
	return nil
}

func (e *env) kbmatchStatus(args []string) error {
	if err := parse(e.newFlags("kbmatch status"), args); err != nil {
		return err
	}
	_, _, digest, summary, err := e.kbmatchSetup()
	if err != nil {
		return err
	}
	st, err := kbmatch.OpenStore(e.kbmatchPath())
	if err != nil {
		return err
	}
	snap, m, err := e.jobsState()
	if err != nil {
		return err
	}
	var current int
	byValue := map[string]int{}
	for _, r := range snap.Records {
		if !jobview.InView("all", r, m[r.Key].Mark) {
			continue
		}
		if en, ok := st.Lookup(string(r.Key), digest.SHA256, kbmatch.TextSHA256(r.Posting, summary, digest)); ok {
			current++
			byValue[en.Value]++
		}
	}
	stale := st.Len() - current
	var parts []string
	for _, o := range jobinsight.KBMatchOrder {
		parts = append(parts, fmt.Sprintf("%s %d", o, byValue[o]))
	}
	fmt.Fprintf(e.stdout, "kbmatch: %d answers kept, %d current (%s), %d out of date or for postings no longer listed\n", st.Len(), current, strings.Join(parts, ", "), stale)
	fmt.Fprintf(e.stdout, "knowledge-base digest %s (%d bytes)\n", digest.SHA256[:12], len(digest.Text))
	return nil
}

// kbmatchLookup is jobinsight.RankContext.KBMatch over the store; nil when no
// run has made one. The warning says why answers exist but are not used.
func kbmatchLookup(cfg config.Config, stateDir string) (func(ledger.Record) (jobinsight.Slot, bool), string) {
	path := filepath.Join(stateDir, kbmatch.FileName)
	st, err := kbmatch.OpenStore(path)
	if err != nil {
		return nil, "language-model reads of your work not used: " + oneLine(err.Error(), 200)
	}
	if st.Len() == 0 {
		return nil, ""
	}
	summary, err := jdtriage.NormalizeSummary(cfg.TargetSummary)
	if err != nil {
		return nil, "language-model reads of your work not used: " + oneLine(err.Error(), 200)
	}
	prof, err := semanticProfile(cfg)
	if err != nil {
		return nil, "language-model reads of your work not used: " + oneLine(err.Error(), 200)
	}
	digest, err := kbmatch.DigestOf(prof)
	if err != nil {
		return nil, "language-model reads of your work not used: " + oneLine(err.Error(), 200)
	}
	return func(r ledger.Record) (jobinsight.Slot, bool) {
		en, ok := st.Lookup(string(r.Key), digest.SHA256, kbmatch.TextSHA256(r.Posting, summary, digest))
		if !ok {
			return jobinsight.Slot{}, false
		}
		return jobinsight.Slot{Value: en.Value, Answered: true, P: en.P, HasP: en.P > 0}, true
	}, ""
}
