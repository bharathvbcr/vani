package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
	"github.com/bharathvbcr/Vani/internal/config"
)

// The default config's llm is Claude through Manvi (the owner's choice,
// 2026-10-20) at Manvi's published rates for the model, and it builds a
// metered provider from a key alone: a blank ANTHROPIC_WORKSPACE_ID is no
// workspace, not an error (the Mac app's anthropic-account names one).
func TestDefaultLLMIsClaudeAtManviRates(t *testing.T) {
	l := config.Default().LLM
	if l.Provider != "anthropic" || l.Model == "" {
		t.Fatalf("default llm %+v, want anthropic", l)
	}
	p, ok := anthropic.PriceOf(l.Model)
	if !ok {
		t.Fatalf("Manvi has no price for the default model %q", l.Model)
	}
	if l.InputNanoUSDPerToken != p.Rates.Input || l.OutputNanoUSDPerToken != p.Rates.Output {
		t.Fatalf("default prices %d/%d, Manvi's %d/%d", l.InputNanoUSDPerToken, l.OutputNanoUSDPerToken, p.Rates.Input, p.Rates.Output)
	}
	cfg := config.Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config does not validate: %v", err)
	}

	clearKeys(t)
	t.Setenv("ANTHROPIC_WORKSPACE_ID", "")
	cfg.Decide.OpenRouterModel = ""
	w := &wired{cfg: cfg, stateDir: t.TempDir(), warn: &bytes.Buffer{}}
	if _, err := w.provider(context.Background()); err == nil {
		t.Fatal("the provider built with no Anthropic key")
	}
	t.Setenv("ANTHROPIC_API_KEY", "test-not-a-real-key")
	if _, err := w.provider(context.Background()); err != nil {
		t.Fatalf("provider: %v", err)
	}
}
