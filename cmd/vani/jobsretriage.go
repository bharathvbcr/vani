package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/credentials"
	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/manualtriage"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// `vani jobs retriage KEY` has the configured Claude triage model (llm.triage_model,
// Sonnet) read ONE posting by hand, the exact question triage asks, all four
// slots, and keeps the reading beside the ledger (internal/manualtriage). It is
// not `vani retriage`, which returns postings that were never read to live and
// asks no model.
//
// The reading outranks the primary's and the escalation's for the slots it names
// (jobinsight.RankContext.SlotFor, through RankContext.Manual) and only orders:
// Rule 7, it never drops a posting or brings a dropped one back. It is written
// without the ledger lock, because the daemon holds that all day; the Mac app
// runs this command, as it runs `vani draft`, with the key in the child's
// environment and VANI_NO_KEYCHAIN=1.
//
// The posting's text is untrusted: it enters the prompt only through
// llmbackend's framing (the untrusted-content markers) and nothing it says is
// followed. The target summary is the config's non-identifying one; no personal
// data is sent.

const (
	// retriageTypicalOut is a typical reply's tokens, thinking included, for the
	// typical-cost line (kbmatch's figure).
	retriageTypicalOut = kbMatchTypicalOut
)

// retriageAskTimeout bounds the whole provider call, building the provider
// included (llmbackend's own turn deadline is inside it). A variable only so
// tests need not wait.
var retriageAskTimeout = 90 * time.Second

// retriageProvider builds the metered llm provider. Tests replace it.
var retriageProvider = func(ctx context.Context, w *wired) (llm.Provider, error) { return w.provider(ctx) }

// Codes of an error reply, which the Mac app maps to words.
const (
	rtBadRequest = "bad_request"
	rtUnknownKey = "unknown_key"
	rtTextGone   = "text_gone"
	rtUntriaged  = "untriaged"
	rtNoLLM      = "no_llm"
	rtNoKey      = "no_key"
	rtCapReached = "cap_reached"
	rtProvider   = "provider"
	rtRefused    = "refused"
	rtUnsure     = "unsure"
	rtUnreadable = "unreadable"
	rtBusy       = "busy"
	rtCancelled  = "cancelled"
	rtStore      = "store"
	rtConfig     = "config"
	rtLedger     = "ledger"
)

// retriageError is a failure with the code the reply carries. cost is set when a
// paid call completed but its result was not used, so the money is not hidden.
type retriageError struct {
	code string
	msg  string
	cost *retriageCost
}

func (e *retriageError) Error() string { return e.msg }

func rtErr(code, format string, a ...any) *retriageError {
	return &retriageError{code: code, msg: fmt.Sprintf(format, a...)}
}

type retriageSlot struct {
	Value   string   `json:"value,omitempty"`
	Backend string   `json:"backend,omitempty"`
	P       *float64 `json:"p,omitempty"`
}

// retriageSide is the ranking's view of a posting at one moment: each slot as
// ranking read it, and the relevance and priority that gives.
type retriageSide struct {
	Slots     map[string]retriageSlot `json:"slots"`
	Relevance float64                 `json:"relevance"`
	Priority  float64                 `json:"priority"`
	Model     string                  `json:"model,omitempty"`
	At        *time.Time              `json:"at,omitempty"`
}

type retriageCost struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	USD          float64 `json:"usd"`
}

type retriageEstimate struct {
	InputTokens         int64   `json:"input_tokens"`
	TypicalOutputTokens int64   `json:"typical_output_tokens"`
	MaxOutputTokens     int64   `json:"max_output_tokens"`
	TypicalUSD          float64 `json:"typical_usd"`
	MaxUSD              float64 `json:"max_usd"`
	PriceSource         string  `json:"price_source"`
	DailyCapUSD         float64 `json:"daily_cap_usd"`
	// SpentUSD and RemainingUSD are absent when today's spend could not be read:
	// unknown is never shown as zero.
	SpentUSD     *float64 `json:"spent_usd,omitempty"`
	RemainingUSD *float64 `json:"remaining_usd,omitempty"`
	// CapReached says what is left of today's cap is less than a call reserves
	// (MaxUSD; for a batch, its dearest call's). Advice only: the metered
	// provider is the gate.
	CapReached bool `json:"cap_reached"`
	// Postings is how many postings a batch estimate sums (absent for one).
	Postings int `json:"postings,omitempty"`
	// FitAtMax is, for a batch, how many of its postings, taken in order, fit in
	// what is left of today's cap at their most; absent when the spend is unknown.
	FitAtMax *int `json:"fit_at_max,omitempty"`
}

