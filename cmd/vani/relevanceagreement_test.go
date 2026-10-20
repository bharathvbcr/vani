package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func seedDropCandidate(t *testing.T, stateDir, key, shadowValue string) {
	seedDropCandidateBy(t, stateDir, key, shadowValue, "local-d3-mini-per_slot")
}

func seedDropCandidateBy(t *testing.T, stateDir, key, shadowValue, shadowBackend string) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	p := posting.Posting{Key: posting.Key(key), ATS: "gh", Company: "Acme", Title: "Engineer " + key, Description: "Build things."}
	ann := map[string]string{
		"triage.role_fit": "mismatch_domain", "triage.role_fit.reading": "answered", "triage.role_fit.backend": "jev-1-13",
		"triage.role_fit.p": "0.9500", "triage.role_fit.shadow_backend": shadowBackend,
		"triage.role_fit.shadow_reading": "answered", "triage.role_fit.shadow": shadowValue,
	}
	if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Dropped, Posting: &p, Annotations: ann}); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

// `vani relevance` reports the shadow's agreement on drop candidates even with
// no would_apply label, from the ledger and the configured drop rules.
func TestRelevanceReportsDropCandidateAgreementWithoutLabels(t *testing.T) {
	cfgPath, state := setup(t)
	mustCode(t, 0, "init")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DropRules = []config.DropRule{{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinOpenRouterProb: 0.9}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	seedDropCandidate(t, state, "gh:a:1", "mismatch_domain")
	seedDropCandidate(t, state, "gh:a:2", "strong_fit")
	o, _ := mustCode(t, 0, "relevance")
	for _, frag := range []string{"no data: no judged would_apply labels", "## Drop candidates: shadow vs jev-1-13", "| role_fit | local-d3-mini-per_slot | 2 | 0 | 2 | 1/2 = 50%", "| 1 | 0 | 0 |"} {
		if !strings.Contains(o, frag) {
			t.Errorf("report lacks %q:\n%s", frag, o)
		}
	}
}

// A cloud shadow is labelled by its backend and the model it ran on, from the
// config (the ledger records only the backend name "llm"); a local shadow keeps
// its own model-bearing name and no "d3-mini" is ever invented.
func TestRelevanceLabelsTheCloudShadowByBackendAndModel(t *testing.T) {
	cfgPath, state := setup(t)
	mustCode(t, 0, "init")
	cfg := anthropicConfig(t)
	cfg.LLM.TriageModel = "claude-haiku-5-5"
	cfg.Decide.OpenRouterModel = config.Default().Decide.OpenRouterModel
	cfg.Decide.Shadow = "llm"
	cfg.Decide.ShadowMode = config.ShadowModeDropCandidates
	cfg.DropRules = []config.DropRule{{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinOpenRouterProb: 0.9}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	seedDropCandidateBy(t, state, "gh:a:1", "mismatch_domain", "llm")
	seedDropCandidateBy(t, state, "gh:a:2", "strong_fit", "llm")
	o, _ := mustCode(t, 0, "relevance")
	if !strings.Contains(o, "| role_fit | llm (claude-haiku-5-5) | 2 | 0 | 2 | 1/2 = 50%") {
		t.Errorf("shadow not labelled by backend and model:\n%s", o)
	}
	if strings.Contains(o, "d3-mini") {
		t.Errorf("report names a local model that did not run:\n%s", o)
	}
}
