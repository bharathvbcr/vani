package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/jobcategory"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// cloudShadowRig is the recommended cloud setup on the real wiring: the
// OpenRouter model decides (a fake Liquid D reading role_fit mismatch_domain at
// 0.95, a drop candidate), Claude on llm.triage_model is the drop-candidate
// shadow (decide.shadow "llm", shadow_mode drop_candidates), and llm.model (the
// expensive tier) is configured but never asked.
func cloudShadowRig(t *testing.T, mut func(*config.Config)) *escalationRig {
	t.Helper()
	cfg := anthropicConfig(t)
	t.Setenv("OPENROUTER_API_KEY", "test-not-a-real-key")
	cfg.Decide.OpenRouterModel = "liquid/d1-ultra"
	cfg.LLM.TriageModel = "claude-haiku-5-5"
	cfg.Decide.Shadow = "llm"
	cfg.Decide.ShadowMode = config.ShadowModeDropCandidates
	cfg.DropRules = []config.DropRule{{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinOpenRouterProb: 0.9}}
	if mut != nil {
		mut(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	answers := map[string]any{}
	for _, s := range jdtriage.Slots {
		answers[s.Name] = map[string]any{"choice": s.Options[0].Value, "confidence": 0.9}
	}
	answers["role_fit"] = map[string]any{"choice": "mismatch_domain", "confidence": 0.95}
	answers[jobcategory.Slot] = map[string]any{"choice": jobcategory.OptionValues()[0], "confidence": 0.9}
	body, _ := json.Marshal(map[string]any{"answers": answers, "usage": map[string]any{"cost": 0}})
	jev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	t.Cleanup(jev.Close)
	claudeURL, bodies := sseMessages(t, triageReply(map[string]string{"role_fit": "strong_fit"}))
	t.Setenv("ANTHROPIC_BASE_URL", claudeURL)
	w := &wired{cfg: cfg, stateDir: t.TempDir(), warn: os.Stderr, backendErrs: newBackendErrors()}
	r, err := w.runner(context.Background(), rewriteDoer{jev})
	if err != nil {
		t.Fatal(err)
	}
	l := newEventLedger()
	if err := l.Append(context.Background(), liveEvent("gh:acme:1")); err != nil {
		t.Fatal(err)
	}
	r.Ledger = l
	return &escalationRig{w: w, r: r, l: l, claudeBody: bodies}
}

// decide.shadow "llm" in drop_candidates mode, end to end: only the drop
// candidate slot is asked, on llm.triage_model (never llm.model), the answer is
// recorded under the "llm" backend as a disagreement, the drop still follows the
// primary's rule, and the spend lands in the one llm daily ledger at the triage
// model's price.
func TestWiredLLMShadowDropCandidatesEndToEnd(t *testing.T) {
	rig := cloudShadowRig(t, nil)
	if err := rig.r.Advance(context.Background(), "gh:acme:1"); err != nil {
		t.Fatal(err)
	}
	rec, _ := rig.l.Get("gh:acme:1")
	ann := rec.Annotations
	want := map[string]string{
		"triage.role_fit":                "mismatch_domain",
		"triage.role_fit.backend":        "d1-ultra",
		"triage.role_fit.shadow":         "strong_fit",
		"triage.role_fit.shadow_reading": "answered",
		"triage.role_fit.shadow_backend": "llm",
		"triage.role_fit.disagree":       "true",
	}
	for k, v := range want {
		if ann[k] != v {
			t.Errorf("annotation %s = %q, want %q (all: %v)", k, ann[k], v, ann)
		}
	}
	if rec.Status != posting.Dropped {
		t.Errorf("status %s: the shadow's disagreement must not change the primary's drop", rec.Status)
	}
	bodies := *rig.claudeBody
	if len(bodies) != 1 || requestModel(t, bodies[0]) != "claude-haiku-5-5" {
		t.Fatalf("shadow requests: %v (want one, on claude-haiku-5-5, never claude-opus-5-5)", bodies)
	}
	files, _ := filepath.Glob(filepath.Join(rig.w.stateDir, "budget", "llm-*.json"))
	if len(files) != 1 {
		t.Fatalf("budget ledgers %v, want the one llm daily ledger", files)
	}
	snap, err := budget.Inspect(context.Background(), files[0])
	if err != nil {
		t.Fatal(err)
	}
	if snap.Settled != 1 || snap.SpentNanoUSD <= 0 || snap.SpentNanoUSD > 100_000 {
		t.Fatalf("ledger %+v: want one settled Haiku-priced attempt", snap)
	}
}

// Past the llm daily cap the shadow is refused by the same ledger: no request is
// sent, the slot's shadow reading is unavailable (never an agreement), and the
// primary's drop stands.
func TestWiredLLMShadowStopsAtTheLLMDailyCap(t *testing.T) {
	rig := cloudShadowRig(t, func(c *config.Config) { c.Decide.DailyUSD.LLM = 0.0000001 })
	if err := rig.r.Advance(context.Background(), "gh:acme:1"); err != nil {
		t.Fatalf("a refused shadow failed the posting: %v", err)
	}
	if n := len(*rig.claudeBody); n != 0 {
		t.Fatalf("%d request(s) sent past the cap", n)
	}
	rec, _ := rig.l.Get("gh:acme:1")
	ann := rec.Annotations
	if ann["triage.role_fit.shadow_reading"] != "unavailable" || ann["triage.role_fit.shadow_backend"] != "llm" || rec.Status != posting.Dropped {
		t.Fatalf("status %s annotations %v", rec.Status, ann)
	}
	if _, has := ann["triage.role_fit.disagree"]; has {
		t.Fatalf("an outage is not a disagreement: %v", ann)
	}
}