type retriageReply struct {
	Key          string                `json:"key"`
	DryRun       bool                  `json:"dry_run,omitempty"`
	Model        string                `json:"model,omitempty"`
	Status       posting.Status        `json:"status,omitempty"`
	Before       *retriageSide         `json:"before,omitempty"`
	After        *retriageSide         `json:"after,omitempty"`
	Cost         *retriageCost         `json:"cost,omitempty"`
	Estimate     *retriageEstimate     `json:"estimate,omitempty"`
	ManualTriage *jobview.ManualTriage `json:"manual_triage,omitempty"`
}

type retriageClearReply struct {
	Key       string  `json:"key"`
	Cleared   bool    `json:"cleared"`
	HadManual bool    `json:"had_manual"`
	Relevance float64 `json:"relevance"`
	Priority  float64 `json:"priority"`
}

type retriageErrorReply struct {
	Error string        `json:"error"`
	Code  string        `json:"code"`
	Cost  *retriageCost `json:"cost,omitempty"`
}

// manualReadingSet is the store as the views read it, for Enrich (which shows a
// stale reading too) and for RankContext.Manual (which uses only current ones).
type manualReadingSet struct {
	m map[posting.Key]manualtriage.Reading
}

// manualReadings reads the store. nil sets and a warning when it cannot be read
// (a corrupt store is never applied and never taken for empty); nil and no
// warning when there are no readings.
func manualReadings(stateDir string) (*manualReadingSet, func(ledger.Record, string) (string, bool), string) {
	m, err := manualtriage.Read(manualtriage.Dir(stateDir))
	if err != nil {
		return nil, nil, "manual Sonnet readings not used: " + oneLine(err.Error(), 200)
	}
	if len(m) == 0 {
		return nil, nil, ""
	}
	s := &manualReadingSet{m: m}
	return s, s.value, ""
}

// manualLookup is manualReadings for a caller that wants only the lookup.
func manualLookup(stateDir string) (func(ledger.Record, string) (string, bool), string) {
	_, f, warn := manualReadings(stateDir)
	return f, warn
}

// state is r's reading and whether it still describes the posting.
func (s *manualReadingSet) state(r ledger.Record) (manualtriage.Reading, manualtriage.State, bool) {
	if s == nil {
		return manualtriage.Reading{}, "", false
	}
	rd, ok := s.m[r.Key]
	if !ok {
		return manualtriage.Reading{}, "", false
	}
	return rd, rd.StateOf(r.Posting, r.BodyEvicted), true
}

// value is RankContext.Manual: the slot's value of a current reading.
func (s *manualReadingSet) value(r ledger.Record, slot string) (string, bool) {
	rd, st, ok := s.state(r)
	if !ok || st != manualtriage.Current {
		return "", false
	}
	v, ok := rd.Slots[slot]
	return v, ok
}

// view is the reading as a job reports it; nil when r has none.
func (s *manualReadingSet) view(r ledger.Record) *jobview.ManualTriage {
	rd, st, ok := s.state(r)
	if !ok {
		return nil
	}
	mt := &jobview.ManualTriage{Slots: rd.Slots, Model: rd.Model, At: rd.At, Current: st == manualtriage.Current}
	if !mt.Current {
		mt.StaleReason = string(st)
	}
	return mt
}

// sideOf is how ranking reads r now: every slot through SlotFor, and the score.
func sideOf(in *insight, r ledger.Record) *retriageSide {
	side := &retriageSide{Slots: make(map[string]retriageSlot, len(jdtriage.Slots))}
	for _, spec := range jdtriage.Slots {
		s := in.rc.SlotFor(r, spec.Name)
		rs := retriageSlot{Value: s.Value, Backend: s.Backend}
		if s.HasP {
			p := s.P
			rs.P = &p
		}
		side.Slots[spec.Name] = rs
	}
	res := in.rc.ScoreOf(r)
	side.Relevance, side.Priority = res.Relevance, res.Score
	return side
}

