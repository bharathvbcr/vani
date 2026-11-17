package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// [95] A config that would misbehave later is refused when it loads, naming
// the key; nothing a config holds can panic rkb afterwards.

func w3aLoad(t *testing.T, body string) error {
	t.Helper()
	restoreConfig(t)
	p := filepath.Join(t.TempDir(), "config.json")
	writeCfg(t, p, body)
	_, err := loadConfigFrom(p)
	return err
}

func TestW3AMalformedConfigsAreRefusedAtLoad(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"misspelt top-level key", `{"bnak": "x.json"}`, "bnak"},
		{"misspelt nested key", `{"bullet_rules": {"testing_term": ["unit"]}}`, "testing_term"},
		{"testing term that does not compile", `{"bullet_rules": {"testing_terms": ["unit", "(unclosed"]}}`, "testing_terms"},
		{"number word that does not compile", `{"bullet_rules": {"number_words": ["[a-"]}}`, "number_words"},
		{"extra_banned that does not compile", `{"bullet_rules": {"extra_banned": ["*oops"]}}`, "extra_banned"},
		{"dup_phrases that does not compile", `{"bullet_rules": {"dup_phrases": ["a(b"]}}`, "dup_phrases"},
		{"parity differentiator that does not compile", `{"bullet_rules": {"parity_differentiators": ["x)"]}}`, "parity_differentiators"},
		{"mechanism that does not compile", `{"bullet_rules": {"mechanisms": [{"id": "m", "all": ["(bad"]}]}}`, "mechanisms"},
		{"density range of the wrong type", `{"layout": {"density_range": {"--fs": ["small", 10, "pt"]}}}`, "density_range"},
		{"density range with a non-string unit", `{"layout": {"density_range": {"--fs": [9, 10, 7]}}}`, "density_range"},
	}
	for _, c := range cases {
		err := w3aLoad(t, c.body)
		if err == nil {
			t.Errorf("%s: the config loaded", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: the error must name %q: %v", c.name, c.want, err)
		}
	}
}

func TestW3AGoodConfigStillLoads(t *testing.T) {
	err := w3aLoad(t, `{"bullet_rules": {"testing_terms": ["unit test", "pytest"], "extra_banned": ["very\\s+good"], "codenames": ["Orion"],
		"mechanisms": [{"id": "cache", "all": ["cach", "hit"]}]}, "layout": {"density_range": {"--fs": [9.5, 10.5, "pt"]}}}`)
	if err != nil {
		t.Fatalf("a valid config was refused: %v", err)
	}
	if err := (&cfg).Validate(); err != nil {
		t.Errorf("the embedded default must validate: %v", err)
	}
}

func TestW3ADensityVarsNeverPanic(t *testing.T) {
	restoreConfig(t)
	cfg.Layout.DensityRanges = map[string][3]interface{}{"--a": {"x", nil, 3}, "--b": {1.0, 2.0, "pt"}}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("densityVars panicked on a malformed range: %v", r)
		}
	}()
	_ = densityVars(0.5)
}

// A hand-written \clm with no macro name used to index a nil match.
func TestW3ATexConvBareClmDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("texToMD panicked on a bare \\clm: %v", r)
		}
	}()
	for _, s := range []string{`a \clm b`, `\clm`, `\clm*`, `\clm{}`} {
		_ = texToMD(s, map[string]bool{})
	}
}
