package config

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// withKBMatch is renamedConfig with a kbmatch section.
func withKBMatch(section string) string {
	return strings.Replace(renamedConfig, `"version": 1,`, `"version": 1, "kbmatch": `+section+`,`, 1)
}

// kbmatch.backend was "jev" until 2026-10-20; it now names the transport,
// "openrouter". Old configs load as the new name and say so.
func TestKBMatchJevBackendLoadsAsOpenRouter(t *testing.T) {
	old, err := Parse([]byte(withKBMatch(`{"backend": "jev"}`)))
	if err != nil {
		t.Fatalf("a config with kbmatch.backend \"jev\" was refused: %v", err)
	}
	renamed, err := Parse([]byte(withKBMatch(`{"backend": "openrouter"}`)))
	if err != nil {
		t.Fatalf("a config with kbmatch.backend \"openrouter\" was refused: %v", err)
	}
	if !reflect.DeepEqual(old, renamed) {
		t.Fatalf("legacy and renamed kbmatch differ: %+v vs %+v", old.KBMatch, renamed.KBMatch)
	}
	if old.KBMatch.Backend != KBMatchOpenRouter || old.KBMatch.EffectiveBackend() != KBMatchOpenRouter {
		t.Fatalf("backend %q (effective %q), want %q", old.KBMatch.Backend, old.KBMatch.EffectiveBackend(), KBMatchOpenRouter)
	}
	want := `kbmatch.backend "jev" is now "openrouter"`
	if got := Deprecations([]byte(withKBMatch(`{"backend": "jev"}`))); !slices.Contains(got, want) {
		t.Errorf("deprecations %q lack %q", got, want)
	}
	if got := Deprecations([]byte(withKBMatch(`{"backend": "openrouter"}`))); len(got) != 0 {
		t.Errorf("renamed kbmatch reported deprecations %q", got)
	}
	if got := Deprecations([]byte(withKBMatch(`{"backend": "llm"}`))); len(got) != 0 {
		t.Errorf("llm kbmatch reported deprecations %q", got)
	}
}

func TestKBMatchSaveWritesOpenRouter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, withKBMatch(`{"backend": "jev"}`))
	c, old, err := LoadWithDeprecations(path)
	if err != nil || len(old) != 1 {
		t.Fatalf("load: %v, deprecations %q", err, old)
	}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	data := readFile(t, path)
	if strings.Contains(data, `"jev"`) || !strings.Contains(data, `"backend": "openrouter"`) {
		t.Fatalf("saved kbmatch should name openrouter only:\n%s", data)
	}
	if _, old, err := LoadWithDeprecations(path); err != nil || len(old) != 0 {
		t.Fatalf("reloaded saved config: %v, deprecations %q", err, old)
	}
}

// A KBMatch built in Go (a track, a flag) with the old value still validates and
// asks the decision model.
func TestKBMatchLegacyValueInGo(t *testing.T) {
	c := valid()
	c.Decide.OpenRouterModel = "liquid/d1-ultra"
	for _, b := range []string{KBMatchOpenRouter, "jev"} {
		c.KBMatch = KBMatch{Backend: b}
		if err := c.Validate(); err != nil {
			t.Errorf("backend %q: %v", b, err)
		}
		if got := c.KBMatch.EffectiveBackend(); got != KBMatchOpenRouter {
			t.Errorf("backend %q is effectively %q, want %q", b, got, KBMatchOpenRouter)
		}
	}
	c.Decide.OpenRouterModel = ""
	for _, b := range []string{KBMatchOpenRouter, "jev"} {
		c.KBMatch = KBMatch{Backend: b}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), `kbmatch.backend "openrouter" needs decide.openrouter_model`) {
			t.Errorf("backend %q without an openrouter model: %v", b, err)
		}
	}
}

func TestKBMatchSectionStaysStrict(t *testing.T) {
	for name, section := range map[string]string{
		"unknown key":     `{"backend": "jev", "extra": 1}`,
		"upper case":      `{"backend": "JEV"}`,
		"padded":          `{"backend": " jev"}`,
		"other model":     `{"backend": "jev-1.13"}`,
		"not an object":   `"jev"`,
		"backend number":  `{"backend": 1}`,
		"jev with model":  `{"backend": "jev", "model": "claude-haiku-5-5"}`,
		"empty and extra": `{"": "jev"}`,
	} {
		if _, err := Parse([]byte(withKBMatch(section))); err == nil {
			t.Errorf("%s: kbmatch %s was accepted", name, section)
		}
	}
	c, err := Parse([]byte(withKBMatch(`null`)))
	if err != nil || c.KBMatch != (KBMatch{}) {
		t.Errorf("kbmatch null: %v, %+v", err, c.KBMatch)
	}
}