// usageRecorder wraps the metered provider to keep the usage and stop reason of
// the one response it carried, so the cost reported is that call's own tokens at
// the configured prices (the budget ledger is shared with the daemon, which spends
// into it at the same time, so its difference would not be this call's).
type usageRecorder struct {
	llm.Provider
	mu   sync.Mutex
	use  llm.Usage
	stop llm.StopReason
}

func (u *usageRecorder) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	st, err := u.Provider.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	return &recordedStream{Stream: st, u: u}, nil
}

func (u *usageRecorder) snapshot() (llm.Usage, llm.StopReason) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.use, u.stop
}

type recordedStream struct {
	llm.Stream
	u *usageRecorder
}

func (s *recordedStream) Response() (llm.Response, error) {
	resp, err := s.Stream.Response()
	if err == nil {
		s.u.mu.Lock()
		s.u.use, s.u.stop = resp.Usage, resp.StopReason
		s.u.mu.Unlock()
	}
	return resp, err
}

func (e *env) jobsRetriage(args []string) error {
	fs := e.newFlags("jobs retriage")
	asJSON := fs.Bool("json", false, "one JSON object on stdout ({error,code} on failure)")
	dry := fs.Bool("dry-run", false, "say what asking would cost and ask nothing (no key is read)")
	clr := fs.Bool("clear", false, "remove the manual reading of this posting instead of making one")
	batch := fs.Bool("batch", false, fmt.Sprintf("read every KEY given (at most %d); with --json a run writes one JSON object per line as it goes", maxRetriageBatch))
	skipRead := fs.Bool("skip-read", false, "with --batch: leave out postings Sonnet already has a current reading of")
	parallel := fs.Int("parallel", defaultRetriageParallel, fmt.Sprintf("with --batch: postings asked at once (1..%d)", maxRetriageParallel))
	if err := parsePositional(fs, args); err != nil {
		return err
	}
	if *batch {
		if *clr {
			return usagef("jobs retriage: --batch and --clear are exclusive")
		}
		ctx, cancel := signalContext()
		defer cancel()
		return e.jobsRetriageBatch(ctx, fs.Args(), *dry, *skipRead, *parallel, *asJSON)
	}
	if *skipRead || *parallel != defaultRetriageParallel {
		return usagef("jobs retriage: --skip-read and --parallel need --batch")
	}
	if fs.NArg() != 1 {
		return usagef("jobs retriage: expected one posting key (this is the manual Sonnet reading; `vani retriage` is a different command)")
	}
	if *dry && *clr {
		return usagef("jobs retriage: --dry-run and --clear are exclusive")
	}
	ctx, cancel := signalContext()
	defer cancel()
	reply, err := e.manualRetriage(ctx, fs.Arg(0), *dry, *clr)
	if *asJSON {
		var v any = reply
		if err != nil {
			rep := retriageErrorReply{Error: err.Error(), Code: rtConfig}
			var re *retriageError
			if errors.As(err, &re) {
				rep.Code, rep.Cost = re.code, re.cost
			}
			v = rep
		}
		enc := json.NewEncoder(e.stdout)
		enc.SetEscapeHTML(false)
		if werr := enc.Encode(v); werr != nil {
			return werr
		}
		if err != nil {
			return errAlreadyReported
		}
		return nil
	}
	if err != nil {
		return err
	}
	e.printRetriage(reply)
	return nil
}

func (e *env) printRetriage(reply any) {
	switch v := reply.(type) {
	case retriageClearReply:
		fmt.Fprintf(e.stdout, "%s: manual reading %s; relevance %.1f, priority %.1f\n", v.Key, map[bool]string{true: "cleared", false: "none to clear"}[v.HadManual], v.Relevance, v.Priority)
	case retriageReply:
		if v.DryRun {
			est := v.Estimate
			fmt.Fprintf(e.stdout, "%s: would ask %s about this posting: about %d input tokens; typically about $%.4f, at most $%.4f (%s); daily cap $%.2f\n", v.Key, v.Model, est.InputTokens, est.TypicalUSD, est.MaxUSD, est.PriceSource, est.DailyCapUSD)
			if est.CapReached {
				fmt.Fprintln(e.stdout, "note: what is left of today's cap is less than a call reserves; asking would be refused")
			}
			return
		}
		fmt.Fprintf(e.stdout, "%s: %s read it (about $%.4f); relevance %.1f -> %.1f, priority %.1f -> %.1f\n", v.Key, v.Model, v.Cost.USD, v.Before.Relevance, v.After.Relevance, v.Before.Priority, v.After.Priority)
		names := make([]string, 0, len(v.After.Slots))
		for n := range v.After.Slots {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(e.stdout, "  %-12s %s -> %s\n", n, orDash(v.Before.Slots[n].Value), v.After.Slots[n].Value)
		}
		fmt.Fprintln(e.stdout, "This only changes the order: a manual reading never drops a posting or brings a dropped one back.")
	}
}

