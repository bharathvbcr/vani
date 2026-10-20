package config

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

// livePlainConfig is shaped like the live config: Anthropic Opus as the LLM,
// the OpenRouter decision model, drop rules, no shadow. It sets neither
// llm.triage_model nor decide.escalate, which must therefore read as before.
const livePlainConfig = `{
  "version": 1,
  "watchlist": [{"ats": "gh", "token": "acme", "company": "Acme"}],
  "allowed_hosts": ["boards-api.greenhouse.io", "openrouter.ai"],
  "schedule": {"daily_at": "08:30", "timezone": "America/Los_Angeles"},
  "decide": {
    "openrouter_model": "liquid/d1-ultra",
    "openrouter_key_env": "OPENROUTER_API_KEY",
    "daily_usd": {"openrouter": 20, "llm": 10}
  },
  "llm": {
    "provider": "anthropic",
    "model": "claude-opus-5-5",
    "input_nano_usd_per_token": 4000,
    "output_nano_usd_per_token": 20000
  },
  "advance_per_run": 3000,
  "drop_rules": [
    {"slot": "role_fit", "values": ["mismatch_domain"], "min_openrouter_prob": 0.9}
  ]
}`

func TestTriageModelDefaultsToModel(t *testing.T) {
	c := valid()
	c.LLM = LLM{Provider: "anthropic", Model: "claude-opus-5-5", InputNanoUSDPerToken: 4000, OutputNanoUSDPerToken: 20000}
	if got := c.LLM.EffectiveTriageModel(); got != "claude-opus-5-5" {
		t.Fatalf("empty triage_model resolved to %q, want the model", got)
	}
	c.LLM.TriageModel = "claude-haiku-5-5"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := c.LLM.EffectiveTriageModel(); got != "claude-haiku-5-5" {
		t.Fatalf("triage_model resolved to %q", got)
	}
	if got := (LLM{}).EffectiveTriageModel(); got != "" {
		t.Fatalf("an unconfigured llm resolved to %q", got)
	}
}

func TestTriageModelRules(t *testing.T) {
	for name, tc := range map[string]struct {
		mut  func(*Config)
		want string
	}{
		"without llm":    {func(c *Config) { c.LLM = LLM{}; c.LLM.TriageModel = "claude-haiku-5-5" }, "llm.triage_model needs llm.provider"},
		"blank":          {func(c *Config) { c.LLM.TriageModel = "   " }, "llm.triage_model"},
		"inner space":    {func(c *Config) { c.LLM.TriageModel = "claude haiku" }, "llm.triage_model"},
		"control":        {func(c *Config) { c.LLM.TriageModel = "claude-haiku\x00" }, "llm.triage_model"},
		"newline":        {func(c *Config) { c.LLM.TriageModel = "claude-haiku\n5-5" }, "llm.triage_model"},
		"over the bound": {func(c *Config) { c.LLM.TriageModel = strings.Repeat("m", MaxModelName+1) }, "llm.triage_model"},
	} {
		t.Run(name, func(t *testing.T) {
			c := valid()
			tc.mut(&c)
			if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate = %v, want %q", err, tc.want)
			}
		})
	}
	c := valid()
	c.LLM.TriageModel = strings.Repeat("m", MaxModelName)
	if err := c.Validate(); err != nil {
		t.Fatalf("a model at the bound: %v", err)
	}
}

func TestEscalateDefaults(t *testing.T) {
	var d Decide
	if d.EscalateEnabled() {
		t.Fatal("escalation is on without a section")
	}
	if d.EscalateBelowP() != 0.5 || d.EscalateMaxPerRun() != 200 {
		t.Fatalf("defaults %v / %d, want 0.5 / 200", d.EscalateBelowP(), d.EscalateMaxPerRun())
	}
	d.Escalate = &Escalate{Enabled: true}
	if !d.EscalateEnabled() || d.EscalateBelowP() != DefaultEscalateBelowP || d.EscalateMaxPerRun() != DefaultEscalateMaxPerRun {
		t.Fatalf("an enabled bare section: %v / %d", d.EscalateBelowP(), d.EscalateMaxPerRun())
	}
	d.Escalate = &Escalate{Enabled: true, BelowP: ptr(0.3), MaxPerRun: ptr(7)}
	if d.EscalateBelowP() != 0.3 || d.EscalateMaxPerRun() != 7 {
		t.Fatalf("explicit values: %v / %d", d.EscalateBelowP(), d.EscalateMaxPerRun())
	}
	// A disabled section never reports enabled, whatever else it sets.
	d.Escalate = &Escalate{Enabled: false, BelowP: ptr(0.3)}
	if d.EscalateEnabled() {
		t.Fatal("a disabled section reads as enabled")
	}
}

func TestEscalateValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		mut  func(*Config)
		want string // "" means valid
	}{
		"enabled with defaults": {func(c *Config) { c.Decide.Escalate = &Escalate{Enabled: true} }, ""},
		"disabled section":      {func(c *Config) { c.Decide.Escalate = &Escalate{} }, ""},
		"below_p exactly 1":     {func(c *Config) { c.Decide.Escalate = &Escalate{Enabled: true, BelowP: ptr(1.0)} }, ""},
		"below_p tiny": {func(c *Config) {
			c.Decide.Escalate = &Escalate{Enabled: true, BelowP: ptr(math.SmallestNonzeroFloat64)}
		}, ""},
		"max_per_run 1":                  {func(c *Config) { c.Decide.Escalate = &Escalate{Enabled: true, MaxPerRun: ptr(1)} }, ""},
		"max_per_run at bound":           {func(c *Config) { c.Decide.Escalate = &Escalate{Enabled: true, MaxPerRun: ptr(MaxEscalatePerRun)} }, ""},
		"enabled without llm":            {func(c *Config) { c.LLM = LLM{}; c.Decide.Escalate = &Escalate{Enabled: true} }, "decide.escalate needs llm.provider"},
		"enabled with shadow":            {func(c *Config) { c.Decide.Shadow = "llm"; c.Decide.Escalate = &Escalate{Enabled: true} }, "decide.escalate and decide.shadow are exclusive"},
		"enabled with openrouter shadow": {func(c *Config) { c.Decide.Shadow = "openrouter"; c.Decide.Escalate = &Escalate{Enabled: true} }, "decide.escalate and decide.shadow are exclusive"},
		"disabled with shadow":           {func(c *Config) { c.Decide.Shadow = "llm"; c.Decide.Escalate = &Escalate{} }, ""},
		"below_p zero":                   {func(c *Config) { c.Decide.Escalate = &Escalate{Enabled: true, BelowP: ptr(0.0)} }, "decide.escalate.below_p"},
		"below_p negative":               {func(c *Config) { c.Decide.Escalate = &Escalate{Enabled: true, BelowP: ptr(-0.1)} }, "decide.escalate.below_p"},
		"below_p over 1":                 {func(c *Config) { c.Decide.Escalate = &Escalate{Enabled: true, BelowP: ptr(1.5)} }, "decide.escalate.below_p"},
		"below_p NaN":                    {func(c *Config) { c.Decide.Escalate = &Escalate{Enabled: true, BelowP: ptr(math.NaN())} }, "decide.escalate.below_p"},
		"below_p +Inf":                   {func(c *Config) { c.Decide.Escalate = &Escalate{Enabled: true, BelowP: ptr(math.Inf(1))} }, "decide.escalate.below_p"},
		"below_p bad when off":           {func(c *Config) { c.Decide.Escalate = &Escalate{BelowP: ptr(7.0)} }, "decide.escalate.below_p"},
		"max_per_run zero":               {func(c *Config) { c.Decide.Escalate = &Escalate{Enabled: true, MaxPerRun: ptr(0)} }, "decide.escalate.max_per_run"},
		"max_per_run negative":           {func(c *Config) { c.Decide.Escalate = &Escalate{Enabled: true, MaxPerRun: ptr(-1)} }, "decide.escalate.max_per_run"},
		"max_per_run over bound":         {func(c *Config) { c.Decide.Escalate = &Escalate{Enabled: true, MaxPerRun: ptr(MaxEscalatePerRun + 1)} }, "decide.escalate.max_per_run"},
		"max_per_run huge":               {func(c *Config) { c.Decide.Escalate = &Escalate{Enabled: true, MaxPerRun: ptr(math.MaxInt)} }, "decide.escalate.max_per_run"},
	} {
		t.Run(name, func(t *testing.T) {
			c := valid()
			tc.mut(&c)
			err := c.Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("Validate = %v, want valid", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("Validate = %v, want %q", err, tc.want)
			}
		})
	}
}

