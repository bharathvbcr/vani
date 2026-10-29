package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/suggest"
)

// countingProvider wraps draftProvider so a test can tell whether a command
// built a provider at all.
func countProviderBuilds(t *testing.T) *int {
	t.Helper()
	n := new(int)
	inner := draftProvider
	draftProvider = func(ctx context.Context, w *wired) (llm.Provider, error) {
		*n++
		return inner(ctx, w)
	}
	return n
}

func stateOutcomes(t *testing.T) ([]suggest.Outcome, []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(os.Getenv("VANI_STATE_DIR"), suggest.OutcomeDir, suggest.OutcomeFile))
	if err != nil {
		t.Fatalf("no outcome log: %v", err)
	}
	var out []suggest.Outcome
	for _, ln := range bytes.Split(raw, []byte{'\n'}) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var o suggest.Outcome
		if err := json.Unmarshal(ln, &o); err != nil {
			t.Fatalf("unreadable outcome line %q: %v", ln, err)
		}
		out = append(out, o)
	}
	return out, raw
}

func TestDraftRefusesANeverDraftedQuestionBeforeSpending(t *testing.T) {
	for q, cat := range map[string]string{
		"Are you legally authorized to work in the United States?": "work_authorization",
		"Please tell us your gender":                               "self_identification",
		"Do you agree to the arbitration agreement?":               "legal_consent",
	} {
		p := &draftLLM{text: "I cut latency by 42%."}
		draftSetup(t, `{"question":"`+q+`","job":{"company":"Modal","title":"Engineer","description":"Latency matters."}}`, p, true)
		builds := countProviderBuilds(t)
		stdout, _ := mustCode(t, 1, "draft")
		r := draftOut(t, stdout)
		if r.Code != "not_drafted" || r.Category != cat || r.Draft != "" || r.Error == "" {
			t.Errorf("%q: reply = %+v", q, r)
		}
		if len(p.reqs) != 0 || *builds != 0 {
			t.Errorf("%q: %d provider turns, %d provider builds before refusing", q, len(p.reqs), *builds)
		}
		outs, raw := stateOutcomes(t)
		if len(outs) != 1 || outs[0].Op != suggest.OpDraft || outs[0].Fields[0].Status != suggest.Manual || outs[0].Fields[0].Code != "not_drafted" || outs[0].Fields[0].Category != cat {
			t.Errorf("%q: outcomes = %+v", q, outs)
		}
		if bytes.Contains(raw, []byte(q[:12])) || bytes.Contains(raw, []byte("Modal")) {
			t.Errorf("%q: question or company reached the outcome log: %s", q, raw)
		}
	}
}

func TestDraftRecordsACountsOnlyOutcome(t *testing.T) {
	p := &draftLLM{text: "I cut latency by 42% at Acme."}
	draftSetup(t, goodDraftReq, p, true)
	mustCode(t, 0, "draft")
	outs, raw := stateOutcomes(t)
	if len(outs) != 1 {
		t.Fatalf("outcomes = %s", raw)
	}
	f := outs[0].Fields[0]
	if outs[0].Op != "draft" || f.Status != "drafted" || f.Model != testLLM.Model || f.Turns != 1 || outs[0].Drafted != 1 {
		t.Errorf("outcome = %+v", outs[0])
	}
	for _, leak := range []string{"latency", "Acme", "Modal", "42%", "Engineer"} {
		if bytes.Contains(raw, []byte(leak)) {
			t.Errorf("%q reached the outcome log: %s", leak, raw)
		}
	}
	// A failed draft records its code.
	p2 := &draftLLM{text: "I cut latency by 99% at Acme."}
	draftSetup(t, goodDraftReq, p2, true)
	mustCode(t, 1, "draft")
	_, raw = stateOutcomes(t)
	if !bytes.Contains(raw, []byte(`"code":"ungrounded"`)) || !bytes.Contains(raw, []byte(`"turns":2`)) {
		t.Errorf("an ungrounded refusal after a repair turn is not counted: %s", raw)
	}
	if len(p2.reqs) != 2 {
		t.Errorf("%d provider turns, want the draft and one repair", len(p2.reqs))
	}
}

// modelSpy records which model each kind of turn asked for.
type modelSpy struct {
	*suggestLLM
	mmu    sync.Mutex
	models map[string][]string
}

func (m *modelSpy) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	kind := "draft"
	if strings.HasPrefix(req.System, "You classify text") {
		kind = "classify"
	}
	m.mmu.Lock()
	m.models[kind] = append(m.models[kind], req.Model)
	m.mmu.Unlock()
	return m.suggestLLM.Stream(ctx, req)
}