// manualRetriage is the command's work. Its reply is a retriageReply or a
// retriageClearReply; its error is a *retriageError.
func (e *env) manualRetriage(ctx context.Context, rawKey string, dry, clr bool) (any, error) {
	k, err := checkJobKey(rawKey)
	if err != nil {
		return nil, rtErr(rtBadRequest, "%v", err)
	}
	snap, m, err := e.jobsStateFor(rowScope(func(ledger.Snapshot) (posting.Key, bool) { return k, true }))
	if err != nil {
		return nil, rtErr(rtLedger, "%v", err)
	}
	r, ok := findRecord(snap, k)
	switch {
	case !ok:
		return nil, rtErr(rtUnknownKey, "jobs retriage: no posting %q in the ledger", k)
	case r.BodyEvicted || strings.TrimSpace(r.Posting.Description) == "":
		return nil, rtErr(rtTextGone, "jobs retriage: the ledger holds no text for %s (it was cleared after the role closed, or the board never gave any), so there is nothing to read", k)
	case r.Status == posting.Live:
		return nil, rtErr(rtUntriaged, "jobs retriage: no run has triaged %s yet; a manual reading re-reads a triage, and the next run will make the first one", k)
	}
	dir := manualtriage.Dir(e.stateDir)

	if clr {
		had, err := manualtriage.Clear(dir, k, now())
		if err != nil {
			return nil, storeErr(err, nil)
		}
		in, err := e.newRowInsight(snap, m, r)
		if err != nil {
			return nil, rtErr(rtLedger, "%v", err)
		}
		side := sideOf(in, r)
		return retriageClearReply{Key: string(k), Cleared: true, HadManual: had, Relevance: side.Relevance, Priority: side.Priority}, nil
	}

	ask, err := e.retriageSetup(ctx)
	if err != nil {
		return nil, err
	}
	a, err := ask.question(ctx, r)
	if err != nil {
		return nil, rtErr(rtConfig, "%v", err)
	}
	in, err := e.newRowInsight(snap, m, r)
	if err != nil {
		return nil, rtErr(rtLedger, "%v", err)
	}

	if dry {
		est := ask.estimate(ctx, []retriagePrompt{a})
		return retriageReply{Key: string(k), DryRun: true, Model: ask.model, Status: r.Status, Estimate: est, ManualTriage: in.manual.view(r)}, nil
	}

	// One paid reading at a time: a second click while a model is being asked
	// would pay twice for the same posting.
	unlock, err := manualtriage.TryAskLock(dir)
	if errors.Is(err, manualtriage.ErrBusy) {
		return nil, rtErr(rtBusy, "jobs retriage: another manual reading is being asked right now; try again when it finishes")
	}
	if err != nil {
		return nil, storeErr(err, nil)
	}
	defer unlock()
	// A store that cannot take the reading is found out before the model is paid,
	// not after.
	if _, err := manualtriage.Read(dir); err != nil {
		return nil, storeErr(err, nil)
	}

	pctx, cancel := context.WithTimeout(ctx, retriageAskTimeout)
	defer cancel()
	p, err := retriageProvider(pctx, ask.w)
	if err != nil {
		return nil, mapProviderErr(pctx, err, "building the provider")
	}
	before := sideOf(in, r)
	rd, cost, re := ask.read(pctx, p, dir, r, a)
	if re != nil {
		return nil, re
	}
	in2, err := e.newRowInsight(snap, m, r)
	if err != nil {
		return nil, &retriageError{code: rtLedger, msg: err.Error(), cost: cost}
	}
	after := sideOf(in2, r)
	after.Model, after.At = ask.model, &rd.At
	return retriageReply{Key: string(k), Model: ask.model, Status: r.Status, Before: before, After: after, Cost: cost}, nil
}

// retriageAsker is what every manual reading in one command shares: the config,
// the model, the target summary and the prices.
type retriageAsker struct {
	cfg         config.Config
	w           *wired
	model       string
	summary     string
	pin, pout   int64
	priceSource string
}

