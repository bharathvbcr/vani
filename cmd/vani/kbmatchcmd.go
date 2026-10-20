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
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// `vani kbmatch` asks the configured language model, for the postings that lead
// the Jobs list, whether the work each describes is the work in the owner's
// knowledge base (internal/decide/kbmatch). The answers are kept beside the
// ledger and only order and explain: the ranker reads them as one more part
// (Rule 7). It spends from decide.daily_usd.llm and sends the posting, the target
// summary and a bounded digest of the knowledge base to the configured provider,
// so it runs only when asked and `--dry-run` says what it would send and cost.

const (
	defaultKBMatchTop  = 40
	maxKBMatchTop      = 500
	kbMatchStopAfter   = 3  // consecutive unreachable or unusable answers before the run stops
	kbMatchSaveEvery   = 10 // answers between saves
	kbMatchMinWords    = 30 // a posting with fewer words is too thin to judge
	kbMatchBytesPerTok = 4  // the rough byte-per-token ratio of English text, for the estimate
	kbMatchOverhead    = 400
)

// kbmatchProvider builds the metered provider and kbmatchDecider the decision
// chain over it. Tests replace both.
var (
	kbmatchProvider = func(ctx context.Context, w *wired) (llm.Provider, error) { return w.provider(ctx) }
	kbmatchDecider  = func(w *wired, p llm.Provider) (decide.Decider, error) {
		lb, err := llmbackend.New(p, llmbackend.Config{Model: w.cfg.LLM.EffectiveTriageModel()})
		if err != nil {
			return nil, fmt.Errorf("llm backend: %w", err)
		}
		return decide.NewRouter(logErrors(lb, w.warnf, nil)), nil
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

// kbmatchSetup is what a run and a status both need: the config, the digest of
// the knowledge base and the normalized target summary.
func (e *env) kbmatchSetup() (config.Config, kbmatch.Digest, string, error) {
	cfg, err := e.loadConfig()
	if err != nil {
		return cfg, kbmatch.Digest{}, "", err
	}
	if strings.TrimSpace(cfg.TargetSummary) == "" {
		return cfg, kbmatch.Digest{}, "", errors.New("kbmatch: the config has no target_summary to judge the postings against")
	}
	summary, err := jdtriage.NormalizeSummary(cfg.TargetSummary)
	if err != nil {
		return cfg, kbmatch.Digest{}, "", err
	}
	prof, err := semanticProfile(cfg)
	if err != nil {
		return cfg, kbmatch.Digest{}, "", fmt.Errorf("kbmatch: the knowledge base: %w", err)
	}
	d, err := kbmatch.DigestOf(prof)
	if err != nil {
		return cfg, kbmatch.Digest{}, "", err
	}
	return cfg, d, summary, nil
}

// kbmatchCandidate is a posting to ask about and where it stands.
type kbmatchCandidate struct {
	rec      ledger.Record
	priority float64
	text     string // kbmatch.TextSHA256
}

// kbmatchCandidates are the browsable postings the model has not read in their
// current form, best first by priority, at most top. A confident-or-not
// mismatch reading, a posting too thin to judge and one already answered under
// the same text and digest are left out. Counts say how many were left out why.
func kbmatchCandidates(snap ledger.Snapshot, m map[posting.Key]marks.Entry, in *insight, st *kbmatch.Store, d kbmatch.Digest, summary string, top int) (out []kbmatchCandidate, mismatch, thin, current int) {
	for _, r := range snap.Records {
		if !inView("all", r, m[r.Key].Mark) {
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

func (e *env) kbmatchRun(args []string) error {
	fs := e.newFlags("kbmatch run")
	top := fs.Int("top", defaultKBMatchTop, fmt.Sprintf("ask about at most this many postings, best first by priority (1-%d)", maxKBMatchTop))
	dry := fs.Bool("dry-run", false, "say what would be sent and the most it could cost, and ask nothing")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *top < 1 || *top > maxKBMatchTop {
		return usagef("kbmatch run: --top must be 1-%d", maxKBMatchTop)
	}
	cfg, digest, summary, err := e.kbmatchSetup()
	if err != nil {
		return err
	}
	if cfg.LLM.Provider == "" || cfg.LLM.Model == "" {
		return errors.New("kbmatch: no llm provider is configured")
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

	model := cfg.LLM.EffectiveTriageModel()
	var inBytes int
	for _, c := range cands {
		q, _ := kbmatch.Question(c.rec.Posting, summary, digest)
		inBytes += len(q.Context) + len(q.Prompt) + kbMatchOverhead
	}
	inTok := int64(inBytes / kbMatchBytesPerTok)
	outTok := int64(len(cands) * (llmbackend.DefaultMaxTokens + llmbackend.ReasoningHeadroom))
	inPrice, outPrice, perr := cfg.LLM.Prices()
	fmt.Fprintf(e.stdout, "kbmatch: %d postings to ask about (left out: %d mismatch readings, %d too thin, %d already answered)\n", len(cands), mismatch, thin, current)
	fmt.Fprintf(e.stdout, "kbmatch: each question carries the posting, your target summary and a %d-byte digest of your knowledge base (titles, stack terms and a few bullets, with employers, dates, places and contact details already removed), sent to %s model %s\n", len(digest.Text), cfg.LLM.Provider, model)
	if perr == nil {
		est := float64(inTok*inPrice+outTok*outPrice) / 1e9
		fmt.Fprintf(e.stdout, "kbmatch: about %d input tokens; at most $%.2f counting every reply at its budget (daily cap $%.2f)\n", inTok, est, cfg.Decide.DailyUSD.LLM)
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
	p, err := kbmatchProvider(ctx, w)
	if err != nil {
		return err
	}
	d, err := kbmatchDecider(w, p)
	if err != nil {
		return err
	}
	return e.kbmatchAsk(ctx, kbmatch.Judge{D: d, Target: summary, Digest: digest}, store, cands, digest, model)
}

// kbmatchAsk asks about each candidate in turn, keeps what was answered and
// saves as it goes. It stops when the context ends or after kbMatchStopAfter
// answers in a row went unread because the backend was unreachable or gave
// nothing usable (a spent budget, a revoked key): the rest would fail the same
// way. A model that abstained on one thin posting is not such a failure.
func (e *env) kbmatchAsk(ctx context.Context, j kbmatch.Judge, st *kbmatch.Store, cands []kbmatchCandidate, d kbmatch.Digest, model string) error {
	counts := map[string]int{}
	var asked, unread, streak, sinceSave int
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
		if err := st.Put(string(c.rec.Key), kbmatch.Entry{Value: v.Value, Model: model, KB: d.SHA256, Text: c.text, At: now(), P: v.P}); err != nil {
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
	fmt.Fprintf(e.stdout, "kbmatch: asked %d, answered %d (%s), not read %d\n", asked, asked-unread, strings.Join(parts, ", "), unread)
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
	_, digest, summary, err := e.kbmatchSetup()
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
		if !inView("all", r, m[r.Key].Mark) {
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
