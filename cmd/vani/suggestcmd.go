package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/decide/fieldtriage"
	"github.com/bharathvbcr/Vani/internal/decide/fmbackend"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/draft"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/suggest"
	"github.com/bharathvbcr/decide"
	"github.com/bharathvbcr/decide/jev"
)

// maxSuggestInput caps the request read from stdin: 60 labels and a posting.
const maxSuggestInput = 256 << 10

// suggestInput is where `vani suggest` reads its request. Tests replace it.
var suggestInput io.Reader = os.Stdin

// suggestDecider builds the field-triage router. Tests replace it.
var suggestDecider = func(w *wired, p llm.Provider) (decide.Decider, error) { return w.fieldRouter(p) }

// suggestReply is the one JSON object `vani suggest` writes.
type suggestReply struct {
	Fields []suggest.Suggestion `json:"fields,omitempty"`
	Error  string               `json:"error,omitempty"`
	Code   string               `json:"code,omitempty"`
}

// cmdSuggest reads one page's new fields (JSON) on stdin and writes one reply
// (JSON) on stdout: {"fields":[...]} or {"error","code"}. The decision model
// sorts each field by its label; Claude (the configured Manvi provider) drafts
// only the fields read as essays, from the same daily ledger as `vani draft`.
func (e *env) cmdSuggest(args []string) error {
	fs := e.newFlags("suggest")
	if err := parse(fs, args); err != nil {
		return err
	}
	start := time.Now()
	out, code, err := e.suggestOnce()
	reply := suggestReply{Fields: out}
	if err != nil {
		reply = suggestReply{Error: err.Error(), Code: code}
		e.recordOutcome(suggest.FailureOutcome(suggest.OpSuggest, start, code))
	}
	return e.writeReply(reply, err)
}

func (e *env) suggestOnce() ([]suggest.Suggestion, string, error) {
	raw, err := io.ReadAll(io.LimitReader(suggestInput, maxSuggestInput+1))
	if err != nil {
		return nil, draft.CodeBadRequest, fmt.Errorf("suggest: read request: %w", err)
	}
	if len(raw) > maxSuggestInput {
		return nil, draft.CodeBadRequest, fmt.Errorf("suggest: request larger than %d bytes", maxSuggestInput)
	}
	var req suggest.Request
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, draft.CodeBadRequest, fmt.Errorf("suggest: request is not a suggest request: %w", err)
	}
	if err := req.Validate(); err != nil {
		return nil, draft.CodeBadRequest, err
	}

	cfg, err := e.loadConfig()
	if err != nil {
		return nil, codeConfig, err
	}
	if cfg.LLM.Provider == "" || cfg.LLM.Model == "" {
		return nil, codeConfig, errors.New("suggest: no llm provider is configured")
	}
	ctx, cancel := signalContext()
	defer cancel()

	w := &wired{cfg: cfg, stateDir: e.stateDir, warn: e.stderr}
	// One metered provider for triage and drafts: one budget ledger per process.
	p, err := draftProvider(ctx, w)
	if err != nil {
		return nil, codeConfig, err
	}
	var d pageDrafter
	dr, code, err := e.newDrafter(ctx, cfg, p)
	switch {
	case err == nil:
		d = dr
	case code == codeKB:
		// An unreadable knowledge base costs the drafts and the answers, not the
		// triage: every field is still sorted, and the essays, facts and choices come
		// back failed with code kb.
		fmt.Fprintf(e.stderr, "vani: suggest: %v; nothing will be drafted or answered\n", err)
		d = kbFailedDrafter{err: &draft.Error{Code: draft.CodeKB, Err: err}}
	default:
		return nil, code, err
	}
	router, err := suggestDecider(w, p)
	if err != nil {
		return nil, codeConfig, err
	}
	out, err := suggest.Engine{D: router, Drafter: d, Answerer: d, Record: e.recordOutcome}.Run(ctx, req)
	if err != nil {
		code := draft.CodeProvider
		if errors.Is(err, suggest.ErrBadRequest) {
			code = draft.CodeBadRequest
		}
		return nil, code, err
	}
	return out, "", nil
}

// pageDrafter is what a suggest page needs of the drafter: essay drafts and the
// batched fact and choice answers (draft.Drafter does both).
type pageDrafter interface {
	suggest.Drafter
	suggest.Answerer
}

// kbFailedDrafter stands in for the drafter when the knowledge base did not
// load: every draft and answer fails with the same code, so suggest can still
// triage.
type kbFailedDrafter struct{ err error }

func (k kbFailedDrafter) Draft(context.Context, draft.Request) (draft.Result, error) {
	return draft.Result{}, k.err
}

func (k kbFailedDrafter) Answer(context.Context, draft.AnswerRequest) (draft.AnswerResult, error) {
	return draft.AnswerResult{}, k.err
}

// fieldRouter is the decision chain for career.form_field: the on-device Apple
// model (labels only; free, and absent or refusing on a Mac without Apple
// Intelligence, so the next backend answers), the OpenRouter decision model
// when configured, then the LLM. Lappi is not trained on this
// task and the local model is only ever a shadow, so neither is asked.
func (w *wired) fieldRouter(p llm.Provider) (decide.Decider, error) {
	var backends []decide.Backend
	backends = append(backends, logErrors(fmbackend.New(fmbackend.Config{Descriptions: fieldtriage.Descriptions()}), w.warnf, nil))
	if w.cfg.OpenRouterConfigured() {
		spend, err := decide.NewDailyCap(filepath.Join(w.stateDir, "budget", openRouterBudgetFile), w.cfg.Decide.DailyUSD.OpenRouter)
		if err != nil {
			return nil, fmt.Errorf("openrouter budget: %w", err)
		}
		d, err := egress.New(egress.Config{AllowedHosts: w.cfg.AllowedHosts})
		if err != nil {
			return nil, err
		}
		jb, err := jev.New(jev.Config{
			Name:         w.cfg.Decide.OpenRouterName(),
			Model:        w.cfg.Decide.OpenRouterModel,
			KeyEnv:       w.cfg.Decide.OpenRouterKeyEnv,
			Getenv:       w.openRouterGetenv,
			Descriptions: fieldtriage.Descriptions(),
			Spend:        spend,
			SpendKey:     openRouterSpendKey,
		}, d)
		if err != nil {
			w.warnf("openrouter (%s) disabled: %v", w.cfg.Decide.OpenRouterModel, err)
		} else {
			backends = append(backends, logErrors(jb, w.warnf, nil))
		}
	}
	lb, err := llmbackend.New(p, llmbackend.Config{Model: w.cfg.LLM.EffectiveTriageModel()})
	if err != nil {
		return nil, fmt.Errorf("llm backend: %w", err)
	}
	backends = append(backends, logErrors(lb, w.warnf, nil))
	return decide.NewRouter(backends...), nil
}
