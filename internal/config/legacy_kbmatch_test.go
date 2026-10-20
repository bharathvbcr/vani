package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// A config written before 2026-10-20 says kbmatch.backend "jev"; it loads as
// "openrouter", is reported as deprecated, and Save writes only the new value.
func TestLegacyKBMatchBackendJevLoadsAsOpenRouter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := Default()
	c.KBMatch.Backend = KBMatchOpenRouter
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	saved := readFile(t, path)
	if !strings.Contains(saved, `"backend": "openrouter"`) {
		t.Fatalf("saved config lacks the openrouter backend:\n%s", saved)
	}
	writeFile(t, path, strings.Replace(saved, `"backend": "openrouter"`, `"backend": "jev"`, 1))

	got, old, err := LoadWithDeprecations(path)
	if err != nil {
		t.Fatalf("an old jev backend must still load: %v", err)
	}
	if got.KBMatch.Backend != KBMatchOpenRouter || got.KBMatch.EffectiveBackend() != KBMatchOpenRouter {
		t.Errorf("backend = %q, want %q", got.KBMatch.Backend, KBMatchOpenRouter)
	}
	if len(old) != 1 || !strings.Contains(old[0], "kbmatch.backend") {
		t.Errorf("deprecations = %q, want the kbmatch backend rename", old)
	}
	if err := Save(path, got); err != nil {
		t.Fatal(err)
	}
	if data := readFile(t, path); strings.Contains(data, `"jev"`) {
		t.Errorf("Save rewrote the old value:\n%s", data)
	}
	if _, old, err := LoadWithDeprecations(path); err != nil || len(old) != 0 {
		t.Errorf("reload: %v, deprecations %q", err, old)
	}
}

func TestKBMatchRejectsUnknownBackendAndFields(t *testing.T) {
	var k KBMatch
	if err := k.UnmarshalJSON([]byte(`{"backend":"openrouter","extra":1}`)); err == nil {
		t.Error("an unknown kbmatch field must be refused")
	}
	if err := k.UnmarshalJSON([]byte(`{"backend":"gpt"}`)); err != nil || k.Backend != "gpt" {
		t.Errorf("an unknown backend is Validate's to refuse, not the decoder's: %v %q", err, k.Backend)
	}
	for in, want := range map[string]string{"jev": KBMatchOpenRouter, "openrouter": KBMatchOpenRouter, "llm": KBMatchLLM, "": ""} {
		if got := NormalizeKBMatchBackend(in); got != want {
			t.Errorf("NormalizeKBMatchBackend(%q) = %q, want %q", in, got, want)
		}
	}
}
