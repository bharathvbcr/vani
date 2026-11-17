package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/decide/jev"
)

// errNoDecisionKey is decisionJev's error for a SubQ or Gemini decision model
// with no key in the environment or the Keychain. The backend is then off,
// exactly as an OpenRouter model without a key is.
var errNoDecisionKey = errors.New("no API key for the decision model")

// decisionJev fills c with the configured decision model, so every client vani
// builds for it (the run, suggest, contacts, notes, kbmatch) reaches the same
// provider with the same model, name, wire, price and key. Callers set the
// question's fields (Descriptions, Instructions, State) and the spend.
//
// endpoint, when not empty, replaces the provider's Decisions URL: the test
// seams notesEndpoint and kbmatchEndpoint. A SubQ or Gemini key travels to
// whatever URL that names; an OpenRouter key from the environment still goes
// only to openrouter.ai (decide/jev's rule).
//
// The OpenRouter key keeps its path (decide.openrouter_key_env through
// openRouterGetenv). SubQ's and Gemini's keys are resolved here, environment
// first and then the provider's Keychain item, and passed as Config.Key: jev
// sends an environment key to openrouter.ai only, and sends no Authorization
// at all to another host without Config.Key.
func (w *wired) decisionJev(c jev.Config, endpoint string) (jev.Config, error) {
	d := w.cfg.Decide
	p, ok := d.DecisionProvider()
	if !ok {
		return c, fmt.Errorf("decide.provider %q is not a decision provider", d.Provider)
	}
	c.Name, c.Model = d.DecisionName(), d.DecisionModel()
	c.Endpoint = p.Endpoint
	if endpoint != "" {
		c.Endpoint = endpoint
	}
	c.InputUSDPerMTok = p.InputUSDPerMTok
	if p.Gemini {
		c.Wire = jev.WireGemini
	}
	if p.ID == config.DecisionOpenRouter {
		c.KeyEnv, c.Getenv = d.OpenRouterKeyEnv, w.openRouterGetenv
		return c, nil
	}
	key, _, why := w.decisionKey()
	if key == "" {
		msg := fmt.Sprintf("%s is not set", strings.Join(p.KeyEnvs, " or "))
		if why != "" {
			msg += " and " + why
		}
		return c, fmt.Errorf("%w (%s): %s", errNoDecisionKey, p.ID, msg)
	}
	c.Key = key
	return c, nil
}

// decisionWhere names where the decision model is asked, for a dry run's
// account of what a command will send: the provider's host.
func decisionWhere(d config.Decide) string {
	if p, ok := d.DecisionProvider(); ok {
		return p.Host
	}
	return "decide.provider " + d.Provider
}

// decisionKey is the configured decision model's key, where it came from and,
// when there is none, why the Keychain gave none: openRouterKey for OpenRouter,
// else the provider's variables and then its Keychain item. Never logged.
func (w *wired) decisionKey() (key, from, why string) {
	d := w.cfg.Decide
	p, ok := d.DecisionProvider()
	switch {
	case !ok:
		return "", "", ""
	case p.ID == config.DecisionOpenRouter:
		return w.openRouterKey(d.OpenRouterKeyEnv)
	}
	return w.modelKey(p.ID, p.KeyEnvs, p.KeychainService)
}
