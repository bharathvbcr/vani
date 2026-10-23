package config

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// kbmatch.backend "jev" (its name until 2026-10-20) loads as "liquid", is
// reported as deprecated, and is saved as "liquid".
func TestKBMatchBackendJevIsLiquid(t *testing.T) {
	var k KBMatch
	if err := json.Unmarshal([]byte(`{"backend": "jev"}`), &k); err != nil || k.Backend != KBMatchLiquid {
		t.Fatalf("jev: %+v %v", k, err)
	}
	for _, b := range []string{"liquid", "llm", ""} {
		raw := `{"backend": "` + b + `"}`
		if err := json.Unmarshal([]byte(raw), &k); err != nil || k.Backend != b {
			t.Fatalf("%q: %+v %v", b, k, err)
		}
	}
	if err := json.Unmarshal([]byte(`{"backend": "jev", "extra": 1}`), &k); err == nil {
		t.Fatal("an unknown kbmatch key was accepted")
	}
	if err := json.Unmarshal([]byte(`{"backend": "JEV"}`), &k); err != nil || k.Backend != "JEV" {
		t.Fatalf("only the exact old name is renamed: %+v %v", k, err)
	}

	doc := strings.Replace(legacyConfig, `"version": 1,`, `"version": 1, "kbmatch": {"backend": "jev"},`, 1)
	got := Deprecations([]byte(doc))
	if want := `kbmatch.backend "jev" is now "liquid"`; len(got) == 0 || got[len(got)-1] != want {
		t.Fatalf("deprecations %q, want last %q", got, want)
	}
	if got := Deprecations([]byte(`{"kbmatch": {"backend": "liquid"}}`)); len(got) != 0 {
		t.Fatalf("liquid reported as deprecated: %q", got)
	}

	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, doc)
	c, _, err := LoadWithDeprecations(path)
	if err != nil || c.KBMatch.Backend != KBMatchLiquid || c.KBMatch.EffectiveBackend() != KBMatchLiquid {
		t.Fatalf("load: %+v %v", c.KBMatch, err)
	}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	if data := readFile(t, path); !strings.Contains(data, `"backend": "liquid"`) || strings.Contains(data, `"backend": "jev"`) {
		t.Errorf("saved kbmatch:\n%s", data)
	}
	again, old, err := LoadWithDeprecations(path)
	if err != nil || !reflect.DeepEqual(again.KBMatch, c.KBMatch) || len(old) != 0 {
		t.Fatalf("reload: %+v %q %v", again.KBMatch, old, err)
	}
}
