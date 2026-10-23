package main

import (
	"fmt"
	"strings"

	"github.com/bharathvbcr/Vani/internal/config"
)

// A daemon that holds a long-lived environment is started by something that
// has to put the decision model's key there (the Mac app's schedule agent reads
// it from the login Keychain). When it could not, the daemon still runs, because
// listing boards, scoring and the digest need no model, but every posting it
// reads stays live. Only the daemon knows what reached its own environment, so
// it says so in daemon.json at start, and status reports it: "no key" is a
// visible state, never a quiet no-op.

// daemonModel is whether the decision model the config names can answer in this
// daemon. It is absent from daemon.json when the config names no OpenRouter
// model (there is no key for the daemon to lack) and from a daemon that predates
// the field (unknown, not "enabled").
type daemonModel struct {
	// Enabled is true when the key's environment variable was set when the
	// daemon started. It never says the key is valid: only a call does.
	Enabled bool `json:"enabled"`
	// KeyEnv is the variable the config reads the key from (decide.openrouter_key_env).
	KeyEnv string `json:"key_env,omitempty"`
	// Reason is why the model is disabled; absent when it is enabled.
	Reason string `json:"reason,omitempty"`
}

// daemonModelOf reports the decision model's state for a daemon whose
// environment is getenv, or nil when the config names no OpenRouter model. The
// key's value is never read into the result.
func daemonModelOf(cfg config.Config, getenv func(string) string) *daemonModel {
	if !cfg.OpenRouterConfigured() {
		return nil
	}
	env := cfg.Decide.OpenRouterKeyEnv
	m := &daemonModel{KeyEnv: env}
	if strings.TrimSpace(getenv(env)) != "" {
		m.Enabled = true
		return m
	}
	m.Reason = fmt.Sprintf("%s is not set in the daemon's environment, so the decision model (%s) is disabled and the daemon triages no posting",
		env, cfg.Decide.OpenRouterModel)
	return m
}

// daemonLines is what the text status says about a running daemon beyond
// whether it keeps the schedule: only a problem, one line.
func daemonLines(d daemonFacet) []string {
	if !d.Running || d.Model == nil || d.Model.Enabled {
		return nil
	}
	return []string{"daemon: model disabled: " + d.Model.Reason,
		"  fix: add the key to the login Keychain (service openrouter-api-key) and turn Run on schedule off and on in Vani Apply's Pipeline tab, or start the daemon with " + d.Model.KeyEnv + " set"}
}
