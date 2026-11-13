package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/cover"
	"github.com/bharathvbcr/Vani/internal/draft"
)

// coverModelTimeout overrides the drafter timeout for `vani cover` and
// `vani jobs cover`. Zero is the draft package default. Tests set it.
var coverModelTimeout time.Duration

// coverReply is the JSON `vani cover` writes. The PDF is base64. A letterhead
// is never a field: it exists only inside the PDF bytes.
type coverReply struct {
	Body          string `json:"body,omitempty"`
	Model         string `json:"model,omitempty"`
	EvidenceItems int    `json:"evidence_items,omitempty"`
	CompanyCheck  string `json:"company_check,omitempty"`
	Note          string `json:"note,omitempty"`
	PDF           string `json:"pdf,omitempty"`
	Bytes         int    `json:"bytes,omitempty"`
	Error         string `json:"error,omitempty"`
	Code          string `json:"code,omitempty"`
}

// coverWiring is the one config owner for `vani cover` and `vani jobs cover`,
// the way resumeConfig is shared by the resume commands.
type coverWiring struct {
	Drafter       *draft.Drafter
	Evidence      int
	BaseSelection string
	Bank          string
}

func (e *env) coverConfig(ctx context.Context) (coverWiring, string, error) {
	cfg, err := e.loadPromptConfig(false)
	if err != nil {
		return coverWiring{}, codeConfig, err
	}
	if cfg.LLM.Provider == "" || cfg.LLM.Model == "" {
		return coverWiring{}, codeConfig, errors.New("cover: no llm provider is configured")
	}
	w := &wired{cfg: cfg, stateDir: e.stateDir, warn: e.stderr}
	p, err := draftProvider(ctx, w)
	if err != nil {
		return coverWiring{}, codeConfig, err
	}
	d, code, err := e.drafterWith(ctx, cfg, p, coverModelTimeout)
	if err != nil {
		return coverWiring{}, code, err
	}
	if d.EvidenceCount() == 0 {
		return coverWiring{}, "no_evidence", errors.New("cover: no eligible bullets, so nothing was drafted")
	}
	return coverWiring{Drafter: d, Evidence: d.EvidenceCount(), BaseSelection: cfg.KB.BaseSelection, Bank: cfg.KB.Bank}, "", nil
}

// cmdCover reads the same JSON job request as `vani draft` and writes one
// cover letter. It spends the same daily LLM ledger. The question and word
// bound are the cover letter's, not the request's.
func (e *env) cmdCover(args []string) error {
	fs := e.newFlags("cover")
	if err := parse(fs, args); err != nil {
		return err
	}
	reply, err := e.coverOnce()
	return e.writeReply(reply, err)
}

func (e *env) coverOnce() (coverReply, error) {
	raw, err := io.ReadAll(io.LimitReader(draftInput, maxDraftInput+1))
	if err != nil {
		return coverReply{Error: "cover: read request", Code: draft.CodeBadRequest}, err
	}
	if len(raw) > maxDraftInput {
		return coverFail(draft.CodeBadRequest, "cover: request larger than %d bytes", maxDraftInput)
	}
	req, err := decodeCoverRequest(raw)
	if err != nil {
		return coverFail(draft.CodeBadRequest, "%s", err.Error())
	}
	if strings.TrimSpace(req.Job.Description) == "" {
		return coverFail("no_text", "cover: the posting has no text")
	}
	ctx, cancel := signalContext()
	defer cancel()
	w, code, err := e.coverConfig(ctx)
	if err != nil {
		return coverReply{Error: err.Error(), Code: code}, err
	}
	head := cover.LetterheadLines(w.BaseSelection, w.Bank)
	letter, err := cover.Write(ctx, w.Drafter, req.Job, head)
	return coverFromLetter(letter, err)
}

func decodeCoverRequest(raw []byte) (draft.Request, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var req draft.Request
	if err := dec.Decode(&req); err != nil {
		return draft.Request{}, fmt.Errorf("cover: request is not a draft request")
	}
	if dec.More() {
		return draft.Request{}, errors.New("cover: the request contains more than one JSON value")
	}
	return req, nil
}

func coverFromLetter(letter cover.Letter, err error) (coverReply, error) {
	reply := coverReply{
		Body:          letter.Body,
		Model:         letter.Model,
		EvidenceItems: letter.EvidenceItems,
		CompanyCheck:  letter.CompanyCheck,
	}
	if letter.CompanyCheck == draft.CompanySkipped {
		reply.Note = draft.CompanySkippedNote
	}
	if err != nil {
		code := draft.CodeProvider
		var de *draft.Error
		if errors.As(err, &de) {
			code = de.Code
		}
		if c := cover.CodeOf(err); c != "" {
			code = c
		}
		if cover.Kept(letter, err) {
			reply.Error = err.Error()
			reply.Code = code
			reply.PDF = ""
			reply.Bytes = 0
			return reply, err
		}
		return coverReply{Error: err.Error(), Code: code}, err
	}
	reply.PDF = base64.StdEncoding.EncodeToString(letter.PDF)
	reply.Bytes = len(letter.PDF)
	return reply, nil
}

func coverFail(code, format string, a ...any) (coverReply, error) {
	err := fmt.Errorf(format, a...)
	return coverReply{Error: err.Error(), Code: code}, err
}
