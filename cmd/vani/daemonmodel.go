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
	// Enabled is true when the daemon holds a key for the model: the key's
	// environment variable was set when it started, or it read the login
	// Keychain item itself (Source). It never says the key is valid: only a
	// call does.
	Enabled bool `json:"enabled"`
	// KeyEnv is the variable the config reads the key from
	// (decide.openrouter_key_env, or the provider's: "SUBQ_API_KEY",
	// "GEMINI_API_KEY or GOOGLE_API_KEY").
	KeyEnv string `json:"key_env,omitempty"`
	// Service is the login Keychain item that holds the provider's key;
	// absent from a daemon that predates the field (openrouter-api-key).
	Service string `json:"service,omitempty"`
	// Source is "keychain" when the daemon read the key from the login
	// Keychain itself; absent means the environment (every daemon before the
	// field, and one the Mac app started).
	Source string `json:"source,omitempty"`
	// Reason is why the model is disabled; absent when it is enabled.
	Reason string `json:"reason,omitempty"`
	// Keychain is why the daemon's own Keychain read gave no key (it was
	// switched off, the item is missing or the read was refused); absent when
	// the daemon did not say or the key was found.
	Keychain string `json:"keychain,omitempty"`
}

// keyLookup finds the decision model's key in a daemon's process: the key
// (never put in a result), where it came from and, when there is none, why the
// Keychain gave none, for the configured provider. (*wired).decisionKey is
// the real one.
type keyLookup func() (key, from, why string)

// daemonModelOf reports the decision model's state for a daemon whose
// environment is getenv and that reads nothing else, or nil when the config
// names no decision model. The key's value is never read into the result.
func daemonModelOf(cfg config.Config, getenv func(string) string) *daemonModel {
	return daemonModelFor(cfg, func() (string, string, string) {
		for _, name := range cfg.Decide.DecisionKeyEnvs() {
			if v := getenv(name); strings.TrimSpace(v) != "" {
				return v, keyFromEnvironment, ""
			}
		}
		return "", keyFromEnvironment, ""
	})
}

// daemonModelFor is daemonModelOf for a daemon that looks the key up with
// lookup: its environment first, then, as vani does for every OpenRouter
// client, the login Keychain unless VANI_NO_KEYCHAIN is set. It is called once
// at the daemon's start, so a Keychain read happens there and not at the first
// run (a success is kept for the process).
func daemonModelFor(cfg config.Config, lookup keyLookup) *daemonModel {
	if !cfg.DecisionConfigured() {
		return nil
	}
	envs := cfg.Decide.DecisionKeyEnvs()
	env := strings.Join(envs, " or ")
	m := &daemonModel{KeyEnv: env}
	if p, ok := cfg.Decide.DecisionProvider(); ok {
		m.Service = p.KeychainService
	}
	key, from, why := lookup()
	if strings.TrimSpace(key) != "" {
		m.Enabled = true
		if from == keyFromKeychain {
			m.Source = from
		}
		return m
	}
	m.Keychain = why
	reason := fmt.Sprintf("%s is not set in the daemon's environment", env)
	if why != "" {
		reason += " and " + why
	}
	m.Reason = fmt.Sprintf("%s, so the decision model (%s) is disabled and the daemon triages no posting", reason, cfg.Decide.DecisionModel())
	return m
}

// daemonLines is what the text status says about a running daemon beyond
// whether it keeps the schedule: only a problem, one line.
func daemonLines(d daemonFacet) []string {
	var lines []string
	if d.Running && d.Binary == daemonBinaryOther {
		lines = append(lines, "daemon: "+d.Reason)
	}
	if !d.Running || d.Model == nil || d.Model.Enabled {
		return lines
	}
	service := d.Model.Service
	if service == "" {
		service = config.KeychainOpenRouter
	}
	fix := "  fix: add the key to the login Keychain (service " + service + "; Vani Apply's Settings can paste it there) and turn Run on schedule off and on in Vani Apply's Pipeline tab, or start the daemon with " + d.Model.KeyEnv + " set"
	if d.Model.Keychain != "" {
		// This daemon looked in the Keychain itself (a daemon the app started is told not to).
		fix += "; a daemon the app did not start reads the item itself when it starts, unless " + noKeychainEnv + " is set"
	}
	return append(lines, "daemon: model disabled: "+d.Model.Reason, fix)
}
