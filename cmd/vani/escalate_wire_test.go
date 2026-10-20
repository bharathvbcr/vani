package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/credentials"
	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/jobcategory"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// anthropicConfig is a valid config whose only decision backend is the
// Anthropic LLM, reached through ANTHROPIC_BASE_URL on loopback.
func anthropicConfig(t *testing.T) config.Config {
	t.Helper()
	clearKeys(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-key-not-real")
	t.Setenv(credentials.AnthropicWorkspaceEnv, "")
	cfg := config.Default()
	cfg.Decide.OpenRouterModel = ""
	cfg.LLM = config.LLM{Provider: "anthropic", Model: "claude-opus-5-5", InputNanoUSDPerToken: 4000, OutputNanoUSDPerToken: 20000}
	cfg.Decide.DailyUSD.LLM = 10
	cfg.Watchlist = nil
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// triageReply is the JSON an LLM would answer the triage question with: every
// slot's first option, except the ones in pick.
func triageReply(pick map[string]string) string {
	obj := map[string]string{}
	for _, s := range jdtriage.Slots {
		obj[s.Name] = s.Options[0].Value
		if v, ok := pick[s.Name]; ok {
			obj[s.Name] = v
		}
	}
	b, _ := json.Marshal(obj)
	return string(b)
}

func liveEvent(key posting.Key) ledger.Event {
	return ledger.Event{V: ledger.SchemaVersion, At: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), Key: key, Status: posting.Live, Reason: "listed",
		Posting: &posting.Posting{Key: key, Company: "Acme", Title: "Engineer", Description: "Build and operate large-scale inference systems for production models. Fully remote within the US."}}
}

// `vani status --json` carries what each decision backend did in the last real
// run: a configured run through the CLI, the wired pipeline and a fake
// Anthropic server writes backend_stats into the run record, and status reads
// it back. (runReport once left BackendStats out, so the meter was collected
// and then lost.)
func TestWiredRunWritesBackendStatsToTheRunRecord(t *testing.T) {
	cfgPath, stateDir := setup(t)
	cfg := anthropicConfig(t)
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	srvURL, bodies := sseMessages(t, triageReply(nil))
	t.Setenv("ANTHROPIC_BASE_URL", srvURL)
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(context.Background(), liveEvent("gh:acme:1")); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	_, stderr := mustCode(t, 0, "run-once")
	if len(*bodies) != 1 {
		t.Fatalf("%d requests reached the LLM, want 1\nstderr: %s", len(*bodies), stderr)
	}
	out, _ := mustCode(t, 0, "status", "--json")
	var raw struct {
		LastRun struct {
			BackendStats map[string]struct {
				Calls    int            `json:"calls"`
				Readings map[string]int `json:"readings"`
			} `json:"backend_stats"`
		} `json:"last_run"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	st, ok := raw.LastRun.BackendStats["llm"]
	if !ok || st.Calls != 1 || st.Readings["answered"] != len(jdtriage.Slots) {
		t.Fatalf("status --json last_run.backend_stats = %+v\n%s", raw.LastRun.BackendStats, out)
	}
	if rec, err := readRunRecord(stateDir); err != nil || rec == nil || rec.BackendStats["llm"].Calls != 1 {
		t.Fatalf("run record %+v, %v", rec, err)
	}
}

// requestModel reads the "model" the Anthropic request body names.
func requestModel(t *testing.T, body string) string {
	t.Helper()
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("request body: %v\n%s", err, body)
	}
	return req.Model
}

// triageBodies runs one posting through the wired runner for cfg and returns
// the request bodies the LLM was sent.
func triageBodies(t *testing.T, cfg config.Config) []string {
	t.Helper()
	srvURL, bodies := sseMessages(t, triageReply(nil))
	t.Setenv("ANTHROPIC_BASE_URL", srvURL)
	r, _ := runnerFor(t, cfg, t.TempDir())
	if _, err := r.Triager.Triage(context.Background(), *liveEvent("gh:acme:1").Posting); err != nil {
		t.Fatal(err)
	}
	return *bodies
}

// The triage fallback asks llm.triage_model when one is set and llm.model
// otherwise; with none set the request is byte for byte what it was before the
// key existed (the same as naming llm.model explicitly).
func TestTriageFallbackUsesTheTriageModel(t *testing.T) {
	cfg := anthropicConfig(t)
	def := triageBodies(t, cfg)
	if len(def) != 1 || requestModel(t, def[0]) != "claude-opus-5-5" {
		t.Fatalf("default: %v", def)
	}
	cfg.LLM.TriageModel = "claude-opus-5-5"
	if same := triageBodies(t, cfg); len(same) != 1 || same[0] != def[0] {
		t.Fatalf("naming the model as triage_model changed the request:\n%v\nvs\n%v", same, def)
	}
	cfg.LLM.TriageModel = "claude-haiku-5-5"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	got := triageBodies(t, cfg)
	if len(got) != 1 || requestModel(t, got[0]) != "claude-haiku-5-5" {
		t.Fatalf("triage_model ignored: %v", got)
	}
}

// The shared provider meters llm.model at the configured rates, as before, and
// the triage_model at Manvi's table; a triage_model changes no price of llm.model.
func TestTriageModelIsPricedFromTheTableAndLLMModelFromConfig(t *testing.T) {
	cfg := anthropicConfig(t)
	cfg.LLM.TriageModel = "claude-haiku-5-5"
	srvURL, _ := sseMessages(t, "ok")
	t.Setenv("ANTHROPIC_BASE_URL", srvURL)
	w := &wired{cfg: cfg, stateDir: t.TempDir(), warn: os.Stderr}
	p, err := w.provider(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bp := p.(*budget.Provider)
	opus, ok := bp.PriceOf("claude-opus-5-5")
	if !ok || opus.Input != 4000 || opus.Output != 20000 {
		t.Fatalf("llm.model priced at %+v", opus)
	}
	haiku, _ := anthropic.PriceOf("claude-haiku-5-5")
	if got, _ := bp.PriceOf("claude-haiku-5-5"); got != haiku {
		t.Fatalf("triage_model priced at %+v, want Manvi's table %+v", got, haiku)
	}
}

// rewriteDoer sends every request to srv whatever its URL says, standing in for
// OpenRouter (the wiring builds the decide/jev client with its default endpoint).
type rewriteDoer struct{ srv *httptest.Server }

func (d rewriteDoer) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	u, _ := url.Parse(d.srv.URL)
	r := req.Clone(ctx)
	r.URL.Scheme, r.URL.Host, r.Host = u.Scheme, u.Host, u.Host
	return d.srv.Client().Do(r)
}

// jevAt answers every triage slot with its first option at the probability p
// names (default 0.9).
func jevAt(ps map[string]float64) string {
	answers := map[string]any{}
	for _, s := range jdtriage.Slots {
		p := 0.9
		if v, ok := ps[s.Name]; ok {
			p = v
		}
		answers[s.Name] = map[string]any{"choice": s.Options[0].Value, "confidence": p}
	}
	// Liquid D answers every slot it is asked, and the question also carries the
	// job-category slot the Triager appends (ExtraSlots).
	catP := 0.9
	if v, ok := ps[jobcategory.Slot]; ok {
		catP = v
	}
	answers[jobcategory.Slot] = map[string]any{"choice": jobcategory.OptionValues()[0], "confidence": catP}
	b, _ := json.Marshal(map[string]any{"answers": answers, "usage": map[string]any{"cost": 0}})
	return string(b)
}

// escalationRig is the real wiring with an OpenRouter primary (a fake Liquid D on
// loopback) and the Claude tier (a fake Anthropic on loopback), escalating
// below 0.5.
type escalationRig struct {
	w          *wired
	r          *pipeline.Runner
	l          *eventLedger
	jevCalls   *atomic.Int64
	claudeBody *[]string
}

func newEscalationRig(t *testing.T, mut func(*config.Config), jevPs map[string]float64, tier map[string]string) *escalationRig {
	t.Helper()
	cfg := anthropicConfig(t)
	t.Setenv("OPENROUTER_API_KEY", "test-not-a-real-key")
	cfg.Decide.OpenRouterModel = "liquid/d1-ultra"
	cfg.LLM.TriageModel = "claude-haiku-5-5"
	cfg.Decide.Escalate = &config.Escalate{Enabled: true}
	cfg.DropRules = []config.DropRule{{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinOpenRouterProb: 0.9}}
	if mut != nil {
		mut(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	var jevCalls atomic.Int64
	jev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jevCalls.Add(1)
		_, _ = w.Write([]byte(jevAt(jevPs)))
	}))
	t.Cleanup(jev.Close)
	claudeURL, bodies := sseMessages(t, triageReply(tier))
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
	return &escalationRig{w: w, r: r, l: l, jevCalls: &jevCalls, claudeBody: bodies}
}

// The whole path on the real wiring: the OpenRouter model reads role_fit at
// p=0.3 and the rest at 0.9; only role_fit goes to the Claude tier, on
// llm.triage_model, whose differing answer is recorded as the slot's shadow
// with the "escalate" backend and a disagreement. The primary value and its
// probability are untouched, and the run's meter shows both backends.
func TestWiredEscalationEndToEnd(t *testing.T) {
	rig := newEscalationRig(t, nil, map[string]float64{"role_fit": 0.3}, map[string]string{"role_fit": "mismatch_domain"})
	if err := rig.r.Advance(context.Background(), "gh:acme:1"); err != nil {
		t.Fatal(err)
	}
	rec, _ := rig.l.Get("gh:acme:1")
	ann := rec.Annotations
	want := map[string]string{
		"triage.role_fit":                "strong_fit",
		"triage.role_fit.p":              "0.3000",
		"triage.role_fit.backend":        "d1-ultra",
		"triage.role_fit.shadow":         "mismatch_domain",
		"triage.role_fit.shadow_reading": "answered",
		"triage.role_fit.shadow_backend": "escalate",
		"triage.role_fit.disagree":       "true",
	}
	for k, v := range want {
		if ann[k] != v {
			t.Errorf("annotation %s = %q, want %q (all: %v)", k, ann[k], v, ann)
		}
	}
	for _, slot := range []string{"sponsorship", "work_mode", "seniority"} {
		for _, suffix := range []string{".shadow", ".shadow_reading", ".shadow_backend", ".disagree"} {
			if v, has := ann["triage."+slot+suffix]; has {
				t.Errorf("%s was not escalated but has triage.%s%s = %q", slot, slot, suffix, v)
			}
		}
	}
	if rec.Status == posting.Dropped {
		t.Fatalf("the Claude tier's mismatch_domain dropped the posting: %+v", rec)
	}

	bodies := *rig.claudeBody
	if len(bodies) != 1 || requestModel(t, bodies[0]) != "claude-haiku-5-5" {
		t.Fatalf("Claude requests: %v", bodies)
	}
	// The question lists one `- "<slot>": [options]` line per slot asked (in the
	// JSON body the quotes are escaped; an option value like mismatch_seniority
	// names a slot without being one).
	listed := func(slot string) bool { return strings.Contains(bodies[0], `- \"`+slot+`\":`) }
	if !listed("role_fit") || listed("sponsorship") || listed("work_mode") || listed("seniority") {
		t.Fatalf("the escalation asked more or other than role_fit:\n%s", bodies[0])
	}
	stats := rig.w.runReport().BackendStats
	if stats["d1-ultra"].Calls != 1 || stats["escalate"].Calls != 1 || stats["escalate"].Readings["answered"] != 1 {
		t.Fatalf("backend_stats %+v", stats)
	}
	if _, has := stats["llm"]; has {
		t.Fatalf("the triage fallback was called though the primary read every slot: %+v", stats)
	}

	// One metered provider: the Claude tier's spend is in the same single daily
	// ledger, priced as Haiku (120 in, 30 out: 27,000 nano-USD, where Opus at the
	// configured rates would be 1,080,000).
	files, _ := filepath.Glob(filepath.Join(rig.w.stateDir, "budget", "llm-*.json"))
	if len(files) != 1 {
		t.Fatalf("budget ledgers %v, want one shared by all LLM spend", files)
	}
	snap, err := budget.Inspect(context.Background(), files[0])
	if err != nil {
		t.Fatal(err)
	}
	if snap.Settled != 1 || snap.SpentNanoUSD <= 0 || snap.SpentNanoUSD > 100_000 {
		t.Fatalf("ledger %+v: want one settled Haiku-priced attempt", snap)
	}
}

// Past the llm daily cap the escalation is refused by the same ledger every
// other LLM call is metered on: no request is sent, the tier's slots read
// unavailable in Shadow, the error is in the meter, and the primary answer and
// the posting's status are what they would have been.
func TestWiredEscalationStopsAtTheLLMDailyCap(t *testing.T) {
	rig := newEscalationRig(t, func(c *config.Config) { c.Decide.DailyUSD.LLM = 0.0000001 }, map[string]float64{"role_fit": 0.3}, nil)
	if err := rig.r.Advance(context.Background(), "gh:acme:1"); err != nil {
		t.Fatalf("a refused escalation failed the posting: %v", err)
	}
	if n := len(*rig.claudeBody); n != 0 {
		t.Fatalf("%d request(s) sent past the cap", n)
	}
	rec, _ := rig.l.Get("gh:acme:1")
	ann := rec.Annotations
	if ann["triage.role_fit"] != "strong_fit" || ann["triage.role_fit.p"] != "0.3000" ||
		ann["triage.role_fit.shadow_reading"] != "unavailable" || ann["triage.role_fit.shadow_backend"] != "escalate" {
		t.Fatalf("annotations %v", ann)
	}
	if _, has := ann["triage.role_fit.disagree"]; has {
		t.Fatalf("an outage is not a disagreement: %v", ann)
	}
	if st := rig.w.runReport().BackendStats["escalate"]; st.Calls != 1 || st.Errors != 1 {
		t.Fatalf("escalate stats %+v", st)
	}
	if rep := rig.w.runReport().BackendErrors; rep == nil || rep.Causes[0].Backend != "escalate" {
		t.Fatalf("backend errors %+v", rep)
	}
}

// Only the triage slots escalate: `vani eval` measures those and no other, so
// a low-confidence job category stays the primary's reading, asks Claude
// nothing and spends none of max_per_run.
func TestWiredEscalationLeavesTheCategorySlotAlone(t *testing.T) {
	rig := newEscalationRig(t, nil, map[string]float64{jobcategory.Slot: 0.3}, nil)
	if err := rig.r.Advance(context.Background(), "gh:acme:1"); err != nil {
		t.Fatal(err)
	}
	if n := len(*rig.claudeBody); n != 0 {
		t.Fatalf("%d Claude request(s) for a low-confidence category", n)
	}
	rec, _ := rig.l.Get("gh:acme:1")
	for k := range rec.Annotations {
		if strings.Contains(k, "shadow") || strings.HasSuffix(k, ".disagree") {
			t.Errorf("unexpected %s", k)
		}
	}
	if got := rec.Annotations[ledger.AnnTriagePrefix+jobcategory.Slot]; got != jobcategory.OptionValues()[0] {
		t.Fatalf("category reading %q, want the primary's %q", got, jobcategory.OptionValues()[0])
	}
}

// With escalation off the same run asks Claude nothing and writes no shadow.
func TestWiredRunWithoutEscalationAsksClaudeNothing(t *testing.T) {
	rig := newEscalationRig(t, func(c *config.Config) { c.Decide.Escalate = nil }, map[string]float64{"role_fit": 0.3}, nil)
	if err := rig.r.Advance(context.Background(), "gh:acme:1"); err != nil {
		t.Fatal(err)
	}
	rec, _ := rig.l.Get("gh:acme:1")
	for k := range rec.Annotations {
		if strings.Contains(k, "shadow") {
			t.Errorf("unexpected %s", k)
		}
	}
	if n := len(*rig.claudeBody); n != 0 {
		t.Fatalf("%d Claude request(s)", n)
	}
}
