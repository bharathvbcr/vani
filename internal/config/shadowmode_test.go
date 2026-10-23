package config

import (
	"strings"
	"testing"
)

func localShadowCfg() Config {
	c := valid()
	c.Decide.Shadow = "local"
	c.Decide.Local = &Local{Endpoint: "http://127.0.0.1:8080", Model: "d3-mini"}
	return c
}

func TestShadowModeValidation(t *testing.T) {
	llmCfg := func(c *Config) {
		c.LLM = LLM{Provider: "anthropic", Model: "claude-opus-5-5", TriageModel: "claude-haiku-5-5", InputNanoUSDPerToken: 4000, OutputNanoUSDPerToken: 20000}
	}
	for name, tc := range map[string]struct {
		mut  func(*Config)
		want string // "" valid
	}{
		"sample is the default": {func(c *Config) { c.Decide.ShadowMode = "sample" }, ""},
		"drop candidates":       {func(c *Config) { c.Decide.ShadowMode = ShadowModeDropCandidates }, ""},
		"unknown mode":          {func(c *Config) { c.Decide.ShadowMode = "all" }, "decide.shadow_mode"},
		"drop candidates no shadow": {func(c *Config) {
			c.Decide.Shadow, c.Decide.Local = "", nil
			c.Decide.ShadowMode = ShadowModeDropCandidates
		}, "needs decide.shadow"},
		"drop candidates with every": {func(c *Config) { c.Decide.ShadowMode = ShadowModeDropCandidates; c.Decide.ShadowEvery = 4 }, "shadow_every does not apply"},
		"escalate coexists": {func(c *Config) {
			llmCfg(c)
			c.Decide.ShadowMode = ShadowModeDropCandidates
			c.Decide.Escalate = &Escalate{Enabled: true}
		}, ""},
		"escalate overlaps a drop rule": {func(c *Config) {
			llmCfg(c)
			c.Decide.ShadowMode = ShadowModeDropCandidates
			c.DropRules = []DropRule{{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinOpenRouterProb: 0.4}}
			c.Decide.Escalate = &Escalate{Enabled: true}
		}, "claim the same slots"},
		"llm shadow on drop candidates, cheap triage model": {func(c *Config) {
			llmCfg(c)
			c.Decide.Local, c.Decide.Shadow = nil, "llm"
			c.Decide.ShadowMode = ShadowModeDropCandidates
		}, ""},
		"llm shadow on drop candidates would use llm.model": {func(c *Config) {
			llmCfg(c)
			c.LLM.TriageModel = ""
			c.Decide.Local, c.Decide.Shadow = nil, "llm"
			c.Decide.ShadowMode = ShadowModeDropCandidates
		}, "needs llm.triage_model"},
		"llm shadow on drop candidates, triage model is llm.model": {func(c *Config) {
			llmCfg(c)
			c.LLM.TriageModel = c.LLM.Model
			c.Decide.Local, c.Decide.Shadow = nil, "llm"
			c.Decide.ShadowMode = ShadowModeDropCandidates
		}, "needs llm.triage_model"},
		"escalate with sampled shadow stays exclusive": {func(c *Config) {
			llmCfg(c)
			c.Decide.Escalate = &Escalate{Enabled: true}
		}, "exclusive"},
	} {
		t.Run(name, func(t *testing.T) {
			c := localShadowCfg()
			tc.mut(&c)
			err := c.Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("valid config refused: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("error %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
