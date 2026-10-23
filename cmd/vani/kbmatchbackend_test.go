package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
	"github.com/bharathvbcr/decide"
)

var typicalCost = regexp.MustCompile(`typically about \$([0-9.]+), at most \$([0-9.]+)`)

func costsOf(t *testing.T, out string) (typical, atMost float64) {
	t.Helper()
	m := typicalCost.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no cost line in:\n%s", out)
	}
	typical, _ = strconv.ParseFloat(m[1], 64)
	atMost, _ = strconv.ParseFloat(m[2], 64)
	return typical, atMost
}

// A cheaper model is priced at its own rates, not at the configured model's, so
// the estimate (and the daily cap, which meters the same way) follows the choice.
func TestKBMatchPricesTheModelItWillAsk(t *testing.T) {
	k := twoPostings(t)
	k.cfg.LLM = config.LLM{Provider: "anthropic", Model: "claude-opus-5-5", InputNanoUSDPerToken: 4000, OutputNanoUSDPerToken: 20000}
	k.save(t)
	opus, _ := mustCode(t, 0, "kbmatch", "run", "--dry-run")
	if !strings.Contains(opus, "the configured llm prices") || !strings.Contains(opus, "anthropic model claude-opus-5-5") {
		t.Fatalf("default model:\n%s", opus)
	}
	typOpus, maxOpus := costsOf(t, opus)

	k.cfg.KBMatch.Model = "claude-haiku-5-5"
	k.save(t)
	haiku, _ := mustCode(t, 0, "kbmatch", "run", "--dry-run")
	if !strings.Contains(haiku, "Anthropic's published rates for claude-haiku-5-5") || !strings.Contains(haiku, "anthropic model claude-haiku-5-5") {
		t.Fatalf("kbmatch.model:\n%s", haiku)
	}
	typHaiku, maxHaiku := costsOf(t, haiku)
	if !(typHaiku*10 < typOpus && maxHaiku*10 < maxOpus) {
		t.Errorf("haiku %.4f/%.4f should cost under a tenth of opus %.4f/%.4f", typHaiku, maxHaiku, typOpus, maxOpus)
	}

	// --model wins over the config, and a model the table does not know is priced
	// at the configured rates (the conservative side), not at zero.
	sonnet, _ := mustCode(t, 0, "kbmatch", "run", "--dry-run", "--model", "claude-sonnet-5-5")
	if !strings.Contains(sonnet, "Anthropic's published rates for claude-sonnet-5-5") {
		t.Errorf("--model:\n%s", sonnet)
	}
	if typSonnet, _ := costsOf(t, sonnet); !(typSonnet < typOpus && typSonnet > typHaiku) {
		t.Errorf("sonnet %.4f should sit between haiku %.4f and opus %.4f", typSonnet, typHaiku, typOpus)
	}
	unknown, _ := mustCode(t, 0, "kbmatch", "run", "--dry-run", "--model", "claude-not-a-model")
	if !strings.Contains(unknown, "the configured llm prices") {
		t.Errorf("unknown model:\n%s", unknown)
	}
}

