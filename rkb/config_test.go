package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Config lookup: the embedded default, then one user file layered over it
// (<skill>/rkb/assets/config.json, else ~/.config/rkb/config.json). A user
// writes only the keys they change; a broken file stops rkb rather than
// silently falling back to the default.

func restoreConfig(t *testing.T) {
	t.Helper()
	saved, savedSrc := cfg, configSource
	t.Cleanup(func() { cfg, configSource = saved, savedSrc })
}

func writeCfg(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConfigOverlayKeepsDefaultForOmittedKeys(t *testing.T) {
	restoreConfig(t)
	if _, err := loadConfigFrom(""); err != nil {
		t.Fatal(err)
	}
	def := cfg
	if def.Bank == "" || def.Rules.MaxWords == 0 {
		t.Fatalf("embedded default lost its paths or rules: %+v", def)
	}
	p := filepath.Join(t.TempDir(), "config.json")
	writeCfg(t, p, `{"bullet_rules": {"codenames": ["Orion", "Vega"]}, "tex_labels": {"Resume": "General"}}`)
	src, err := loadConfigFrom(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(src, p) {
		t.Errorf("source %q does not name the file", src)
	}
	if got := strings.Join(cfg.Rules.Codenames, ","); got != "Orion,Vega" {
		t.Errorf("overlay key not applied: %q", got)
	}
	if cfg.TexLabels["Resume"] != "General" {
		t.Errorf("overlay map not applied: %v", cfg.TexLabels)
	}
	if cfg.Bank != def.Bank || cfg.Rules.MaxWords != def.Rules.MaxWords || cfg.ResumesDir != def.ResumesDir {
		t.Errorf("keys the file leaves out must keep the default: bank %q/%q max_words %d/%d", cfg.Bank, def.Bank, cfg.Rules.MaxWords, def.Rules.MaxWords)
	}
	// A second load starts from the default again: nothing carries over.
	if _, err := loadConfigFrom(""); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Rules.Codenames) != 0 || len(cfg.TexLabels) != 0 {
		t.Errorf("a reload must reset to the default: %v %v", cfg.Rules.Codenames, cfg.TexLabels)
	}
}

func TestConfigBrokenFileIsAnError(t *testing.T) {
	restoreConfig(t)
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	writeCfg(t, bad, `{"bullet_rules": {"codenames": [`)
	if _, err := loadConfigFrom(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Errorf("malformed config must fail naming the file: %v", err)
	}
	wrongType := filepath.Join(dir, "type.json")
	writeCfg(t, wrongType, `{"bullet_rules": {"codenames": "Orion"}}`)
	if _, err := loadConfigFrom(wrongType); err == nil {
		t.Error("a key of the wrong type must fail")
	}
	if _, err := loadConfigFrom(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("a named file that cannot be read must fail")
	}
	unreadable := filepath.Join(dir, "unreadable.json")
	writeCfg(t, unreadable, `{}`)
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() != 0 {
		if _, err := loadConfigFrom(unreadable); err == nil {
			t.Error("an unreadable config must fail, not fall back to the default")
		}
	}
}

// v1 matched config keys ignoring case; v2 matches them exactly. A key in the
// wrong case, at any depth, must stop rkb rather than be ignored, so a rule the
// person added by hand cannot silently not apply.
func TestConfigMiscasedKeyIsAnError(t *testing.T) {
	restoreConfig(t)
	dir := t.TempDir()
	for name, body := range map[string]string{
		"top":    `{"Bank": "x.json"}`,
		"nested": `{"bullet_rules": {"Codenames": ["Orion"]}}`,
		"layout": `{"layout": {"Margin_In": {"top": 0.5}}}`,
	} {
		p := filepath.Join(dir, name+".json")
		writeCfg(t, p, body)
		_, err := loadConfigFrom(p)
		if err == nil || !strings.Contains(err.Error(), "case-sensitive") || !strings.Contains(err.Error(), p) {
			t.Errorf("%s: got %v, want a case-sensitive key error naming the file", name, err)
		}
	}
	ok := filepath.Join(dir, "ok.json")
	writeCfg(t, ok, `{"_about": "mine", "bullet_rules": {"codenames": ["Orion"]}}`)
	if _, err := loadConfigFrom(ok); err != nil {
		t.Fatalf("exact keys must load: %v", err)
	}
	if len(cfg.Rules.Codenames) != 1 {
		t.Fatalf("codenames = %v", cfg.Rules.Codenames)
	}
}

// The same holds for every file read through decodeJSONStrict (claims
// exports, reviewer sheets, verifier answers, the verify log).
func TestStrictReadMiscasedKeyIsAnError(t *testing.T) {
	var l VerifyLog
	err := decodeJSONStrict("log.json", []byte(`{"clmA": [{"pass": 1, "Verdict": "CONFIRMED"}]}`), &l)
	if err == nil || !strings.Contains(err.Error(), `$.clmA[0]`) || !strings.Contains(err.Error(), `"verdict"`) {
		t.Fatalf("got %v, want an error naming $.clmA[0] and \"verdict\"", err)
	}
	if err := decodeJSONStrict("log.json", []byte(`{"clmA": [{"pass": 1, "verdict": "CONFIRMED", "note_x": 1}]}`), &l); err != nil {
		t.Fatalf("exact keys and unrelated unknown keys must load: %v", err)
	}
}

func TestUserConfigPathOrder(t *testing.T) {
	restoreConfig(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("RKB_SKILL_DIR", "")
	if got := userConfigPath(); got != "" {
		t.Fatalf("no user file anywhere: want none, got %q", got)
	}
	homeCfg := filepath.Join(home, ".config", "rkb", "config.json")
	writeCfg(t, homeCfg, `{}`)
	if got := userConfigPath(); got != homeCfg {
		t.Fatalf("want the home config %q, got %q", homeCfg, got)
	}
	skill := t.TempDir()
	t.Setenv("RKB_SKILL_DIR", skill)
	if got := userConfigPath(); got != homeCfg {
		t.Fatalf("a skill folder without a config must not hide the home config: %q", got)
	}
	skillCfg := filepath.Join(skill, "rkb", "assets", "config.json")
	writeCfg(t, skillCfg, `{}`)
	if got := userConfigPath(); got != skillCfg {
		t.Fatalf("the skill config comes first: want %q, got %q", skillCfg, got)
	}
}
