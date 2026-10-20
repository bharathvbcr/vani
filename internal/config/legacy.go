package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// The OpenRouter decision model was configured under "jev" names until
// 2026-10-19, after the Jev client it is asked through. Configs and track
// deciders written before then still load: each old key is read as its new
// name, a file that sets both with different values is refused, and
// Deprecations lists the old names a file uses so callers can say so. Save
// writes only the new names. kbmatch.backend "jev" followed on 2026-10-20 the
// same way.
const (
	legacyModelKey    = "jev_model"
	legacyKeyEnvKey   = "jev_key_env"
	legacyDailyKey    = "jev"
	legacyMinProbKey  = "min_jev_prob"
	legacyShadowValue = "jev"
	// legacyKBMatchBackend is kbmatch.backend's name for KBMatchOpenRouter
	// until 2026-10-20.
	legacyKBMatchBackend = "jev"
)

// ShadowOpenRouter is the decide.shadow value that makes the OpenRouter
// decision model the shadow.
const ShadowOpenRouter = "openrouter"

// MaxBackendName is decide/jev's MaxNameBytes: the longest backend name the
// Jev client accepts. cmd/vani's tests check the two agree.
const MaxBackendName = 32

// reservedBackendNames are the names other backends and the eval report
// already use; an OpenRouter model must not answer under one of them, or its
// readings would be filed as theirs.
// "title" is the deterministic title reader (title_signals.read).
var reservedBackendNames = []string{"llm", "lappi", "local", "unknown", "title"}

// OpenRouterName is the name the OpenRouter decision model answers under in
// routing, ledger annotations, drop reasons and the eval: see BackendName.
func (d Decide) OpenRouterName() string { return BackendName(d.OpenRouterModel) }

// BackendName derives a backend name from a model id: its last path segment,
// lower-cased, with every character outside [a-z0-9_-] turned into '-' and
// leading or trailing '-' trimmed ("liquid/d1-ultra" -> "d1-ultra",
// "typesafe/jev-1.13" -> "jev-1-13"). The result may be empty or too long;
// Validate refuses those.
func BackendName(model string) string {
	m := strings.TrimSpace(model)
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(m) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// ReservedBackendNames is a copy of the reserved set, for the test in cmd/vani
// that holds it equal to llmbackend's (the two packages cannot import each other).
func ReservedBackendNames() []string { return slices.Clone(reservedBackendNames) }

// CheckBackendName reports whether n may name an OpenRouter decision backend.
func CheckBackendName(n string) error { return checkBackendName(n) }

func checkBackendName(n string) error {
	switch {
	case n == "":
		return errors.New("leaves no backend name (its last path segment has no letters or digits)")
	case len(n) > MaxBackendName:
		return fmt.Errorf("backend name %q is longer than %d bytes", n, MaxBackendName)
	case slices.Contains(reservedBackendNames, n):
		return fmt.Errorf("backend name %q is taken by another backend", n)
	case strings.HasPrefix(n, LocalShadowPrefix):
		return fmt.Errorf("backend name %q starts with %q, which the local shadow's names use", n, LocalShadowPrefix)
	}
	return nil
}

// strictDecode decodes one JSON value refusing unknown fields. A custom
// UnmarshalJSON does not inherit the outer decoder's DisallowUnknownFields,
// so each one below uses this to stay as strict as Parse.
func strictDecode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data after JSON value")
	}
	return nil
}

// pick returns the value set under the new key or, failing that, the old one.
// Both set to different values is an error: neither can be trusted.
func pick[T comparable](newKey, oldKey string, newV, oldV *T) (T, error) {
	var zero T
	switch {
	case newV != nil && oldV != nil:
		if *newV != *oldV {
			return zero, fmt.Errorf("%s (%v) and its old name %s (%v) are both set and differ; keep %s", newKey, *newV, oldKey, *oldV, newKey)
		}
		return *newV, nil
	case newV != nil:
		return *newV, nil
	case oldV != nil:
		return *oldV, nil
	}
	return zero, nil
}

// UnmarshalJSON reads Decide, accepting the old jev_* keys and shadow "jev".
func (d *Decide) UnmarshalJSON(data []byte) error {
	type plain Decide // no methods, so no recursion
	var w struct {
		plain
		Model     *string `json:"openrouter_model"`
		KeyEnv    *string `json:"openrouter_key_env"`
		OldModel  *string `json:"jev_model"`
		OldKeyEnv *string `json:"jev_key_env"`
	}
	if err := strictDecode(data, &w); err != nil {
		return err
	}
	out := Decide(w.plain)
	var err error
	if out.OpenRouterModel, err = pick("decide.openrouter_model", "decide."+legacyModelKey, w.Model, w.OldModel); err != nil {
		return err
	}
	if out.OpenRouterKeyEnv, err = pick("decide.openrouter_key_env", "decide."+legacyKeyEnvKey, w.KeyEnv, w.OldKeyEnv); err != nil {
		return err
	}
	if out.Shadow == legacyShadowValue {
		out.Shadow = ShadowOpenRouter
	}
	*d = out
	return nil
}