// kbmatch.model is its own setting: it never changes llm.triage_model, which the
// triage and escalation backends read.
func TestKBMatchModelDoesNotMoveTriage(t *testing.T) {
	k := twoPostings(t)
	k.cfg.LLM = config.LLM{Provider: "anthropic", Model: "claude-opus-5-5", TriageModel: "claude-sonnet-5-5", InputNanoUSDPerToken: 4000, OutputNanoUSDPerToken: 20000}
	k.cfg.KBMatch.Model = "claude-haiku-5-5"
	k.save(t)
	cfg, err := config.Load(k.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.EffectiveTriageModel() != "claude-sonnet-5-5" || cfg.KBMatchModel() != "claude-haiku-5-5" {
		t.Errorf("triage %q, kbmatch %q", cfg.LLM.EffectiveTriageModel(), cfg.KBMatchModel())
	}
	k.cfg.KBMatch.Model = ""
	k.save(t)
	if cfg, _ = config.Load(k.cfgPath); cfg.KBMatchModel() != "claude-sonnet-5-5" {
		t.Errorf("with no kbmatch.model the llm.triage_model answers, got %q", cfg.KBMatchModel())
	}
}

func TestKBMatchFlagsAreChecked(t *testing.T) {
	k := twoPostings(t)
	useFake(t, &fakeKB{answer: byTitle(nil)})
	k.cfg.Decide.OpenRouterModel = ""
	k.save(t)
	if code, _, _ := do(t, "kbmatch", "run", "--dry-run", "--backend", "gpt"); code != exitUsage {
		t.Errorf("--backend gpt: exit %d, want usage", code)
	}
	for _, b := range []string{"JEV", " jev", "jev-1.13", "Openrouter"} {
		if code, _, _ := do(t, "kbmatch", "run", "--dry-run", "--backend", b); code != exitUsage {
			t.Errorf("--backend %q: exit %d, want usage", b, code)
		}
	}
	for _, b := range []string{config.KBMatchOpenRouter, "jev"} {
		if code, _, e := do(t, "kbmatch", "run", "--dry-run", "--backend", b); code != exitRuntime || !strings.Contains(e, `kbmatch.backend "openrouter" needs decide.openrouter_model`) {
			t.Errorf("--backend %s without openrouter: exit %d, %s", b, code, e)
		}
	}
	k.cfg.LLM = config.LLM{}
	k.cfg.Decide.DailyUSD.LLM = 0
	k.save(t)
	if code, _, e := do(t, "kbmatch", "run", "--dry-run"); code != exitRuntime || !strings.Contains(e, "--backend openrouter") {
		t.Errorf("no llm: exit %d, %s (should point at --backend openrouter)", code, e)
	}
}

// The decision model answers over the wire with the catalog of option
// descriptions and the question text, sees the posting but no employer, and its
// calibrated probability is kept.
//
// kbmatch.backend was "jev" until 2026-10-20: a config file or --backend flag
// that still says so asks the same model, and new answers record "openrouter".
func TestKBMatchAsksTheDecisionModel(t *testing.T) {
	for _, tc := range []struct {
		name, configured string
		flags            []string
	}{
		{"openrouter", config.KBMatchOpenRouter, nil},
		{"legacy jev config", "jev", nil},
		{"legacy jev flag", "", []string{"--backend", "jev"}},
		{"openrouter flag", "", []string{"--backend", config.KBMatchOpenRouter}},
	} {
		t.Run(tc.name, func(t *testing.T) { kbmatchAsksTheDecisionModel(t, tc.configured, tc.flags) })
	}
}

func kbmatchAsksTheDecisionModel(t *testing.T, configured string, flags []string) {
	k := twoPostings(t)
	clearKeys(t)
	t.Setenv("OPENROUTER_API_KEY", "test-not-a-real-key")
	k.cfg.LLM = config.LLM{}
	k.cfg.Decide.DailyUSD.LLM = 0
	k.cfg.Decide.OpenRouterModel = "liquid/d1-ultra"
	k.cfg.Decide.DailyUSD.OpenRouter = 5
	k.cfg.KBMatch.Backend = configured
	k.save(t)
	if raw, err := os.ReadFile(k.cfgPath); err != nil || (configured == "jev") != strings.Contains(string(raw), `"backend": "jev"`) {
		t.Fatalf("config file should say backend %q: %v\n%s", configured, err, raw)
	}

	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		choice := "unrelated"
		if strings.Contains(string(b), "Inference Engineer") {
			choice = "core"
		}
		_, _ = io.WriteString(w, `{"answers":{"kb_match":{"choice":"`+choice+`","confidence":0.9}}}`)
	}))
	defer srv.Close()
	oldE, oldD, oldF := kbmatchEndpoint, kbmatchDoer, kbmatchFocus
	t.Cleanup(func() { kbmatchEndpoint, kbmatchDoer, kbmatchFocus = oldE, oldD, oldF })
	kbmatchEndpoint = srv.URL
	kbmatchDoer = func(config.Config) (egress.Doer, error) { return egress.NewLoopback(5*time.Second, 0) }

	o, _ := mustCode(t, 0, append([]string{"kbmatch", "run", "--dry-run"}, flags...)...)
	if !strings.Contains(o, "openrouter.ai decision model liquid/d1-ultra") || strings.Contains(o, "typically about") {
		t.Errorf("dry run for the decision model:\n%s", o)
	}
	o, e := mustCode(t, 0, append([]string{"kbmatch", "run"}, flags...)...)
	if !strings.Contains(o, "asked 2, answered 2 (core 1, partial 0, adjacent 0, unrelated 1)") || len(bodies) != 2 {
		t.Fatalf("run:\n%s\n%d requests", o, len(bodies))
	}
	if warned := strings.Contains(e, `kbmatch.backend "jev" is now "openrouter"`); warned != (configured == "jev") {
		t.Errorf("deprecation warning %v for backend %q:\n%s", warned, configured, e)
	}
	var sent struct {
		Model     string                `json:"model"`
		State     struct{ Text string } `json:"state"`
		Questions map[string]struct {
			Criteria     map[string]string `json:"criteria"`
			Instructions string            `json:"instructions"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(bodies[0], &sent); err != nil {
		t.Fatal(err)
	}
	q := sent.Questions[kbmatch.Slot]
	if sent.Model != "liquid/d1-ultra" || q.Instructions != kbmatch.Prompt || len(q.Criteria) != 4 || q.Criteria["adjacent"] != kbmatch.Descriptions()[kbmatch.Slot]["adjacent"] {
		t.Errorf("request: model %q, %d criteria, instructions match %v", sent.Model, len(q.Criteria), q.Instructions == kbmatch.Prompt)
	}
	for _, secret := range []string{"SecretCo", "OtherCo", "SyntheticCo"} {
		if strings.Contains(string(bodies[0]), secret) || strings.Contains(string(bodies[1]), secret) {
			t.Errorf("%q reached the decision model", secret)
		}
	}
	st, err := kbmatch.OpenStore(filepath.Join(k.state, kbmatch.FileName))
	if err != nil || st.Len() != 2 {
		t.Fatalf("store: %v", err)
	}
	cfg, _ := config.Load(k.cfgPath)
	_, _, digest, summary, err := (&env{configPath: k.cfgPath, stateDir: k.state, stderr: io.Discard}).kbmatchSetup()
	_ = cfg
	if err != nil {
		t.Fatal(err)
	}
	snapAnswers := 0
	for _, key := range []string{"gh:a:1", "gh:a:2"} {
		recs := map[string]posting.Posting{
			"gh:a:1": {Title: "Inference Engineer", Description: longDesc, Key: "gh:a:1", ATS: "gh", Company: "SecretCo"},
			"gh:a:2": {Title: "Marketing Lead", Description: longDesc, Key: "gh:a:2", ATS: "gh", Company: "OtherCo"},
		}
		if e, ok := st.Lookup(key, digest.SHA256, kbmatch.TextSHA256(recs[key], summary, digest)); ok {
			snapAnswers++
			if e.Backend != config.KBMatchOpenRouter || e.Model != "liquid/d1-ultra" || e.P != 0.9 || e.Mode != kbmatch.ModeFull {
				t.Errorf("%s: entry %+v", key, e)
			}
		}
	}
	if snapAnswers != 2 {
		t.Errorf("found %d current answers, want 2", snapAnswers)
	}
}

// The embedding model's picks ride along when it is available, and a run goes on
// with the full digest when it is not.
func TestKBMatchUsesTheEmbeddingModelForEvidence(t *testing.T) {
	k := twoPostings(t)
	f := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "adjacent"})}
	useFake(t, f)
	k.cfg.Semantic.Snapshot = filepath.Join(filepath.Dir(k.cfgPath), "snapshot")
	k.save(t)
	asked := 0
	kbmatchFocus = func(_ context.Context, _ config.Config, _ string, prof semantic.Profile) (func(context.Context, posting.Posting) ([]kbmatch.Focus, error), func(), error) {
		if len(prof.Chunks) == 0 {
			t.Errorf("the focus was given no profile")
		}
		return func(_ context.Context, p posting.Posting) ([]kbmatch.Focus, error) {
			asked++
			return []kbmatch.Focus{{Label: "LLM Inference Runtime", Text: "Built a request batching scheduler in Rust"}}, nil
		}, func() {}, nil
	}
	o, _ := mustCode(t, 0, "kbmatch", "run", "--dry-run")
	if !strings.Contains(o, "compact digest of your knowledge base plus up to 8 lines of it that the embedding model finds closest") {
		t.Errorf("dry run does not say the embedding model is used:\n%s", o)
	}
	o, _ = mustCode(t, 0, "kbmatch", "run")
	if !strings.Contains(o, "2 of the questions carried embedding-picked evidence") || asked != 2 {
		t.Errorf("run:\n%s\n(asked %d)", o, asked)
	}
	for _, c := range f.contexts {
		if !strings.Contains(c, "CLOSEST TO THIS POSTING") || !strings.Contains(c, "Built a request batching scheduler in Rust") {
			t.Errorf("a question lacks the picked evidence:\n%s", c)
		}
	}
	st, _ := kbmatch.OpenStore(filepath.Join(k.state, kbmatch.FileName))
	_, _, digest, summary, _ := (&env{configPath: k.cfgPath, stateDir: k.state, stderr: io.Discard}).kbmatchSetup()
	p := posting.Posting{Key: "gh:a:1", ATS: "gh", Company: "SecretCo", Title: "Inference Engineer", Description: longDesc}
	if e, ok := st.Lookup("gh:a:1", digest.SHA256, kbmatch.TextSHA256(p, summary, digest)); !ok || e.Mode != kbmatch.ModeFocused {
		t.Errorf("entry %+v (found %v), want mode focused", e, ok)
	}

	// --no-retrieve never opens the embedding model.
	k2 := twoPostings(t)
	k2.cfg.Semantic.Snapshot = filepath.Join(filepath.Dir(k2.cfgPath), "snapshot")
	k2.save(t)
	f2 := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core"})}
	useFake(t, f2)
	kbmatchFocus = func(context.Context, config.Config, string, semantic.Profile) (func(context.Context, posting.Posting) ([]kbmatch.Focus, error), func(), error) {
		t.Fatal("--no-retrieve opened the embedding model")
		return nil, nil, nil
	}
	mustCode(t, 0, "kbmatch", "run", "--no-retrieve")
	for _, c := range f2.contexts {
		if strings.Contains(c, "CLOSEST TO THIS POSTING") {
			t.Errorf("--no-retrieve still sent embedding-picked evidence")
		}
	}
}

func TestKBMatchFallsBackToTheFullDigestWhenTheEmbeddingModelFails(t *testing.T) {
	k := twoPostings(t)
	f := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "partial"})}
	useFake(t, f)
	k.cfg.Semantic.Snapshot = filepath.Join(filepath.Dir(k.cfgPath), "snapshot")
	k.save(t)
	// The model cannot be opened at all.
	_, e := mustCode(t, 0, "kbmatch", "run")
	if !strings.Contains(e, "the embedding model could not pick evidence") || len(f.contexts) != 2 {
		t.Fatalf("stderr %q after %d questions", e, len(f.contexts))
	}
	// It opens but fails on a posting: that question carries the full digest.
	f.contexts = nil
	k2 := twoPostings(t)
	k2.cfg.Semantic.Snapshot = filepath.Join(filepath.Dir(k2.cfgPath), "snapshot")
	k2.save(t)
	kbmatchFocus = func(context.Context, config.Config, string, semantic.Profile) (func(context.Context, posting.Posting) ([]kbmatch.Focus, error), func(), error) {
		return func(context.Context, posting.Posting) ([]kbmatch.Focus, error) { return nil, io.ErrUnexpectedEOF }, func() {}, nil
	}
	o, _ := mustCode(t, 0, "kbmatch", "run")
	if !strings.Contains(o, "answered 2") || !strings.Contains(o, "0 of the questions carried embedding-picked evidence") {
		t.Errorf("run:\n%s", o)
	}
	for _, c := range f.contexts {
		if !strings.Contains(c, "Cut decode latency") {
			t.Errorf("a fallback question lacks the full digest")
		}
	}
	var _ decide.Decider = f
}
