package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/config"
)

// suggestLLM answers classification turns (llmbackend's system prompt) from a
// label table and every other turn with a draft. It is safe for the
// concurrent triage calls.
type suggestLLM struct {
	mu       sync.Mutex
	kinds    map[string]string // label substring -> kind
	classify []string          // user messages of classification turns
	drafts   []string          // user messages of draft turns
}

func (p *suggestLLM) Name() string { return "fake" }
func (p *suggestLLM) Capability(model string) (llm.Capability, bool) {
	return llm.Capability{Provider: "fake", Model: model, MaxOutputTokens: 4096}, true
}
func (p *suggestLLM) Stream(_ context.Context, req llm.Request) (llm.Stream, error) {
	msg := req.Messages[0].Text()
	p.mu.Lock()
	defer p.mu.Unlock()
	if strings.HasPrefix(req.System, "You classify text") {
		p.classify = append(p.classify, msg)
		kind := "other"
		for sub, k := range p.kinds {
			if strings.Contains(msg, sub) {
				kind = k
			}
		}
		return &draftStream{text: `{"kind":"` + kind + `"}`}, nil
	}
	p.drafts = append(p.drafts, msg)
	return &draftStream{text: "I want to build reliable inference."}, nil
}

func suggestSetup(t *testing.T, input string, p llm.Provider) {
	t.Helper()
	draftSetup(t, "", &draftLLM{}, false)
	oldIn := suggestInput
	suggestInput = strings.NewReader(input)
	draftProvider = func(context.Context, *wired) (llm.Provider, error) { return p, nil }
	t.Cleanup(func() { suggestInput = oldIn })
}

func suggestOut(t *testing.T, stdout string) suggestReply {
	t.Helper()
	var r suggestReply
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout is not one JSON reply: %q (%v)", stdout, err)
	}
	return r
}

const goodSuggestReq = `{"job":{"company":"Modal","title":"Engineer","description":"Build the fastest serverless GPU platform for inference workloads."},
"fields":[
 {"id":"q1","label":"Why Modal?","kind":"textarea"},
 {"id":"q2","label":"GitHub URL","kind":"text"},
 {"id":"q3","label":"Will you now or in the future require visa sponsorship?","kind":"text"},
 {"id":"q4","label":"Agreement to Arbitrate","kind":"text"}]}`

func TestSuggestTriagesWithTheDecisionModelAndDraftsEssaysOnly(t *testing.T) {
	p := &suggestLLM{kinds: map[string]string{"Why Modal?": "essay", "GitHub URL": "applicant_fact"}}
	suggestSetup(t, goodSuggestReq, p)
	stdout, _ := mustCode(t, 0, "suggest")
	r := suggestOut(t, stdout)
	if r.Error != "" || len(r.Fields) != 4 {
		t.Fatalf("reply = %+v", r)
	}
	got := map[string]string{}
	for _, f := range r.Fields {
		got[f.ID] = f.Status + "/" + f.Category
	}
	// No knowledge base is configured here, so the fact has nothing to be answered from.
	if q2 := r.Fields[1]; q2.Code != "no_evidence" || q2.Reason != "Your knowledge base does not answer this." {
		t.Errorf("q2 = %+v", q2)
	}
	want := map[string]string{"q1": "drafted/essay", "q2": "manual/applicant_fact", "q3": "manual/work_authorization", "q4": "manual/legal_consent"}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s = %s, want %s", id, got[id], w)
		}
	}
	if r.Fields[0].Draft != "I want to build reliable inference." || r.Fields[0].Backend != "llm" {
		t.Errorf("q1 = %+v", r.Fields[0])
	}
	if len(p.classify) != 2 || len(p.drafts) != 1 {
		t.Fatalf("%d classification turns, %d drafts; want 2 and 1", len(p.classify), len(p.drafts))
	}
	for _, m := range append(append([]string{}, p.classify...), p.drafts...) {
		if strings.Contains(m, "sponsorship") || strings.Contains(m, "Arbitrate") {
			t.Errorf("a guarded label reached the model: %q", m)
		}
	}
}

func TestSuggestRefusalsAreJSONWithCodes(t *testing.T) {
	cases := []struct{ name, input, code string }{
		{"not json", "{", "bad_request"},
		{"unknown field", `{"fields":[{"id":"a","label":"Why?","kind":"text","value":"secret"}]}`, "bad_request"},
		{"no fields", `{"job":{}}`, "bad_request"},
		{"select field", `{"fields":[{"id":"a","label":"Why?","kind":"select"}]}`, "bad_request"},
		{"oversized", `{"fields":[{"id":"a","label":"` + strings.Repeat("q", maxSuggestInput) + `","kind":"text"}]}`, "bad_request"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &suggestLLM{}
			suggestSetup(t, c.input, p)
			stdout, _ := mustCode(t, 1, "suggest")
			r := suggestOut(t, stdout)
			if r.Code != c.code || r.Error == "" || len(r.Fields) != 0 {
				t.Errorf("reply = %+v", r)
			}
			if len(p.classify)+len(p.drafts) != 0 {
				t.Error("a bad request reached the provider")
			}
		})
	}
}

func TestSuggestNeedsAProvider(t *testing.T) {
	p := &suggestLLM{}
	suggestSetup(t, goodSuggestReq, p)
	cfgPath := os.Getenv("VANI_CONFIG")
	cfg, _ := config.Load(cfgPath)
	cfg.LLM.Provider, cfg.LLM.Model = "", ""
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	stdout, _ := mustCode(t, 1, "suggest")
	if r := suggestOut(t, stdout); r.Code != "config" {
		t.Errorf("reply = %+v", r)
	}
}
