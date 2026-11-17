package config

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// The decision model's providers (decide.provider). Each speaks a Decisions
// API that decide/jev drives; the model's other settings stay in the config
// when another provider is chosen, so switching back loses nothing.
const (
	// DecisionOpenRouter asks decide.openrouter_model (Liquid D) through
	// OpenRouter's Decisions API. The default ("" reads as it).
	DecisionOpenRouter = "openrouter"
	// DecisionSubQ asks decide.subq_model on SubQ's /v1/systemone.
	DecisionSubQ = "subq"
	// DecisionGemini asks decide.gemini_model on Google's Decisions API with
	// the AI Studio key, the one llm.provider "gemini" uses.
	DecisionGemini = "gemini"
)

// DecisionProvider is one row of the provider table: everything vani needs to
// reach a provider's decision model, and nothing a config may change.
type DecisionProvider struct {
	ID    string
	Label string
	// Endpoint is the Decisions URL; "" for OpenRouter, which decide/jev
	// defaults (and to whose host alone it sends an environment key).
	Endpoint string
	Host     string
	// DefaultModel answers when the config names none.
	DefaultModel string
	// KeyEnvs are read in order before the Keychain. OpenRouter's is the
	// configurable decide.openrouter_key_env instead.
	KeyEnvs []string
	// KeychainService is the login Keychain item the Mac app keeps the key in
	// (vault.ModelKey's table and Keychain in macos/.../Drafting.swift).
	KeychainService string
	// InputUSDPerMTok prices a reply that reports input tokens but no cost.
	InputUSDPerMTok float64
	// Gemini is true for Google's dialect (decide/jev WireGemini).
	Gemini bool
}

// Keychain services for the model keys. vault.ModelKey reads only these.
const (
	KeychainOpenRouter = "openrouter-api-key"
	KeychainSubQ       = "subq-api-key"
	KeychainGemini     = "gemini-api-key"
)

// GeminiKeyEnvs are the variables Manvi's credential resolver reads for the
// gemini provider, in its order; the decision model reads the same key.
var GeminiKeyEnvs = []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}

const (
	hostSubQ   = "api.subq.ai"
	hostGemini = "generativelanguage.googleapis.com"
)

var decisionProviders = []DecisionProvider{
	{ID: DecisionOpenRouter, Label: "Liquid (OpenRouter)", Host: hostOpenRouter,
		DefaultModel: "liquid/d1.2-ultra", KeychainService: KeychainOpenRouter},
	// docs.subq.ai (read 2026-11-17): subq-snap-1.2 is the decision model,
	// $0.04 per 1M input tokens, output free; usage reports input_tokens only.
	{ID: DecisionSubQ, Label: "SubQ Snap", Endpoint: "https://" + hostSubQ + "/v1/systemone", Host: hostSubQ,
		DefaultModel: "subq-snap-1.2", KeyEnvs: []string{"SUBQ_API_KEY"}, KeychainService: KeychainSubQ,
		InputUSDPerMTok: 0.04},
	// aistudio.google.com/docs/decisions-api and /pricing (read 2026-11-17):
	// gemini-decision-1, $0.10 per 1M input tokens on the paid tier (free on
	// the free tier; metering at the paid rate only overstates).
	{ID: DecisionGemini, Label: "Gemini Decision (AI Studio)", Endpoint: "https://" + hostGemini + "/v1beta/decisions", Host: hostGemini,
		DefaultModel: "gemini-decision-1", KeyEnvs: GeminiKeyEnvs, KeychainService: KeychainGemini,
		InputUSDPerMTok: 0.10, Gemini: true},
}

// DecisionProviders is a copy of the provider table, in the order the app
// offers them.
func DecisionProviders() []DecisionProvider {
	out := slices.Clone(decisionProviders)
	for i := range out {
		out[i].KeyEnvs = slices.Clone(out[i].KeyEnvs)
	}
	return out
}

// LookupDecisionProvider returns the table row for id ("" is OpenRouter).
func LookupDecisionProvider(id string) (DecisionProvider, bool) {
	if id == "" {
		id = DecisionOpenRouter
	}
	for _, p := range decisionProviders {
		if p.ID == id {
			p.KeyEnvs = slices.Clone(p.KeyEnvs)
			return p, true
		}
	}
	return DecisionProvider{}, false
}

// ProviderID is decide.provider with "" read as DecisionOpenRouter.
func (d Decide) ProviderID() string {
	if d.Provider == "" {
		return DecisionOpenRouter
	}
	return d.Provider
}

// DecisionProvider is the configured provider's table row; ok is false for
// a provider Validate refuses.
func (d Decide) DecisionProvider() (DecisionProvider, bool) {
	return LookupDecisionProvider(d.Provider)
}

// DecisionModel is the model the configured provider is asked: its own
// setting, else the provider's default. OpenRouter has no default here: an
// empty decide.openrouter_model turns the decision model off, as it always has.
func (d Decide) DecisionModel() string {
	p, _ := d.DecisionProvider()
	var m string
	switch p.ID {
	case DecisionOpenRouter:
		return strings.TrimSpace(d.OpenRouterModel)
	case DecisionSubQ:
		m = d.SubQModel
	case DecisionGemini:
		m = d.GeminiModel
	default:
		return ""
	}
	if m = strings.TrimSpace(m); m == "" {
		return p.DefaultModel
	}
	return m
}

// DecisionKeyEnvs are the variables the configured provider's key is read
// from before the Keychain.
func (d Decide) DecisionKeyEnvs() []string {
	p, _ := d.DecisionProvider()
	if p.ID == DecisionOpenRouter {
		if d.OpenRouterKeyEnv == "" {
			return nil
		}
		return []string{d.OpenRouterKeyEnv}
	}
	return p.KeyEnvs
}

// DecisionName is the name the decision model answers under in routing,
// ledger annotations, drop reasons and the eval: see BackendName.
func (d Decide) DecisionName() string { return BackendName(d.DecisionModel()) }

// DecisionConfigured reports whether a decision model is set up.
func (c Config) DecisionConfigured() bool { return c.Decide.DecisionModel() != "" }

// validateDecision checks decide.provider and the provider model settings.
func (d Decide) validateDecision(add func(string, ...any)) {
	if _, ok := d.DecisionProvider(); !ok {
		add("decide.provider %q is not %s, %s or %s", d.Provider, DecisionOpenRouter, DecisionSubQ, DecisionGemini)
		return
	}
	for _, f := range []struct{ key, v string }{{"decide.subq_model", d.SubQModel}, {"decide.gemini_model", d.GeminiModel}} {
		if err := checkModelID(f.v); err != nil {
			add("%s %q: %v", f.key, f.v, err)
		}
	}
}

// checkModelID accepts "" (the provider's default) or a model id that fits a
// request: at most MaxModelName bytes with no space or control character.
func checkModelID(m string) error {
	switch {
	case m == "":
		return nil
	case len(m) > MaxModelName:
		return fmt.Errorf("is longer than %d bytes", MaxModelName)
	case strings.IndexFunc(m, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		return fmt.Errorf("must not contain spaces or control characters")
	}
	return nil
}
