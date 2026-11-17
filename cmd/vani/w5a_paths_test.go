package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// run refuses a relative VANI_CONFIG or VANI_STATE_DIR before any command
// runs, naming the variable.
func TestW5aRunRefusesRelativeEnv(t *testing.T) {
	abs := t.TempDir()
	for _, c := range []struct{ name, cfg, state, want string }{
		{"config", "c.json", filepath.Join(abs, "s"), "VANI_CONFIG"},
		{"state", filepath.Join(abs, "c.json"), "state", "VANI_STATE_DIR"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("VANI_CONFIG", c.cfg)
			t.Setenv("VANI_STATE_DIR", c.state)
			var o, e bytes.Buffer
			if code := run([]string{"status"}, &o, &e); code != exitRuntime {
				t.Fatalf("exit %d, want %d (stderr %q)", code, exitRuntime, e.String())
			}
			if !strings.Contains(e.String(), c.want) {
				t.Errorf("stderr %q does not name %s", e.String(), c.want)
			}
		})
	}
}

// status prints and reports the resolved absolute (cleaned) paths.
func TestW5aStatusShowsResolvedPaths(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.json")
	state := filepath.Join(dir, "state")
	t.Setenv("VANI_CONFIG", dir+"/x/../c.json")
	t.Setenv("VANI_STATE_DIR", state+"/./")
	t.Setenv("HOME", t.TempDir())

	var o, e bytes.Buffer
	if code := run([]string{"init"}, &o, &e); code != 0 {
		t.Fatalf("init exit %d: %s", code, e.String())
	}
	o.Reset()
	run([]string{"status"}, &o, &e)
	for _, want := range []string{"config: " + cfg + " (VANI_CONFIG)", "state dir: " + state + " (VANI_STATE_DIR)", "app settings: "} {
		if !strings.Contains(o.String(), want) {
			t.Errorf("status text lacks %q:\n%s", want, o.String())
		}
	}

	o.Reset()
	e.Reset()
	run([]string{"--json", "status"}, &o, &e)
	var s struct {
		StateDir string `json:"state_dir"`
		Paths    struct {
			Config      string `json:"config"`
			AppSettings string `json:"app_settings"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(o.Bytes(), &s); err != nil {
		t.Fatalf("status --json: %v\n%s", err, o.String())
	}
	if s.StateDir != state || s.Paths.Config != cfg || s.Paths.AppSettings == "" {
		t.Errorf("status --json paths: %+v", s)
	}
}
