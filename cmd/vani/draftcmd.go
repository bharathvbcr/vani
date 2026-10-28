package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/draft"
	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// maxDraftInput caps the request read from stdin. It carries a posting of
// draft.MaxPostingRunes in the worst UTF-8 case (4 bytes a rune), the same cap
// as `vani suggest`.
const maxDraftInput = 256 << 10

// draftInput is where `vani draft` reads its request. Tests replace it.
var draftInput io.Reader = os.Stdin

// draftProvider builds the metered provider for a draft. Tests replace it.
var draftProvider = func(ctx context.Context, w *wired) (llm.Provider, error) { return w.provider(ctx) }

// draftReply is the one JSON object `vani draft` writes.
type draftReply struct {
	Draft         string `json:"draft,omitempty"`
	Model         string `json:"model,omitempty"`
	EvidenceItems int    `json:"evidence_items,omitempty"`
	Error         string `json:"error,omitempty"`
	Code          string `json:"code,omitempty"`
	// Kind says what a draft.CodeUngrounded refusal found unbacked (draft.Kind*).
	Kind string `json:"kind,omitempty"`
}

// Codes `vani draft` adds to draft's own.
const (
	codeConfig = "config"
	codeKB     = "kb"
)

// cmdDraft reads one draft request (JSON) on stdin and writes one reply (JSON)
// on stdout: {"draft","model","evidence_items"} or {"error","code"}. It spends
// from the same daily LLM ledger as the pipeline, so decide.daily_usd.llm caps
// both together.
func (e *env) cmdDraft(args []string) error {
	fs := e.newFlags("draft")
	if err := parse(fs, args); err != nil {
		return err
	}
	res, code, err := e.draftOnce()
	reply := draftReply{Draft: res.Draft, Model: res.Model, EvidenceItems: res.EvidenceItems}
	if err != nil {
		reply = draftReply{Error: err.Error(), Code: code, Kind: draft.KindOf(err)}
	}
	enc := json.NewEncoder(e.stdout)
	enc.SetEscapeHTML(false)
	if werr := enc.Encode(reply); werr != nil {
		return werr
	}
	if err != nil {
		return errAlreadyReported
	}
	return nil
}

// errAlreadyReported exits 1 without a second message: the JSON reply carried
// it. `vani draft` and `vani suggest` share it.
var errAlreadyReported = errors.New("failed (reported on stdout as JSON)")

func (e *env) draftOnce() (draft.Result, string, error) {
	raw, err := io.ReadAll(io.LimitReader(draftInput, maxDraftInput+1))
	if err != nil {
		return draft.Result{}, draft.CodeBadRequest, fmt.Errorf("draft: read request: %w", err)
	}
	if len(raw) > maxDraftInput {
		return draft.Result{}, draft.CodeBadRequest, fmt.Errorf("draft: request larger than %d bytes", maxDraftInput)
	}
	var req draft.Request
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return draft.Result{}, draft.CodeBadRequest, fmt.Errorf("draft: request is not a draft request: %w", err)
	}
	if err := req.Validate(); err != nil {
		return draft.Result{}, draft.CodeBadRequest, err
	}

	cfg, err := e.loadConfig()
	if err != nil {
		return draft.Result{}, codeConfig, err
	}
	if cfg.LLM.Provider == "" || cfg.LLM.Model == "" {
		return draft.Result{}, codeConfig, errors.New("draft: no llm provider is configured")
	}
	ctx, cancel := signalContext()
	defer cancel()

	w := &wired{cfg: cfg, stateDir: e.stateDir, warn: e.stderr}
	p, err := draftProvider(ctx, w)
	if err != nil {
		return draft.Result{}, codeConfig, err
	}
	d, code, err := e.newDrafter(ctx, cfg, p)
	if err != nil {
		return draft.Result{}, code, err
	}
	res, err := d.Draft(ctx, req)
	if err != nil {
		code := draft.CodeProvider
		var de *draft.Error
		if errors.As(err, &de) {
			code = de.Code
		}
		return draft.Result{}, code, err
	}
	return res, "", nil
}

// newDrafter loads the knowledge-base evidence (when configured) and builds a
// drafter on p. On failure it returns the reply code to report.
func (e *env) newDrafter(ctx context.Context, cfg config.Config, p llm.Provider) (*draft.Drafter, string, error) {
	var evidence []draft.Evidence
	if cfg.KB.Bank != "" && cfg.KB.RKB != "" {
		b, claims, el, err := eligibleBank(ctx, cfg)
		if err != nil {
			return nil, codeKB, fmt.Errorf("draft: %w", err)
		}
		fmt.Fprintf(e.stderr, "vani: draft: %s\n", eligibleSummary(cfg, el))
		evidence = draft.FromResolved(kbsync.ResolveBank(b, claims), el)
	} else {
		fmt.Fprintln(e.stderr, "vani: draft: kb.bank and kb.rkb are not configured; drafting without knowledge-base evidence")
	}
	d, err := draft.New(p, draft.Config{Model: cfg.LLM.Model, TargetSummary: cfg.TargetSummary, MotivationNotes: cfg.MotivationNotes}, evidence)
	if err != nil {
		return nil, codeConfig, err
	}
	return d, "", nil
}
