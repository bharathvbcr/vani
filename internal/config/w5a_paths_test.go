package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// A relative VANI_CONFIG or VANI_STATE_DIR is refused, naming the variable:
// the config and state would otherwise depend on the starting process's
// working directory (launchd and the app have none that means anything).
func TestW5aResolveRefusesRelativeEnvPaths(t *testing.T) {
	home := t.TempDir()
	abs := filepath.Join(home, "x")
	for _, c := range []struct {
		name string
		env  map[string]string
		want string // variable named in the refusal
	}{
		{"config relative", map[string]string{"VANI_CONFIG": "c.json", "VANI_STATE_DIR": abs}, "VANI_CONFIG"},
		{"config dot-relative", map[string]string{"VANI_CONFIG": "./c.json"}, "VANI_CONFIG"},
		{"config tilde literal", map[string]string{"VANI_CONFIG": "~/c.json"}, "VANI_CONFIG"},
		{"state relative", map[string]string{"VANI_CONFIG": filepath.Join(abs, "c.json"), "VANI_STATE_DIR": "state"}, "VANI_STATE_DIR"},
		{"state parent-relative", map[string]string{"VANI_STATE_DIR": "../state"}, "VANI_STATE_DIR"},
		{"state tilde literal", map[string]string{"VANI_STATE_DIR": "~/state"}, "VANI_STATE_DIR"},
		{"state dot", map[string]string{"VANI_STATE_DIR": "."}, "VANI_STATE_DIR"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Resolve(envOf(c.env), home, "darwin")
			if err == nil {
				t.Fatal("a relative path was accepted")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("refusal %q does not name %s", err, c.want)
			}
		})
	}
}

func TestW5aResolveAcceptsAbsoluteAndCleansDotDot(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, "a")
	r, err := Resolve(envOf(map[string]string{
		"VANI_CONFIG":    base + "/b/../c.json",
		"VANI_STATE_DIR": base + "/s/./x/..",
	}), home, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	if r.ConfigPath != filepath.Join(base, "c.json") || r.StateDir != filepath.Join(base, "s") {
		t.Errorf("not cleaned: %q %q", r.ConfigPath, r.StateDir)
	}
	if r.ConfigFrom != FromEnv || r.StateFrom != FromEnv {
		t.Errorf("from: %q %q", r.ConfigFrom, r.StateFrom)
	}
}

func TestW5aResolveEmptyIsDefault(t *testing.T) {
	home := t.TempDir()
	r, err := Resolve(envOf(map[string]string{"VANI_CONFIG": "", "VANI_STATE_DIR": ""}), home, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if r.ConfigFrom != FromDefault || r.StateFrom != FromDefault ||
		!filepath.IsAbs(r.ConfigPath) || !filepath.IsAbs(r.StateDir) {
		t.Errorf("empty variables did not give the default: %+v", r)
	}
}
