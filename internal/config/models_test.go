package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// liveShape is the owner's llm block as it stood on 2026-11-17: Claude through
// Manvi, Opus drafting and Sonnet triaging, at Opus's prices.
func liveShape(t *testing.T) Config {
	t.Helper()
	c := withProvider(t, "")
	c.LLM = LLM{Provider: "anthropic", Model: "claude-opus-5-5", TriageModel: "claude-sonnet-5-5",
		InputNanoUSDPerToken: 4000, OutputNanoUSDPerToken: 20000}
	if err := c.Validate(); err != nil {
		t.Fatalf("starting config: %v", err)
	}
	return c
}

func TestSwitchToAIStudioSetsModelsAndPricesTogether(t *testing.T) {
	c := liveShape(t)
	if err := c.ApplyModelSelection(ModelSelection{LLMProvider: "gemini", Model: "gemini-4-hydrogen"}); err != nil {
		t.Fatal(err)
	}
	want := LLM{Provider: "gemini", Model: "gemini-4-hydrogen", InputNanoUSDPerToken: 1500, OutputNanoUSDPerToken: 7500}
	if c.LLM != want {
		t.Errorf("llm = %+v, want %+v", c.LLM, want)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("after switch: %v", err)
	}
	// And back: Claude's prices come back with Claude's models.
	if err := c.ApplyModelSelection(ModelSelection{LLMProvider: "anthropic", Model: "claude-opus-5-5", TriageModel: "claude-sonnet-5-5"}); err != nil {
		t.Fatal(err)
	}
	if c.LLM != liveShape(t).LLM {
		t.Errorf("back to Claude: %+v", c.LLM)
	}
}

func TestSwitchMetersAtTheDearerOfTheTwoModels(t *testing.T) {
	c := liveShape(t)
	if err := c.ApplyModelSelection(ModelSelection{LLMProvider: "anthropic", Model: "claude-haiku-5-5", TriageModel: "claude-opus-5-5"}); err != nil {
		t.Fatal(err)
	}
	if c.LLM.InputNanoUSDPerToken != 4000 || c.LLM.OutputNanoUSDPerToken != 20000 {
		t.Errorf("prices = %d/%d", c.LLM.InputNanoUSDPerToken, c.LLM.OutputNanoUSDPerToken)
	}
}

func TestSwitchRefusesAModelOfAnotherProvider(t *testing.T) {
	for _, s := range []ModelSelection{
		{LLMProvider: "gemini", Model: "claude-opus-5-5"},
		{LLMProvider: "gemini", Model: "gemini-4-hydrogen", TriageModel: "claude-sonnet-5-5"},
		{LLMProvider: "anthropic", Model: "gemini-4-hydrogen"},
		{LLMProvider: "anthropic", Model: ""},
		{LLMProvider: "openai", Model: "gpt"},
		{Model: "claude-opus-5-5"},
		{DecisionProvider: "anthropic"},
		{},
	} {
		c := liveShape(t)
		before := c.LLM
		if err := c.ApplyModelSelection(s); err == nil {
			t.Errorf("%+v accepted", s)
		}
		if c.LLM != before {
			t.Errorf("%+v changed llm to %+v", s, c.LLM)
		}
	}
	c := liveShape(t)
	if err := c.ApplyModelSelection(ModelSelection{}); !errors.Is(err, ErrNoSelection) {
		t.Errorf("empty: %v", err)
	}
}

// A config edited by hand into a cross-provider pair fails validation, before
// any run builds the provider.
func TestValidateRefusesCrossProviderModels(t *testing.T) {
	c := liveShape(t)
	c.LLM.Provider = "gemini"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), `llm.model "claude-opus-5-5" belongs to the anthropic provider, but llm.provider is gemini`) ||
		!strings.Contains(err.Error(), "llm.triage_model") {
		t.Errorf("validate: %v", err)
	}
}

func TestDecisionSwitchAddsTheHostAndKeepsTheOthers(t *testing.T) {
	c := liveShape(t)
	if err := c.ApplyModelSelection(ModelSelection{DecisionProvider: DecisionGemini}); err != nil {
		t.Fatal(err)
	}
	if c.Decide.Provider != DecisionGemini || !slices.Contains(c.AllowedHosts, "generativelanguage.googleapis.com") || c.Decide.OpenRouterModel != "liquid/d1-ultra" {
		t.Errorf("decide = %+v hosts = %v", c.Decide, c.AllowedHosts)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("after switch: %v", err)
	}
	n := len(c.AllowedHosts)
	if err := c.ApplyModelSelection(ModelSelection{DecisionProvider: DecisionGemini}); err != nil || len(c.AllowedHosts) != n {
		t.Errorf("the host was added twice: %v", c.AllowedHosts)
	}
	// Back to Liquid writes the default provider, which a file stores as no key.
	if err := c.ApplyModelSelection(ModelSelection{DecisionProvider: DecisionOpenRouter}); err != nil || c.Decide.Provider != "" {
		t.Errorf("back to openrouter: %v %q", err, c.Decide.Provider)
	}
	// Choosing Liquid when it was off turns it on with the default model.
	c.Decide.OpenRouterModel, c.Decide.OpenRouterKeyEnv = "", ""
	if err := c.ApplyModelSelection(ModelSelection{DecisionProvider: DecisionOpenRouter}); err != nil ||
		c.Decide.OpenRouterModel != "liquid/d1.2-ultra" || c.Decide.OpenRouterKeyEnv != "OPENROUTER_API_KEY" {
		t.Errorf("liquid from off: %+v", c.Decide)
	}
}

// Through Update, the way the app's switch writes: one locked, validated write.
func TestModelSelectionThroughUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(renamedConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Update(path, func(c *Config) error {
		return c.ApplyModelSelection(ModelSelection{DecisionProvider: DecisionSubQ, LLMProvider: "gemini", Model: "gemini-4-hydrogen"})
	}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil || c.Decide.DecisionModel() != "subq-snap-1.2" || c.LLM.Model != "gemini-4-hydrogen" {
		t.Fatalf("%v %+v %+v", err, c.Decide, c.LLM)
	}
	// A refused selection leaves the file as it was.
	before, _ := os.ReadFile(path)
	if err := Update(path, func(c *Config) error {
		return c.ApplyModelSelection(ModelSelection{LLMProvider: "anthropic", Model: "gemini-4-hydrogen"})
	}); err == nil {
		t.Fatal("refused selection written")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("a refused selection changed the file")
	}
}

func TestEveryOfferedModelIsPricedAndOwnedByItsProvider(t *testing.T) {
	for _, p := range LLMProviders() {
		if len(p.Models) == 0 {
			t.Errorf("%s offers no model", p.ID)
		}
		for _, m := range p.Models {
			if m.InputNanoUSD <= 0 || m.OutputNanoUSD <= 0 || m.InputNanoUSD > MaxNanoUSDPerToken || m.OutputNanoUSD > MaxNanoUSDPerToken {
				t.Errorf("%s/%s prices %d/%d", p.ID, m.ID, m.InputNanoUSD, m.OutputNanoUSD)
			}
			if modelFamilyProvider(m.ID) != p.ID {
				t.Errorf("%s offers %s", p.ID, m.ID)
			}
		}
	}
}
