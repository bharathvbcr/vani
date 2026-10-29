package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm"
)

// answerLLM plays the three kinds of turn a suggest page can make: a label
// classification (from a table), an essay draft, and the batched fact and choice
// answers (from a fixed JSON reply). Safe for the concurrent triage calls.
type answerLLM struct {
	mu       sync.Mutex
	kinds    map[string]string // label substring -> triage kind
	batch    string            // the reply to the batched turn
	classify []string
	drafts   []string
	batches  []string
}

func (p *answerLLM) Name() string { return "fake" }
func (p *answerLLM) Capability(model string) (llm.Capability, bool) {
	return llm.Capability{Provider: "fake", Model: model, MaxOutputTokens: 4096}, true
}
func (p *answerLLM) Stream(_ context.Context, req llm.Request) (llm.Stream, error) {
	msg := req.Messages[0].Text()
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case strings.HasPrefix(req.System, "You classify text"):
		p.classify = append(p.classify, msg)
		kind := "other"
		for sub, k := range p.kinds {
			if strings.Contains(msg, sub) {
				kind = k
			}
		}
		return &draftStream{text: `{"kind":"` + kind + `"}`}, nil
	case strings.HasPrefix(req.System, "You answer short fact"):
		p.batches = append(p.batches, msg)
		return &draftStream{text: p.batch}, nil
	}
	p.drafts = append(p.drafts, msg)
	return &draftStream{text: "I want to build reliable inference."}, nil
}

// A page with a fact, a choice and a work-authorization choice: the first two are
// answered in one turn from the knowledge base, the third never leaves the
// machine, and the outcome log counts the batch without any of its text.
func TestSuggestAnswersFactAndChoiceFieldsFromTheKnowledgeBase(t *testing.T) {
	p := &answerLLM{
		kinds: map[string]string{"How many years": "applicant_fact", "Have you shipped": "applicant_fact"},
		batch: `{"q1":{"answer":"4 years.","evidence":["e1"]},"q2":{"choice":"Yes","evidence":["e1"]}}`,
	}
	kb := defaultDraftKB()
	kb.bullets = map[string]any{"b1": reviewedBullet("Shipped Python services for {{clmYears}} years.", "Shipped Python services for 4 years.")}
	kb.claims = `{"claims":{"clmYears":{"value":"4"}}}`
	draftSetupKB(t, "", &draftLLM{}, kb)
	suggestInput = strings.NewReader(`{"job":{"company":"Modal","title":"Engineer","description":"Build Python services for the fastest serverless GPU platform."},
"fields":[
 {"id":"q1","label":"How many years have you shipped Python services?","kind":"text"},
 {"id":"q2","label":"Have you shipped Python services?","kind":"choice","options":["Yes","No"]},
 {"id":"q3","label":"Are you legally authorized to work in the US?","kind":"choice","options":["Yes","No"]}]}`)
	draftProvider = func(context.Context, *wired) (llm.Provider, error) { return p, nil }
	stdout, _ := mustCode(t, 0, "suggest")
	r := suggestOut(t, stdout)
	if r.Error != "" || len(r.Fields) != 3 {
		t.Fatalf("reply = %+v", r)
	}
	if f := r.Fields[0]; f.Status != "drafted" || f.AnswerKind != "fact" || f.Draft != "4 years." || f.Choice != "" || !strings.Contains(f.Reason, "yellow") {
		t.Errorf("q1 = %+v", f)
	}
	if f := r.Fields[1]; f.Status != "drafted" || f.AnswerKind != "choice" || f.Choice != "Yes" || f.Draft != "" {
		t.Errorf("q2 = %+v", f)
	}
	if f := r.Fields[2]; f.Status != "manual" || f.Category != "work_authorization" || f.AnswerKind != "" || f.Choice != "" {
		t.Errorf("q3 = %+v", f)
	}
	if len(p.batches) != 1 || len(p.drafts) != 0 {
		t.Fatalf("%d batched turns and %d essay drafts, want 1 and 0", len(p.batches), len(p.drafts))
	}
	for _, m := range append(append([]string{}, p.batches...), p.classify...) {
		if strings.Contains(m, "authorized") {
			t.Errorf("the work-authorization question reached a model: %q", m)
		}
	}
	for _, m := range p.classify {
		if strings.Contains(m, `"Yes"`) || strings.Contains(m, `"No"`) {
			t.Errorf("a choice's options reached the triage model: %q", m)
		}
	}
	if !strings.Contains(p.batches[0], `options: ["Yes","No"]`) || !strings.Contains(p.batches[0], "[e1] Shipped Python services for 4 years") {
		t.Errorf("the batched turn lacks the options or the evidence: %q", p.batches[0])
	}
	outs, raw := stateOutcomes(t)
	if len(outs) != 1 || outs[0].AnswerAsked != 2 || outs[0].AnswerAnswered != 2 || outs[0].AnswerTurns != 1 {
		t.Fatalf("outcomes = %+v", outs)
	}
	var kinds []string
	for _, f := range outs[0].Fields {
		kinds = append(kinds, f.AnswerKind)
	}
	if got := strings.Join(kinds, ","); got != "fact,choice," {
		t.Errorf("answer kinds %q", got)
	}
	for _, s := range []string{"Python", "4 years", "authorized"} {
		if strings.Contains(string(raw), s) {
			t.Errorf("%q reached the outcome log: %s", s, raw)
		}
	}
}

// A choice field whose options break the contract is refused before any model or
// config is touched.
func TestSuggestRefusesBadChoiceFields(t *testing.T) {
	for name, field := range map[string]string{
		"one option":   `{"id":"a","label":"Relocate?","kind":"choice","options":["Yes"]}`,
		"no options":   `{"id":"a","label":"Relocate?","kind":"choice"}`,
		"repeats":      `{"id":"a","label":"Relocate?","kind":"choice","options":["Yes","Yes"]}`,
		"options text": `{"id":"a","label":"Relocate?","kind":"text","options":["Yes","No"]}`,
		"a value":      `{"id":"a","label":"Relocate?","kind":"choice","options":["Yes","No"],"value":"Yes"}`,
	} {
		p := &answerLLM{}
		suggestSetup(t, `{"fields":[`+field+`]}`, p)
		stdout, _ := mustCode(t, 1, "suggest")
		if r := suggestOut(t, stdout); r.Code != "bad_request" {
			t.Errorf("%s: %+v", name, r)
		}
		if len(p.classify)+len(p.drafts)+len(p.batches) != 0 {
			t.Errorf("%s: reached the provider", name)
		}
	}
}
