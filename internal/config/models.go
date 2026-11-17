package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// LLMModelChoice is one model the Mac app's switch offers for an llm
// provider, with the per-token prices (nano-USD) vani meters it at.
type LLMModelChoice struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	InputNanoUSD  int64  `json:"input_nano_usd_per_token"`
	OutputNanoUSD int64  `json:"output_nano_usd_per_token"`
}

// LLMProviderChoice is one llm.provider the switch offers.
type LLMProviderChoice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// KeychainService is the login Keychain item the app passes the key from;
	// empty for anthropic, whose key anthropic-account supplies.
	KeychainService string           `json:"keychain_service,omitempty"`
	KeyEnvs         []string         `json:"key_envs,omitempty"`
	Models          []LLMModelChoice `json:"models"`
}

// llmProviders is the switch's table. Prices are list prices, in nano-USD
// per token ($1 per million tokens = 1000):
//   - Claude: Anthropic's model table (claude-api skill, cached 2026-11-03).
//     Haiku 5.5 is $0.10/$0.50 up to 100K-token prompts and $0.50/$2.50
//     beyond; the higher rate is used, since over-stating only refuses early.
//   - Gemini 4 Hydrogen: $1.50/$7.50 paid tier, thinking included
//     (aistudio.google.com/docs/pricing, read 2026-11-17), which are also the
//     conservative Gemini defaults (GeminiInputNanoUSD).
var llmProviders = []LLMProviderChoice{
	{ID: "anthropic", Label: "Claude (Manvi)", Models: []LLMModelChoice{
		{ID: "claude-opus-5-5", Label: "Claude Opus 5.5", InputNanoUSD: 4000, OutputNanoUSD: 20000},
		{ID: "claude-sonnet-5-5", Label: "Claude Sonnet 5.5", InputNanoUSD: 2000, OutputNanoUSD: 10000},
		{ID: "claude-haiku-5-5", Label: "Claude Haiku 5.5", InputNanoUSD: 500, OutputNanoUSD: 2500},
	}},
	{ID: "gemini", Label: "Google AI Studio", KeychainService: KeychainGemini, KeyEnvs: GeminiKeyEnvs, Models: []LLMModelChoice{
		{ID: "gemini-4-hydrogen", Label: "Gemini 4 Hydrogen", InputNanoUSD: 1500, OutputNanoUSD: 7500},
	}},
}

// LLMProviders is a copy of the switch's table.
func LLMProviders() []LLMProviderChoice {
	out := slices.Clone(llmProviders)
	for i := range out {
		out[i].KeyEnvs = slices.Clone(out[i].KeyEnvs)
		out[i].Models = slices.Clone(out[i].Models)
	}
	return out
}

func lookupLLMProvider(id string) (LLMProviderChoice, bool) {
	for _, p := range llmProviders {
		if p.ID == id {
			return p, true
		}
	}
	return LLMProviderChoice{}, false
}

func (p LLMProviderChoice) model(id string) (LLMModelChoice, bool) {
	for _, m := range p.Models {
		if m.ID == id {
			return m, true
		}
	}
	return LLMModelChoice{}, false
}

// ModelSelection is what the Mac app's model switch sets (`vani config models
// --set`). An empty field leaves that part of the config as it is.
type ModelSelection struct {
	// DecisionProvider is a DecisionProviders id.
	DecisionProvider string
	// LLMProvider is an LLMProviders id; Model and TriageModel are models of
	// that provider (TriageModel empty means Model). Setting a provider sets
	// all three and both prices together, so no model is left metered at
	// another provider's prices or named under the wrong provider.
	LLMProvider string
	Model       string
	TriageModel string
}

// ErrNoSelection is a ModelSelection that names nothing to change.
var ErrNoSelection = errors.New("config models: nothing to set")

// ApplyModelSelection applies s to c. The caller validates the result
// (Update does). A decision provider's host is added to allowed_hosts, so
// choosing it is one step; its other settings, and the other providers',
// are kept.
func (c *Config) ApplyModelSelection(s ModelSelection) error {
	if s.DecisionProvider == "" && s.LLMProvider == "" {
		return ErrNoSelection
	}
	if s.DecisionProvider != "" {
		p, ok := LookupDecisionProvider(s.DecisionProvider)
		if !ok {
			return fmt.Errorf("config models: decision provider %q is not one of %s", s.DecisionProvider, decisionIDs())
		}
		c.Decide.Provider = p.ID
		if p.ID == DecisionOpenRouter {
			// The default writes no key, so the file stays readable by an
			// older vani.
			c.Decide.Provider = ""
			if c.Decide.OpenRouterModel == "" {
				c.Decide.OpenRouterModel = p.DefaultModel
			}
			if c.Decide.OpenRouterKeyEnv == "" {
				c.Decide.OpenRouterKeyEnv = "OPENROUTER_API_KEY"
			}
		}
		if !slices.Contains(c.AllowedHosts, p.Host) {
			c.AllowedHosts = append(c.AllowedHosts, p.Host)
		}
	}
	if s.LLMProvider == "" {
		if s.Model != "" || s.TriageModel != "" {
			return errors.New("config models: a model needs its llm provider")
		}
		return nil
	}
	p, ok := lookupLLMProvider(s.LLMProvider)
	if !ok {
		return fmt.Errorf("config models: llm provider %q is not one of %s", s.LLMProvider, llmIDs())
	}
	m, ok := p.model(s.Model)
	if !ok {
		return fmt.Errorf("config models: %q is not a %s model this switch offers (%s)", s.Model, p.ID, modelIDs(p))
	}
	t := m
	if s.TriageModel != "" {
		if t, ok = p.model(s.TriageModel); !ok {
			return fmt.Errorf("config models: triage model %q is not a %s model this switch offers (%s)", s.TriageModel, p.ID, modelIDs(p))
		}
	}
	c.LLM.Provider, c.LLM.Model = p.ID, m.ID
	c.LLM.TriageModel = ""
	if t.ID != m.ID {
		c.LLM.TriageModel = t.ID
	}
	// The configured prices meter llm.model, and any model the provider's own
	// table does not price (Gemini publishes none to Manvi): the higher of the
	// two models' rates, so the triage tier is never under-metered.
	c.LLM.InputNanoUSDPerToken = max(m.InputNanoUSD, t.InputNanoUSD)
	c.LLM.OutputNanoUSDPerToken = max(m.OutputNanoUSD, t.OutputNanoUSD)
	return nil
}

// modelFamilyProvider names the llm provider a model id unmistakably belongs
// to ("claude-" is anthropic's, "gemini-" Google's), or "" when the id does not
// say.
func modelFamilyProvider(model string) string {
	switch {
	case strings.HasPrefix(model, "claude-"):
		return "anthropic"
	case strings.HasPrefix(model, "gemini-"):
		return "gemini"
	}
	return ""
}

func decisionIDs() string {
	var ids []string
	for _, p := range decisionProviders {
		ids = append(ids, p.ID)
	}
	return fmt.Sprint(ids)
}

func llmIDs() string {
	var ids []string
	for _, p := range llmProviders {
		ids = append(ids, p.ID)
	}
	return fmt.Sprint(ids)
}

func modelIDs(p LLMProviderChoice) string {
	var ids []string
	for _, m := range p.Models {
		ids = append(ids, m.ID)
	}
	return fmt.Sprint(ids)
}