// UnmarshalJSON reads KBMatch, accepting the old backend "jev".
func (k *KBMatch) UnmarshalJSON(data []byte) error {
	type plain KBMatch
	var w plain
	if err := strictDecode(data, &w); err != nil {
		return err
	}
	if w.Backend == legacyKBMatchBackend {
		w.Backend = KBMatchOpenRouter
	}
	*k = KBMatch(w)
	return nil
}

// UnmarshalJSON reads DailyUSD, accepting the old "jev" cap.
func (u *DailyUSD) UnmarshalJSON(data []byte) error {
	var w struct {
		OpenRouter    *float64 `json:"openrouter"`
		OldOpenRouter *float64 `json:"jev"`
		LLM           float64  `json:"llm"`
	}
	if err := strictDecode(data, &w); err != nil {
		return err
	}
	v, err := pick("decide.daily_usd.openrouter", "decide.daily_usd."+legacyDailyKey, w.OpenRouter, w.OldOpenRouter)
	if err != nil {
		return err
	}
	*u = DailyUSD{OpenRouter: v, LLM: w.LLM}
	return nil
}

// UnmarshalJSON reads DropRule, accepting the old min_jev_prob.
func (r *DropRule) UnmarshalJSON(data []byte) error {
	type plain DropRule
	var w struct {
		plain
		MinProb    *float64 `json:"min_openrouter_prob"`
		OldMinProb *float64 `json:"min_jev_prob"`
	}
	if err := strictDecode(data, &w); err != nil {
		return err
	}
	out := DropRule(w.plain)
	var err error
	if out.MinOpenRouterProb, err = pick("drop_rules[].min_openrouter_prob", "drop_rules[]."+legacyMinProbKey, w.MinProb, w.OldMinProb); err != nil {
		return fmt.Errorf("drop rule for slot %q: %w", out.Slot, err)
	}
	*r = out
	return nil
}

// Deprecations lists, in file order of sections, the old key names a config
// document uses, each with its replacement. It reads only those keys and
// reports nothing for a document it cannot read (Parse reports that).
func Deprecations(data []byte) []string {
	var p struct {
		Decide *struct {
			Model    json.RawMessage `json:"jev_model"`
			KeyEnv   json.RawMessage `json:"jev_key_env"`
			Shadow   *string         `json:"shadow"`
			DailyUSD *struct {
				OpenRouter json.RawMessage `json:"jev"`
			} `json:"daily_usd"`
		} `json:"decide"`
		DropRules []struct {
			MinProb json.RawMessage `json:"min_jev_prob"`
		} `json:"drop_rules"`
		KBMatch *struct {
			Backend *string `json:"backend"`
		} `json:"kbmatch"`
	}
	if json.Unmarshal(data, &p) != nil {
		return nil
	}
	var out []string
	if d := p.Decide; d != nil {
		if d.Model != nil {
			out = append(out, "decide.jev_model is now decide.openrouter_model")
		}
		if d.KeyEnv != nil {
			out = append(out, "decide.jev_key_env is now decide.openrouter_key_env")
		}
		if d.DailyUSD != nil && d.DailyUSD.OpenRouter != nil {
			out = append(out, "decide.daily_usd.jev is now decide.daily_usd.openrouter")
		}
		if d.Shadow != nil && *d.Shadow == legacyShadowValue {
			out = append(out, `decide.shadow "jev" is now "openrouter"`)
		}
	}
	if k := p.KBMatch; k != nil && k.Backend != nil && *k.Backend == legacyKBMatchBackend {
		out = append(out, `kbmatch.backend "jev" is now "openrouter"`)
	}
	n := 0
	for _, r := range p.DropRules {
		if r.MinProb != nil {
			n++
		}
	}
	if n > 0 {
		out = append(out, fmt.Sprintf("drop_rules min_jev_prob is now min_openrouter_prob (%d rule(s))", n))
	}
	if k := p.KBMatch; k != nil && k.Backend != nil && *k.Backend == legacyKBMatchBackend {
		out = append(out, fmt.Sprintf("kbmatch.backend %q is now %q", legacyKBMatchBackend, KBMatchOpenRouter))
	}
	return out
}