// An explicit zero in the file must be refused, not read as "use the default".
func TestEscalateExplicitZeroInJSONIsRefused(t *testing.T) {
	for _, body := range []string{`{"enabled": true, "below_p": 0}`, `{"enabled": true, "max_per_run": 0}`} {
		var m map[string]any
		if err := json.Unmarshal([]byte(livePlainConfig), &m); err != nil {
			t.Fatal(err)
		}
		var esc map[string]any
		if err := json.Unmarshal([]byte(body), &esc); err != nil {
			t.Fatal(err)
		}
		m["decide"].(map[string]any)["escalate"] = esc
		data, _ := json.Marshal(m)
		if _, err := Parse(data); err == nil || !strings.Contains(err.Error(), "decide.escalate") {
			t.Fatalf("Parse(%s) = %v, want a decide.escalate refusal", body, err)
		}
	}
	// An unknown key in the section is refused like every other section.
	var m map[string]any
	_ = json.Unmarshal([]byte(livePlainConfig), &m)
	m["decide"].(map[string]any)["escalate"] = map[string]any{"enabled": true, "tau": 0.4}
	data, _ := json.Marshal(m)
	if _, err := Parse(data); err == nil {
		t.Fatal("an unknown decide.escalate key was accepted")
	}
}

// A config written before these keys existed loads, validates and saves back
// without gaining them: every save must not add keys to existing configs.
func TestLiveStyleConfigRoundTripsUnchanged(t *testing.T) {
	c, err := Parse([]byte(livePlainConfig))
	if err != nil {
		t.Fatal(err)
	}
	if c.LLM.TriageModel != "" || c.Decide.Escalate != nil {
		t.Fatalf("absent keys read as %q / %+v", c.LLM.TriageModel, c.Decide.Escalate)
	}
	if c.LLM.EffectiveTriageModel() != "claude-opus-5-5" || c.Decide.EscalateEnabled() {
		t.Fatal("an existing config changed behaviour")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"triage_model", "escalate", "below_p", "max_per_run"} {
		if strings.Contains(string(data), key) {
			t.Errorf("saving an old config wrote %q", key)
		}
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(c)
	b, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Errorf("round trip changed the config:\n%s\n%s", a, b)
	}
}

func TestEscalateRoundTrip(t *testing.T) {
	c := valid()
	c.LLM = LLM{Provider: "anthropic", Model: "claude-opus-5-5", TriageModel: "claude-haiku-5-5", InputNanoUSDPerToken: 4000, OutputNanoUSDPerToken: 20000}
	c.Decide.Escalate = &Escalate{Enabled: true, BelowP: ptr(0.4), MaxPerRun: ptr(120)}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.LLM.TriageModel != "claude-haiku-5-5" || !got.Decide.EscalateEnabled() ||
		got.Decide.EscalateBelowP() != 0.4 || got.Decide.EscalateMaxPerRun() != 120 {
		t.Fatalf("round trip lost the section: %+v %+v", got.LLM, got.Decide.Escalate)
	}
	var raw map[string]map[string]any
	data, _ := os.ReadFile(path)
	_ = json.Unmarshal(data, &raw)
	if raw["llm"]["triage_model"] != "claude-haiku-5-5" {
		t.Errorf("llm.triage_model not written: %v", raw["llm"])
	}
	esc, _ := raw["decide"]["escalate"].(map[string]any)
	if esc["enabled"] != true || esc["below_p"] != 0.4 || esc["max_per_run"] != float64(120) {
		t.Errorf("decide.escalate written as %v", esc)
	}
}