// retriagePrompt is the triage question for one posting and the two messages
// llmbackend frames it as.
type retriagePrompt struct {
	q            decide.Question
	system, user string
}

// retriageSetup reads the config a manual reading needs: an llm provider, the
// triage model, the target summary and that model's prices.
func (e *env) retriageSetup(ctx context.Context) (*retriageAsker, error) {
	cfg, err := e.loadConfig()
	if err != nil {
		return nil, rtErr(rtConfig, "%v", err)
	}
	if cfg.LLM.Provider == "" || cfg.LLM.Model == "" {
		return nil, rtErr(rtNoLLM, "jobs retriage: no llm provider is configured (llm.provider, llm.model), so there is no Claude model to ask")
	}
	model := cfg.LLM.EffectiveTriageModel()
	summary, err := jdtriage.NormalizeSummary(cfg.TargetSummary)
	if err != nil {
		return nil, rtErr(rtConfig, "%v", err)
	}
	pin, pout, source, err := kbmatchPrices(cfg, model)
	if err != nil {
		return nil, rtErr(rtConfig, "%v", err)
	}
	return &retriageAsker{cfg: cfg, w: &wired{cfg: cfg, stateDir: e.stateDir, warn: e.stderr}, model: model,
		summary: summary, pin: pin, pout: pout, priceSource: source}, nil
}

// question is triage's own question for r, built by its own Triager, so the
// manual reading is made from exactly what a run would send.
func (a *retriageAsker) question(ctx context.Context, r ledger.Record) (retriagePrompt, error) {
	var q decide.Question
	capture := askFunc(func(_ context.Context, got decide.Question) (decide.Answer, error) {
		q = got
		return decide.Answer{}, nil
	})
	if _, err := (jdtriage.Triager{D: capture, TargetSummary: a.summary}).Triage(ctx, r.Posting); err != nil {
		return retriagePrompt{}, err
	}
	system, user := llmbackend.Messages(q)
	return retriagePrompt{q: q, system: system, user: user}, nil
}

func (a *retriageAsker) usd(in, out int64) float64 { return float64(in*a.pin+out*a.pout) / nanoPerUSD }

// estimate is what asking about every prompt would cost, typically and at
// most, against what is left of today's cap. CapReached says not even the
// dearest single call fits in what is left; FitAtMax how many of them, taken
// in order, fit there at their most.
func (a *retriageAsker) estimate(ctx context.Context, ps []retriagePrompt) *retriageEstimate {
	maxOut := int64(llmbackend.DefaultMaxTokens + llmbackend.ReasoningHeadroom)
	est := &retriageEstimate{TypicalOutputTokens: retriageTypicalOut, MaxOutputTokens: maxOut,
		PriceSource: a.priceSource, DailyCapUSD: a.cfg.Decide.DailyUSD.LLM}
	maxes := make([]float64, len(ps))
	var dearest float64
	for i, p := range ps {
		inTok := int64((len(p.system) + len(p.user) + kbMatchBytesPerTok - 1) / kbMatchBytesPerTok)
		est.InputTokens += inTok
		est.TypicalUSD += a.usd(inTok, retriageTypicalOut)
		maxes[i] = a.usd(inTok, maxOut)
		est.MaxUSD += maxes[i]
		dearest = max(dearest, maxes[i])
	}
	if len(ps) > 1 {
		est.Postings = len(ps)
	}
	if spent, err := readLLMSpend(ctx, a.w.budgetLedgerPath()); err == nil {
		remaining := max(est.DailyCapUSD-spent, 0)
		est.SpentUSD, est.RemainingUSD = &spent, &remaining
		est.CapReached = len(ps) > 0 && remaining < dearest
		if len(ps) > 1 {
			fit, left := 0, remaining
			for _, m := range maxes {
				if m > left {
					break
				}
				left -= m
				fit++
			}
			est.FitAtMax = &fit
		}
	}
	return est
}

