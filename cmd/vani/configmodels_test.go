package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
	"github.com/bharathvbcr/Manvi/manvi/llm/gemini"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/vault"
)

func modelsOut(t *testing.T, stdout string) modelsReply {
	t.Helper()
	var r modelsReply
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout is not one JSON reply: %q (%v)", stdout, err)
	}
	return r
}

func TestConfigModelsReadsAndSwitches(t *testing.T) {
	cfgPath, _ := setup(t)
	initWithLLM(t, cfgPath)

	r := modelsOut(t, func() string { o, _ := mustCode(t, 0, "config", "models"); return o }())
	if r.Decision.Provider != config.DecisionOpenRouter || len(r.Decision.Choices) != 3 || len(r.LLM.Choices) != 2 || r.Error != "" {
		t.Fatalf("read = %+v", r)
	}
	for _, c := range r.Decision.Choices {
		if c.KeychainService == "" || c.Host == "" || c.Model == "" || len(c.KeyEnvs) == 0 {
			t.Errorf("choice %+v is incomplete", c)
		}
	}

	o, _ := mustCode(t, 0, "config", "models", "--set", "--decision", "subq", "--llm", "gemini", "--model", "gemini-4-hydrogen")
	r = modelsOut(t, o)
	if r.Decision.Provider != "subq" || r.Decision.Model != "subq-snap-1.2" || r.LLM.Provider != "gemini" || r.LLM.Model != "gemini-4-hydrogen" || r.LLM.TriageModel != "gemini-4-hydrogen" {
		t.Fatalf("after set = %+v", r)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Decide.Provider != "subq" || cfg.LLM.InputNanoUSDPerToken != 1500 || cfg.LLM.OutputNanoUSDPerToken != 7500 {
		t.Errorf("saved = %+v %+v", cfg.Decide, cfg.LLM)
	}

	// A refused selection is a JSON failure and leaves the file as it was.
	before, _ := os.ReadFile(cfgPath)
	o, _ = mustCode(t, exitRuntime, "config", "models", "--set", "--llm", "anthropic", "--model", "gemini-4-hydrogen")
	if r := modelsOut(t, o); r.Error == "" || !strings.Contains(r.Error, "anthropic") {
		t.Errorf("refusal = %+v", r)
	}
	if after, _ := os.ReadFile(cfgPath); !bytes.Equal(before, after) {
		t.Error("a refused selection changed the config")
	}
	// Selection flags without --set are a usage error, not a silent read.
	if code, _, _ := do(t, "config", "models", "--decision", "gemini"); code != exitUsage {
		t.Errorf("flags without --set: code %d", code)
	}
}

// The gemini llm provider takes the AI Studio key from the login Keychain
// when neither variable is set, and from nowhere when VANI_NO_KEYCHAIN is.
func TestGeminiProviderReadsTheAIStudioKeychainItem(t *testing.T) {
	clearKeys(t)
	cfg := defaultWithLLM()
	cfg.Decide.OpenRouterModel = ""
	var asked []string
	old := keychainRead
	keychainRead = func(_ context.Context, s string) (string, error) {
		asked = append(asked, s)
		return "test-not-a-real-key", nil
	}
	t.Cleanup(func() { keychainRead = old })

	t.Setenv(noKeychainEnv, "1")
	w := &wired{cfg: cfg, stateDir: t.TempDir(), warn: &bytes.Buffer{}}
	if _, err := w.provider(context.Background()); err == nil || !strings.Contains(err.Error(), noKeychainEnv) || len(asked) != 0 {
		t.Fatalf("opted out: %v asked=%v", err, asked)
	}

	t.Setenv(noKeychainEnv, "")
	w = &wired{cfg: cfg, stateDir: t.TempDir(), warn: &bytes.Buffer{}}
	if _, err := w.provider(context.Background()); err != nil {
		t.Fatalf("keychain: %v", err)
	}
	if len(asked) != 1 || asked[0] != vault.GeminiService {
		t.Errorf("asked %v", asked)
	}

	// Anthropic's key is never looked up this way.
	cfg.LLM = config.LLM{Provider: "anthropic", Model: "claude-opus-5-5", InputNanoUSDPerToken: 4000, OutputNanoUSDPerToken: 20000}
	asked = nil
	w = &wired{cfg: cfg, stateDir: t.TempDir(), warn: &bytes.Buffer{}}
	if _, err := w.provider(context.Background()); err == nil || len(asked) != 0 {
		t.Errorf("anthropic: %v asked=%v", err, asked)
	}
}

// Every model the switch offers is one the linked Manvi adapter serves: a model
// it does not know is refused when the provider is built, which fails a run
// that advances. gemini-4-hydrogen needs Manvi with feat/gemini-4-hydrogen
// (e599aaa); against an older Manvi this test is the alarm.
func TestEveryOfferedModelIsServedByTheLinkedManvi(t *testing.T) {
	for _, p := range config.LLMProviders() {
		for _, m := range p.Models {
			var ok bool
			switch p.ID {
			case "anthropic":
				_, ok = anthropic.Capability(m.ID)
			case "gemini":
				_, ok = gemini.Capability(m.ID)
			default:
				t.Fatalf("no adapter check for provider %q", p.ID)
			}
			if !ok {
				t.Errorf("%s/%s is offered but the linked Manvi adapter does not serve it", p.ID, m.ID)
			}
		}
	}
}
