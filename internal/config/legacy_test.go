package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A config written with the pre-rename keys, as the live one was.
const legacyConfig = `{
  "version": 1,
  "watchlist": [{"ats": "gh", "token": "acme", "company": "Acme"}],
  "allowed_hosts": ["boards-api.greenhouse.io", "openrouter.ai"],
  "schedule": {"daily_at": "08:30", "timezone": "America/Los_Angeles"},
  "decide": {
    "jev_model": "liquid/d1-ultra",
    "jev_key_env": "OPENROUTER_API_KEY",
    "daily_usd": {"jev": 0.5, "llm": 10},
    "shadow": "jev"
  },
  "llm": {"provider": "gemini", "model": "m"},
  "devprism": {"binary": "", "tools_hash": ""},
  "drop_rules": [
    {"slot": "role_fit", "values": ["mismatch_domain"], "min_jev_prob": 0.9, "min_lappi_score": 0},
    {"slot": "seniority", "values": ["staff_plus"], "min_jev_prob": 0.8, "min_lappi_score": 0.2}
  ]
}`

// The same config under the new keys.
const renamedConfig = `{
  "version": 1,
  "watchlist": [{"ats": "gh", "token": "acme", "company": "Acme"}],
  "allowed_hosts": ["boards-api.greenhouse.io", "openrouter.ai"],
  "schedule": {"daily_at": "08:30", "timezone": "America/Los_Angeles"},
  "decide": {
    "openrouter_model": "liquid/d1-ultra",
    "openrouter_key_env": "OPENROUTER_API_KEY",
    "daily_usd": {"openrouter": 0.5, "llm": 10},
    "shadow": "openrouter"
  },
  "llm": {"provider": "gemini", "model": "m"},
  "devprism": {"binary": "", "tools_hash": ""},
  "drop_rules": [
    {"slot": "role_fit", "values": ["mismatch_domain"], "min_openrouter_prob": 0.9, "min_lappi_score": 0},
    {"slot": "seniority", "values": ["staff_plus"], "min_openrouter_prob": 0.8, "min_lappi_score": 0.2}
  ]
}`

func TestLegacyKeysLoadAsTheirNewNames(t *testing.T) {
	old, err := Parse([]byte(legacyConfig))
	if err != nil {
		t.Fatalf("legacy config refused: %v", err)
	}
	renamed, err := Parse([]byte(renamedConfig))
	if err != nil {
		t.Fatalf("renamed config refused: %v", err)
	}
	if !reflect.DeepEqual(old, renamed) {
		t.Fatalf("legacy and renamed configs differ:\nold     %+v\nrenamed %+v", old, renamed)
	}
	d := renamed.Decide
	if d.OpenRouterModel != "liquid/d1-ultra" || d.OpenRouterKeyEnv != "OPENROUTER_API_KEY" || d.DailyUSD.OpenRouter != 0.5 ||
		d.Shadow != ShadowOpenRouter || renamed.DropRules[1].MinOpenRouterProb != 0.8 || d.OpenRouterName() != "d1-ultra" {
		t.Fatalf("decoded %+v rules %+v", d, renamed.DropRules)
	}
}

func TestDeprecationsNameEachOldKey(t *testing.T) {
	got := Deprecations([]byte(legacyConfig))
	want := []string{
		"decide.jev_model is now decide.openrouter_model",
		"decide.jev_key_env is now decide.openrouter_key_env",
		"decide.daily_usd.jev is now decide.daily_usd.openrouter",
		`decide.shadow "jev" is now "openrouter"`,
		"drop_rules min_jev_prob is now min_openrouter_prob (2 rule(s))",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("deprecations\n got %q\nwant %q", got, want)
	}
	if got := Deprecations([]byte(renamedConfig)); len(got) != 0 {
		t.Fatalf("renamed config reported deprecations %q", got)
	}
	if got := Deprecations([]byte("{not json")); got != nil {
		t.Fatalf("unreadable document reported %q", got)
	}
}

func TestLoadWithDeprecationsAndSaveWritesNewNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, legacyConfig)
	c, old, err := LoadWithDeprecations(path)
	if err != nil || len(old) != 5 {
		t.Fatalf("load: %v, deprecations %q", err, old)
	}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	data := readFile(t, path)
	for _, k := range []string{"jev_model", "jev_key_env", `"jev"`, "min_jev_prob"} {
		if strings.Contains(data, k) {
			t.Errorf("saved config still has %s:\n%s", k, data)
		}
	}
	for _, k := range []string{`"openrouter_model"`, `"openrouter_key_env"`, `"openrouter": 0.5`, `"shadow": "openrouter"`, `"min_openrouter_prob": 0.9`} {
		if !strings.Contains(data, k) {
			t.Errorf("saved config lacks %s:\n%s", k, data)
		}
	}
	if _, old, err := LoadWithDeprecations(path); err != nil || len(old) != 0 {
		t.Fatalf("reloaded saved config: %v, deprecations %q", err, old)
	}
}