// read asks the model about r through p and keeps its reading. The cost is that
// call's own tokens at the configured prices (the budget ledger is shared with
// the daemon, which spends into it at the same time, so its difference would not
// be this call's); it is set on an error whenever a paid call completed, so the
// money is not hidden. ctx bounds the call.
func (a *retriageAsker) read(ctx context.Context, p llm.Provider, dir string, r ledger.Record, pr retriagePrompt) (manualtriage.Reading, *retriageCost, *retriageError) {
	rec := &usageRecorder{Provider: p}
	lb, err := llmbackend.New(rec, llmbackend.Config{Model: a.model, Name: jobinsight.BackendManual})
	if err != nil {
		return manualtriage.Reading{}, nil, rtErr(rtConfig, "%v", err)
	}
	ans, askErr := lb.Decide(ctx, pr.q)
	use, stop := rec.snapshot()
	var cost *retriageCost
	if use.InputTokens+use.OutputTokens > 0 {
		cost = &retriageCost{InputTokens: use.InputTokens, OutputTokens: use.OutputTokens,
			USD: a.usd(int64(use.InputTokens), int64(use.OutputTokens))}
	}
	if askErr != nil {
		re := mapProviderErr(ctx, askErr, "asking "+a.model)
		if re.code != rtCancelled {
			re.cost = cost
		}
		return manualtriage.Reading{}, nil, re
	}
	if ctx.Err() != nil {
		return manualtriage.Reading{}, nil, mapProviderErr(ctx, ctx.Err(), "asking "+a.model)
	}
	slots, re := readingSlots(ans, stop, a.model)
	if re != nil {
		re.cost = cost
		return manualtriage.Reading{}, nil, re
	}
	rd := manualtriage.Reading{
		Key: r.Key, Slots: slots, Model: a.model, At: now().UTC(),
		Text: manualtriage.TextSHA256(r.Posting), Prompt: manualtriage.PromptSHA256(pr.system, a.summary),
	}
	if err := manualtriage.Set(dir, rd); err != nil {
		return manualtriage.Reading{}, nil, storeErr(err, cost)
	}
	if cost == nil {
		cost = &retriageCost{}
	}
	return rd, cost, nil
}

// readingSlots turns the model's answer into the slots to keep, or says why it
// cannot be kept: all four triage slots must be answered with a listed option,
// or nothing is stored.
func readingSlots(ans decide.Answer, stop llm.StopReason, model string) (map[string]string, *retriageError) {
	slots := make(map[string]string, len(jdtriage.Slots))
	var refused, unsure, other bool
	for _, spec := range jdtriage.Slots {
		sr, ok := ans.Slots[spec.Name]
		switch {
		case ok && sr.Reading == decide.Answered:
			slots[spec.Name] = sr.Value
		case ok && sr.Reading == decide.Refused:
			refused = true
		case ok && sr.Reading == decide.Abstained:
			unsure = true
		default:
			other = true
		}
	}
	switch {
	case stop == llm.StopRefusal || refused:
		return nil, rtErr(rtRefused, "%s declined to read this posting, so nothing was kept", model)
	case unsure:
		return nil, rtErr(rtUnsure, "%s could not tell on at least one of the four readings, so nothing was kept (a partial reading would mix its view with the old one)", model)
	case other:
		return nil, rtErr(rtUnreadable, "%s's reply could not be read as the four readings, so nothing was kept", model)
	}
	if err := manualtriage.CheckSlots(slots); err != nil {
		return nil, rtErr(rtUnreadable, "%s's reply was not a listed option for every reading, so nothing was kept", model)
	}
	return slots, nil
}

// mapProviderErr names a failure of building or asking the provider. A cancelled
// context is the owner's Stop, not a fault; the deadline is a provider that did
// not answer in time.
func mapProviderErr(ctx context.Context, err error, doing string) *retriageError {
	var missing *credentials.ErrMissing
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		return rtErr(rtCancelled, "stopped; nothing was kept")
	case errors.As(err, &missing):
		return rtErr(rtNoKey, "no Anthropic key is available to this command (%s)", oneLine(err.Error(), 160))
	case errors.Is(err, budget.ErrExhausted):
		return rtErr(rtCapReached, "today's llm spending cap would be passed by this call, so it was not made (%s)", oneLine(err.Error(), 160))
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return rtErr(rtProvider, "the provider did not answer in time while %s", doing)
	}
	return rtErr(rtProvider, "the provider could not be reached or refused the request while %s: %s", doing, oneLine(err.Error(), 200))
}

func storeErr(err error, cost *retriageCost) *retriageError {
	code := rtStore
	if errors.Is(err, manualtriage.ErrBusy) {
		code = rtBusy
	}
	return &retriageError{code: code, msg: "the manual reading could not be saved or removed: " + oneLine(err.Error(), 200), cost: cost}
}
