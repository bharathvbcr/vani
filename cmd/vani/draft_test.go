package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Vani/internal/config"
)

type draftStream struct {
	text string
	done bool
}

func (s *draftStream) Next() (llm.Chunk, error) {
	if !s.done {
		s.done = true
		return llm.Chunk{Kind: llm.ChunkText, Text: s.text}, nil
	}
	return llm.Chunk{}, io.EOF
}
func (s *draftStream) Response() (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock{Text: s.text}}}}, nil
}
func (s *draftStream) Close() error { return nil }

type draftLLM struct {
	text string
	err  error
	reqs []llm.Request
}

func (p *draftLLM) Name() string { return "fake" }
func (p *draftLLM) Capability(model string) (llm.Capability, bool) {
	return llm.Capability{Provider: "fake", Model: model, MaxOutputTokens: 4096}, true
}
func (p *draftLLM) Stream(_ context.Context, req llm.Request) (llm.Stream, error) {
	p.reqs = append(p.reqs, req)
	if p.err != nil {
		return nil, p.err
	}
	return &draftStream{text: p.text}, nil
}

// draftSetup writes a config with the synthetic bank wired in and replaces the
// provider and stdin seams.
func draftSetup(t *testing.T, input string, p *draftLLM, withKB bool) {
	t.Helper()
	bank, rkb, _ := kbSetup(t, "")
	cfgPath := os.Getenv("VANI_CONFIG")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if withKB {
		cfg.KB.Bank, cfg.KB.RKB = bank, rkb
	}
	cfg.TargetSummary = "ML inference roles"
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	oldIn, oldP := draftInput, draftProvider
	draftInput = strings.NewReader(input)
	draftProvider = func(context.Context, *wired) (llm.Provider, error) { return p, nil }
	t.Cleanup(func() { draftInput, draftProvider = oldIn, oldP })
}

func draftOut(t *testing.T, stdout string) draftReply {
	t.Helper()
	var r draftReply
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout is not one JSON reply: %q (%v)", stdout, err)
	}
	return r
}

const goodDraftReq = `{"question":"Why do you want to cut latency here?","job":{"company":"Modal","title":"Engineer","description":"Latency matters."},"max_words":120}`

func TestDraftUsesKnowledgeBaseWithoutPersonalData(t *testing.T) {
	p := &draftLLM{text: "I cut latency by 42% at Acme."}
	draftSetup(t, goodDraftReq, p, true)
	stdout, _ := mustCode(t, 0, "draft")
	r := draftOut(t, stdout)
	if r.Draft != "I cut latency by 42% at Acme." || r.Error != "" || r.EvidenceItems != 1 {
		t.Fatalf("reply = %+v", r)
	}
	if len(p.reqs) != 1 {
		t.Fatalf("%d provider calls", len(p.reqs))
	}
	msg := p.reqs[0].Messages[0].Text()
	if !strings.Contains(msg, "Cut latency by 42%.") {
		t.Errorf("resolved evidence missing from the prompt: %q", msg)
	}
	for _, personal := range []string{"Synthetic Person", "person@example.invalid"} {
		if strings.Contains(msg, personal) || strings.Contains(p.reqs[0].System, personal) {
			t.Errorf("%q reached the prompt", personal)
		}
	}
}

func TestDraftWithoutKnowledgeBaseStillDrafts(t *testing.T) {
	p := &draftLLM{text: "A draft."}
	draftSetup(t, goodDraftReq, p, false)
	stdout, stderr := mustCode(t, 0, "draft")
	if r := draftOut(t, stdout); r.Draft != "A draft." || r.EvidenceItems != 0 {
		t.Errorf("reply = %+v", r)
	}
	if !strings.Contains(stderr, "without knowledge-base evidence") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestDraftRefusalsAreJSONWithCodes(t *testing.T) {
	cases := []struct {
		name, input, code string
		p                 *draftLLM
	}{
		{"not json", "{", "bad_request", &draftLLM{}},
		{"unknown field", `{"question":"Why?","ssn":"1"}`, "bad_request", &draftLLM{}},
		{"empty question", `{"question":" "}`, "bad_request", &draftLLM{}},
		{"oversized", `{"question":"` + strings.Repeat("q", maxDraftInput) + `"}`, "bad_request", &draftLLM{}},
		{"budget", goodDraftReq, "budget_exhausted", &draftLLM{err: fmt.Errorf("reserve: %w", budget.ErrExhausted)}},
		{"provider", goodDraftReq, "provider", &draftLLM{err: fmt.Errorf("503")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			draftSetup(t, c.input, c.p, true)
			stdout, stderr := mustCode(t, 1, "draft")
			r := draftOut(t, stdout)
			if r.Code != c.code || r.Error == "" || r.Draft != "" {
				t.Errorf("reply = %+v", r)
			}
			if strings.Contains(stderr, "usage") {
				t.Errorf("a refusal printed usage: %q", stderr)
			}
			if c.code == "bad_request" && len(c.p.reqs) != 0 {
				t.Error("a bad request reached the provider")
			}
		})
	}
}

func TestDraftNeedsAProvider(t *testing.T) {
	p := &draftLLM{text: "x"}
	draftSetup(t, goodDraftReq, p, true)
	cfgPath := os.Getenv("VANI_CONFIG")
	cfg, _ := config.Load(cfgPath)
	cfg.LLM.Provider, cfg.LLM.Model = "", ""
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	stdout, _ := mustCode(t, 1, "draft")
	if r := draftOut(t, stdout); r.Code != "config" {
		t.Errorf("reply = %+v", r)
	}
	if len(p.reqs) != 0 {
		t.Error("provider called without a configured llm")
	}
}

func TestDraftBadBankIsAKBError(t *testing.T) {
	p := &draftLLM{text: "x"}
	draftSetup(t, goodDraftReq, p, true)
	cfg, _ := config.Load(os.Getenv("VANI_CONFIG"))
	if err := os.WriteFile(cfg.KB.Bank, []byte(`{"entries":{"x":null}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _ := mustCode(t, 1, "draft")
	if r := draftOut(t, stdout); r.Code != "kb" {
		t.Errorf("reply = %+v", r)
	}
}
