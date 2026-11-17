package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func withProvider(t *testing.T, provider string) Config {
	t.Helper()
	c, err := Parse([]byte(renamedConfig))
	if err != nil {
		t.Fatal(err)
	}
	c.Decide.Provider = provider
	return c
}

func TestDecisionProviderDefaultsToOpenRouter(t *testing.T) {
	c := withProvider(t, "")
	if c.Decide.ProviderID() != DecisionOpenRouter || c.Decide.DecisionModel() != "liquid/d1-ultra" || c.Decide.DecisionName() != "d1-ultra" {
		t.Errorf("id=%q model=%q name=%q", c.Decide.ProviderID(), c.Decide.DecisionModel(), c.Decide.DecisionName())
	}
	if got := c.Decide.DecisionKeyEnvs(); !slices.Equal(got, []string{"OPENROUTER_API_KEY"}) {
		t.Errorf("key envs = %v", got)
	}
	// An empty openrouter_model still turns the decision model off.
	c.Decide.OpenRouterModel = ""
	if c.DecisionConfigured() {
		t.Error("openrouter with no model reads as configured")
	}
}

func TestSubQAndGeminiUseTheirOwnModelAndKeepOpenRouters(t *testing.T) {
	for _, tc := range []struct {
		provider, model, name, host string
		envs                        []string
	}{
		{DecisionSubQ, "subq-snap-1.2", "subq-snap-1-2", "api.subq.ai", []string{"SUBQ_API_KEY"}},
		{DecisionGemini, "gemini-decision-1", "gemini-decision-1", "generativelanguage.googleapis.com", []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}},
	} {
		c := withProvider(t, tc.provider)
		if !c.DecisionConfigured() || c.Decide.DecisionModel() != tc.model || c.Decide.DecisionName() != tc.name {
			t.Errorf("%s: configured=%v model=%q name=%q", tc.provider, c.DecisionConfigured(), c.Decide.DecisionModel(), c.Decide.DecisionName())
		}
		if !slices.Equal(c.Decide.DecisionKeyEnvs(), tc.envs) {
			t.Errorf("%s: key envs = %v", tc.provider, c.Decide.DecisionKeyEnvs())
		}
		hosts := c.RequiredHosts()
		if !slices.Contains(hosts, tc.host) || slices.Contains(hosts, "openrouter.ai") {
			t.Errorf("%s: required hosts = %v", tc.provider, hosts)
		}
		if c.Decide.OpenRouterModel != "liquid/d1-ultra" {
			t.Errorf("%s: openrouter_model lost", tc.provider)
		}
		// The provider's host must be allowed, like any other.
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "allowed_hosts is missing: "+tc.host) {
			t.Errorf("%s: validate without host = %v", tc.provider, err)
		}
		c.AllowedHosts = append(c.AllowedHosts, tc.host)
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", tc.provider, err)
		}
	}
	c := withProvider(t, DecisionSubQ)
	c.Decide.SubQModel = "subq-snap-latest"
	if c.Decide.DecisionModel() != "subq-snap-latest" || c.Decide.DecisionName() != "subq-snap-latest" {
		t.Errorf("explicit model = %q", c.Decide.DecisionModel())
	}
}

func TestDecisionProviderValidation(t *testing.T) {
	c := withProvider(t, "anthropic")
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), `decide.provider "anthropic"`) {
		t.Errorf("unknown provider: %v", err)
	}
	if c.DecisionConfigured() {
		t.Error("an unknown provider reads as configured")
	}
	for _, bad := range []string{"subq snap", "a\x00b", strings.Repeat("m", MaxModelName+1)} {
		c := withProvider(t, DecisionSubQ)
		c.AllowedHosts = append(c.AllowedHosts, "api.subq.ai")
		c.Decide.SubQModel = bad
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "decide.subq_model") {
			t.Errorf("model %q: %v", bad, err)
		}
	}
	// A model whose backend name is reserved is refused whichever provider names it.
	c = withProvider(t, DecisionGemini)
	c.AllowedHosts = append(c.AllowedHosts, "generativelanguage.googleapis.com")
	c.Decide.GeminiModel = "models/lappi"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "taken by another backend") {
		t.Errorf("reserved name: %v", err)
	}
	// The key env is OpenRouter's setting only.
	c = withProvider(t, DecisionSubQ)
	c.AllowedHosts = append(c.AllowedHosts, "api.subq.ai")
	c.Decide.OpenRouterKeyEnv = ""
	if err := c.Validate(); err != nil {
		t.Errorf("subq refused for a missing openrouter_key_env: %v", err)
	}
}

// A config that never chose a provider is written back without the new keys,
// so a vani binary from before them still reads it (Parse refuses unknown keys).
func TestDefaultProviderWritesNoNewKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(renamedConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Update(path, func(*Config) error { return nil }); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Decide map[string]json.RawMessage `json:"decide"`
	}
	if err := json.Unmarshal(data, &file); err != nil || file.Decide == nil {
		t.Fatalf("rewritten config: %v", err)
	}
	for _, k := range []string{"provider", "subq_model", "gemini_model"} {
		if _, ok := file.Decide[k]; ok {
			t.Errorf("decide block carries %q, which an older vani refuses", k)
		}
	}
	// Choosing a provider writes it.
	if err := Update(path, func(c *Config) error {
		c.Decide.Provider = DecisionSubQ
		c.AllowedHosts = append(c.AllowedHosts, "api.subq.ai")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if c, err := Load(path); err != nil || c.Decide.ProviderID() != DecisionSubQ {
		t.Fatalf("after choosing subq: %v %q", err, c.Decide.Provider)
	}
}

func TestProviderTableIsACopy(t *testing.T) {
	ps := DecisionProviders()
	ps[1].KeyEnvs[0] = "X"
	ps[0].Host = "evil.example"
	if p, _ := LookupDecisionProvider(DecisionSubQ); p.KeyEnvs[0] != "SUBQ_API_KEY" {
		t.Error("the table was changed through a copy")
	}
	if p, _ := LookupDecisionProvider(""); p.Host != "openrouter.ai" {
		t.Error("the table was changed through a copy")
	}
}
