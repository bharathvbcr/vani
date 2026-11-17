package main

import (
	"github.com/bharathvbcr/Vani/internal/config"
)

// modelsReply is the one JSON object `vani config models` writes: what the
// config chooses now and every choice the Mac app's switch may offer, so the
// app never keeps its own copy of the provider tables. It names Keychain
// items and variables, never a key.
type modelsReply struct {
	Decision decisionModels `json:"decision"`
	LLM      llmModels      `json:"llm"`
	failure
}

type decisionModels struct {
	Provider string           `json:"provider"`
	Model    string           `json:"model"`
	Choices  []decisionChoice `json:"choices"`
}

type decisionChoice struct {
	ID              string   `json:"id"`
	Label           string   `json:"label"`
	Model           string   `json:"model"`
	Host            string   `json:"host"`
	KeychainService string   `json:"keychain_service"`
	KeyEnvs         []string `json:"key_envs"`
}

type llmModels struct {
	Provider    string                     `json:"provider"`
	Model       string                     `json:"model"`
	TriageModel string                     `json:"triage_model"`
	Choices     []config.LLMProviderChoice `json:"choices"`
}

// cmdConfigModels prints the model switch's state, or with --set applies one
// selection (config.ApplyModelSelection) in one locked, validated write: a
// refused selection leaves the file unchanged. It is how the Mac app's
// Settings switches the decision model and the llm provider. Keys never pass
// through here: the app keeps them in the login Keychain.
func (e *env) cmdConfigModels(args []string) error {
	fs := e.newFlags("config models")
	set := fs.Bool("set", false, "apply the selection below and save the config")
	decision := fs.String("decision", "", "decision model provider: openrouter, subq or gemini")
	llmProvider := fs.String("llm", "", "llm provider: anthropic or gemini (with --model)")
	model := fs.String("model", "", "the llm model for drafts and the rest (llm.model)")
	triage := fs.String("triage-model", "", "the llm model for triage (llm.triage_model); empty means --model")
	if err := parse(fs, args); err != nil {
		return err
	}
	if !*set && (*decision != "" || *llmProvider != "" || *model != "" || *triage != "") {
		return usagef("config models: --decision, --llm, --model and --triage-model need --set")
	}
	if *set {
		sel := config.ModelSelection{DecisionProvider: *decision, LLMProvider: *llmProvider, Model: *model, TriageModel: *triage}
		if err := e.updateConfig(func(c *config.Config) error { return c.ApplyModelSelection(sel) }); err != nil {
			return e.reply(true, &modelsReply{}, err, nil)
		}
	}
	cfg, err := e.loadConfigUnchecked()
	if err != nil {
		return e.reply(true, &modelsReply{}, err, nil)
	}
	return e.reply(true, modelsOf(cfg), nil, nil)
}

func modelsOf(cfg config.Config) *modelsReply {
	r := &modelsReply{
		Decision: decisionModels{Provider: cfg.Decide.ProviderID(), Model: cfg.Decide.DecisionModel()},
		LLM:      llmModels{Provider: cfg.LLM.Provider, Model: cfg.LLM.Model, TriageModel: cfg.LLM.EffectiveTriageModel(), Choices: config.LLMProviders()},
	}
	for _, p := range config.DecisionProviders() {
		m := p.DefaultModel
		switch p.ID {
		case config.DecisionOpenRouter:
			if cfg.Decide.OpenRouterModel != "" {
				m = cfg.Decide.OpenRouterModel
			}
		case config.DecisionSubQ:
			if cfg.Decide.SubQModel != "" {
				m = cfg.Decide.SubQModel
			}
		case config.DecisionGemini:
			if cfg.Decide.GeminiModel != "" {
				m = cfg.Decide.GeminiModel
			}
		}
		envs := p.KeyEnvs
		if p.ID == config.DecisionOpenRouter && cfg.Decide.OpenRouterKeyEnv != "" {
			envs = []string{cfg.Decide.OpenRouterKeyEnv}
		}
		r.Decision.Choices = append(r.Decision.Choices, decisionChoice{ID: p.ID, Label: p.Label, Model: m, Host: p.Host,
			KeychainService: p.KeychainService, KeyEnvs: envs})
	}
	return r
}