// An old and a new key that disagree are refused; agreeing ones load.
func TestOldAndNewKeysMustAgree(t *testing.T) {
	cases := []struct{ name, from, to, want string }{
		{"model", `"jev_model": "liquid/d1-ultra",`, `"jev_model": "liquid/d1-ultra", "openrouter_model": "typesafe/jev-1.13",`, "decide.openrouter_model"},
		{"key env", `"jev_key_env": "OPENROUTER_API_KEY",`, `"jev_key_env": "OPENROUTER_API_KEY", "openrouter_key_env": "OTHER_KEY",`, "decide.openrouter_key_env"},
		{"daily", `{"jev": 0.5, "llm": 10}`, `{"jev": 0.5, "openrouter": 0.4, "llm": 10}`, "decide.daily_usd.openrouter"},
		{"min prob", `"min_jev_prob": 0.9,`, `"min_jev_prob": 0.9, "min_openrouter_prob": 0.95,`, "min_openrouter_prob"},
	}
	for _, c := range cases {
		if !strings.Contains(legacyConfig, c.from) {
			t.Fatalf("%s: fixture lacks %q", c.name, c.from)
		}
		_, err := Parse([]byte(strings.Replace(legacyConfig, c.from, c.to, 1)))
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "differ") {
			t.Errorf("%s: err %v, want a %q disagreement", c.name, err, c.want)
		}
	}
	same := strings.Replace(legacyConfig, `"jev_model": "liquid/d1-ultra",`, `"jev_model": "liquid/d1-ultra", "openrouter_model": "liquid/d1-ultra",`, 1)
	if _, err := Parse([]byte(same)); err != nil {
		t.Fatalf("agreeing old and new keys refused: %v", err)
	}
}

// The decoders that read old names must stay as strict as Parse: an unknown
// key anywhere under them is still an error.
func TestLegacyDecodersRejectUnknownFields(t *testing.T) {
	for name, edit := range map[string][2]string{
		"decide":    {`"shadow": "jev"`, `"shadow": "jev", "jev_modle": "x"`},
		"daily_usd": {`{"jev": 0.5, "llm": 10}`, `{"jev": 0.5, "llm": 10, "lappi": 1}`},
		"drop rule": {`"min_jev_prob": 0.9,`, `"min_jev_prob": 0.9, "min_jev_porb": 0.9,`},
		"lappi":     {`"shadow": "jev"`, `"shadow": "jev", "lappi": {"socket": "/tmp/s", "primray": true}`},
	} {
		_, err := Parse([]byte(strings.Replace(legacyConfig, edit[0], edit[1], 1)))
		if err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Errorf("%s: err %v, want unknown field", name, err)
		}
	}
}

func TestBackendName(t *testing.T) {
	for model, want := range map[string]string{
		"liquid/d1-ultra":    "d1-ultra",
		"typesafe/jev-1.13":  "jev-1-13",
		"d1-ultra":           "d1-ultra",
		" Vendor/Model_X:v2": "model_x-v2",
		"a/b/c":              "c",
		"vendor/...":         "",
		"":                   "",
		"vendor/":            "",
	} {
		if got := BackendName(model); got != want {
			t.Errorf("BackendName(%q) = %q, want %q", model, got, want)
		}
	}
}

// The OpenRouter model must leave a usable name that no other backend uses,
// or its readings would be filed as another backend's.
func TestOpenRouterModelNameRules(t *testing.T) {
	for model, want := range map[string]string{
		"vendor/...":                      "leaves no backend name",
		"acme/local":                      `"local" is taken`,
		"acme/llm":                        `"llm" is taken`,
		"acme/lappi":                      `"lappi" is taken`,
		"acme/unknown":                    `"unknown" is taken`,
		"acme/" + strings.Repeat("x", 33): "longer than 32 bytes",
	} {
		c := valid()
		c.Decide.OpenRouterModel = model
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err %v, want %q", model, err, want)
		}
	}
	c := valid()
	c.Decide.OpenRouterModel = "acme/" + strings.Repeat("x", 32)
	if err := c.Validate(); err != nil {
		t.Fatalf("32-byte name refused: %v", err)
	}
}

func TestShadowOpenRouterNeedsModel(t *testing.T) {
	c := valid()
	c.Decide.OpenRouterModel = ""
	c.Decide.Shadow = ShadowOpenRouter
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "decide.shadow openrouter needs decide.openrouter_model") {
		t.Fatalf("err %v", err)
	}
	var d Decide
	if err := json.Unmarshal([]byte(`{"shadow": "jev"}`), &d); err != nil || d.Shadow != ShadowOpenRouter {
		t.Fatalf("shadow jev read as %q (%v)", d.Shadow, err)
	}
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