func TestSuggestFallbackDeciderUsesTheTriageModel(t *testing.T) {
	spy := &modelSpy{suggestLLM: &suggestLLM{kinds: map[string]string{"Why Modal?": "essay"}}, models: map[string][]string{}}
	suggestSetup(t, goodSuggestReq, spy)
	cfgPath := os.Getenv("VANI_CONFIG")
	cfg, _ := config.Load(cfgPath)
	cfg.LLM.TriageModel = "gemini-3.8-flash-lite"
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	mustCode(t, 0, "suggest")
	if got := spy.models["classify"]; len(got) == 0 || got[0] != "gemini-3.8-flash-lite" {
		t.Errorf("classification turns ran on %v, want the triage model", got)
	}
	if got := spy.models["draft"]; len(got) != 1 || got[0] != testLLM.Model {
		t.Errorf("the draft ran on %v, want the main model %s", got, testLLM.Model)
	}
	// Without a triage model, the main model triages (EffectiveTriageModel).
	spy2 := &modelSpy{suggestLLM: &suggestLLM{kinds: map[string]string{"Why Modal?": "essay"}}, models: map[string][]string{}}
	suggestSetup(t, goodSuggestReq, spy2)
	mustCode(t, 0, "suggest")
	if got := spy2.models["classify"]; len(got) == 0 || got[0] != testLLM.Model {
		t.Errorf("with no triage model, classification ran on %v", got)
	}
}

// A knowledge base that cannot be read costs the drafts, not the page.
func TestSuggestKBFailureStillTriagesEveryField(t *testing.T) {
	p := &suggestLLM{kinds: map[string]string{"Why Modal?": "essay", "GitHub URL": "applicant_fact"}}
	kb := defaultDraftKB()
	kb.selections = nil // fails closed: code kb
	draftSetupKB(t, "", &draftLLM{}, kb)
	suggestInput = strings.NewReader(goodSuggestReq)
	draftProvider = func(context.Context, *wired) (llm.Provider, error) { return p, nil }
	stdout, stderr := mustCode(t, 0, "suggest")
	r := suggestOut(t, stdout)
	if r.Error != "" || len(r.Fields) != 4 {
		t.Fatalf("a kb failure took the whole page down: %+v", r)
	}
	got := map[string]string{}
	for _, f := range r.Fields {
		got[f.ID] = f.Status + "/" + f.Code + "/" + f.Category
	}
	// A fact is answered from the knowledge base, so it fails with the essay; the
	// pinned fields never needed it.
	want := map[string]string{"q1": "failed/kb/essay", "q2": "failed/kb/applicant_fact", "q3": "manual//work_authorization", "q4": "manual//legal_consent"}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s = %s, want %s", id, got[id], w)
		}
	}
	if len(p.drafts) != 0 {
		t.Errorf("%d drafts or answers spent with no knowledge base", len(p.drafts))
	}
	if !strings.Contains(stderr, "kb.selections") {
		t.Errorf("the cause is not on stderr: %q", stderr)
	}
	if outs, _ := stateOutcomes(t); len(outs) != 1 || outs[0].Fields[0].Code != "kb" {
		t.Errorf("outcomes = %+v", outs)
	}
}

func TestSuggestAcceptsMaxCharsAndTreatsShortFieldsAsManual(t *testing.T) {
	p := &suggestLLM{kinds: map[string]string{"Why Modal?": "essay", "Tell us more": "essay"}}
	req := `{"job":{"company":"Modal","title":"Engineer","description":"Build the fastest serverless GPU platform for inference workloads."},
"fields":[
 {"id":"a","label":"Why Modal?","kind":"textarea","max_chars":120},
 {"id":"b","label":"Tell us more","kind":"textarea","max_chars":1000}]}`
	suggestSetup(t, req, p)
	stdout, _ := mustCode(t, 0, "suggest")
	r := suggestOut(t, stdout)
	if r.Error != "" || len(r.Fields) != 2 {
		t.Fatalf("reply = %+v", r)
	}
	if r.Fields[0].Status != "manual" || r.Fields[0].Reason != "short-answer field" {
		t.Errorf("a 120 character field = %+v", r.Fields[0])
	}
	if r.Fields[1].Status != "drafted" {
		t.Errorf("a 1000 character field = %+v", r.Fields[1])
	}
	if len(p.drafts) != 1 || !strings.Contains(p.drafts[0], "Write at most 153 words") {
		t.Errorf("the draft was not asked for 153 words: %v", p.drafts)
	}
	for name, bad := range map[string]string{"negative": "-1", "huge": "1000000", "string": `"50"`} {
		suggestSetup(t, `{"fields":[{"id":"a","label":"Why?","kind":"text","max_chars":`+bad+`}]}`, &suggestLLM{})
		out, _ := mustCode(t, 1, "suggest")
		if r := suggestOut(t, out); r.Code != "bad_request" {
			t.Errorf("%s max_chars: %+v", name, r)
		}
	}
}
