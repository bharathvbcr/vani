package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Validate agrees with egress about which required hosts the allowlist covers:
// a wildcard covers the hosts under it (egress allows them), but not its bare
// suffix; case and a trailing dot do not matter.
func TestValidateRequiredHostAgreesWithEgress(t *testing.T) {
	for name, tc := range map[string]struct {
		hosts   []string
		missing bool
	}{
		"wildcard covers the board host":    {[]string{"*.greenhouse.io", "api.eu.lever.co", "api.ashbyhq.com", "openrouter.ai"}, false},
		"wildcard does not cover the bare":  {[]string{"*.boards-api.greenhouse.io", "api.eu.lever.co", "api.ashbyhq.com", "openrouter.ai"}, true},
		"upper case and trailing dot":       {[]string{"Boards-API.Greenhouse.IO.", "API.eu.lever.co", "api.ashbyhq.com", "OpenRouter.ai"}, false},
		"wildcard with trailing dot":        {[]string{"*.GREENHOUSE.io.", "api.eu.lever.co", "api.ashbyhq.com", "openrouter.ai"}, false},
		"unrelated wildcard":                {[]string{"*.example.com", "api.eu.lever.co", "api.ashbyhq.com", "openrouter.ai"}, true},
		"invalid entry is not an allowance": {[]string{"https://boards-api.greenhouse.io", "api.eu.lever.co", "api.ashbyhq.com", "openrouter.ai"}, true},
	} {
		c := valid()
		c.AllowedHosts = tc.hosts
		err := c.Validate()
		got := err != nil && strings.Contains(err.Error(), "allowed_hosts is missing")
		if got != tc.missing {
			t.Errorf("%s: missing=%v, want %v (err %v)", name, got, tc.missing, err)
		}
	}
}

// A config that does not exist yet, spelled through a symlinked directory, is
// created in the resolved directory and locked beside it.
func TestCreateThroughSymlinkedDirectory(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	if err := Create(filepath.Join(linkDir, "config.json"), valid()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(realDir, "config.json")); err != nil {
		t.Errorf("config not created in the resolved directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(realDir, "config.json"+lockSuffix)); err != nil {
		t.Errorf("lock not beside the resolved file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(linkDir, "config.json")); err != nil {
		t.Errorf("config not reachable through the link: %v", err)
	}
}

// UpdateWithDeprecations reports the old key names the file used before the
// rewrite, including when fn then fails; a file it could not read reports none.
func TestUpdateWithDeprecationsReportsOldKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(legacyConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := UpdateWithDeprecations(path, func(c *Config) error { c.MotivationNotes = "n"; return nil })
	if err != nil || !slices.ContainsFunc(old, func(s string) bool { return strings.Contains(s, "jev_model") }) {
		t.Fatalf("old %v err %v, want the jev_model deprecation", old, err)
	}
	// The rewrite used the new names, so a second update has nothing to warn about.
	if old, err = UpdateWithDeprecations(path, func(*Config) error { return nil }); err != nil || len(old) != 0 {
		t.Errorf("second update: old %v err %v, want none", old, err)
	}
	if old, err = UpdateWithDeprecations(filepath.Join(t.TempDir(), "missing.json"), func(*Config) error { return nil }); err == nil || len(old) != 0 {
		t.Errorf("missing file: old %v err %v, want an error and no list", old, err)
	}
}
