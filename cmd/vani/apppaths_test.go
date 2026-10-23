package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
)

// withAppSettings points HOME at a temp dir whose Mac app settings name body,
// with neither VANI_CONFIG nor VANI_STATE_DIR set.
func withAppSettings(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VANI_CONFIG", "")
	t.Setenv("VANI_STATE_DIR", "")
	p := config.AppSettingsPath(home)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// A vani that falls back to the default pair while the Mac app runs on
// another refuses every command, before reading or writing anything, and says
// how to point it at the app's pair.
func TestDefaultPairRefusedWhenTheAppUsesAnother(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the Mac app's settings are only checked on macOS")
	}
	home := withAppSettings(t, `{"vaniConfig":"/elsewhere/config.json","vaniStateDir":"/elsewhere/state"}`)
	_, e := mustCode(t, exitRuntime, "status")
	if !strings.Contains(e, "VANI_CONFIG=/elsewhere/config.json VANI_STATE_DIR=/elsewhere/state") {
		t.Fatalf("stderr %q", e)
	}
	if _, err := os.Stat(filepath.Join(home, "Library", "Application Support", "Vani")); !os.IsNotExist(err) {
		t.Fatalf("the default pair was touched: %v", err)
	}
}

// Status says where each path came from and how it compares with the app.
func TestStatusNamesWherePathsCameFrom(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the Mac app's settings are only checked on macOS")
	}
	withAppSettings(t, `{"vaniConfig":"","vaniStateDir":""}`)
	mustCode(t, 0, "init")
	o, _ := mustCode(t, 0, "status")
	for _, want := range []string{"config: ", "config.json (default)", "state dir: ", "/state (default)", "app settings: agrees"} {
		if !strings.Contains(o, want) {
			t.Errorf("status lacks %q:\n%s", want, o)
		}
	}
	j, _ := mustCode(t, 0, "status", "--json")
	var s struct {
		Paths *struct {
			ConfigFrom, StateFrom, AppSettings string
		} `json:"paths"`
	}
	if err := json.Unmarshal([]byte(j), &s); err != nil {
		t.Fatal(err)
	}
	if s.Paths == nil {
		t.Fatalf("no paths in %s", j)
	}
	var raw map[string]map[string]any
	_ = json.Unmarshal([]byte(j), &raw)
	if p := raw["paths"]; p["config_from"] != "default" || p["state_dir_from"] != "default" || p["app_settings"] != "agrees" {
		t.Errorf("paths %v", p)
	}
}

// Under the tests' setup both variables are set, so the app's settings are
// never consulted and status says so.
func TestStatusFromEnvIsNotChecked(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	o, _ := mustCode(t, 0, "status")
	if !strings.Contains(o, "(VANI_CONFIG)") || !strings.Contains(o, "(VANI_STATE_DIR)") || !strings.Contains(o, "app settings: not checked") {
		t.Errorf("status:\n%s", o)
	}
}

// Unreadable app settings warn on stderr and the command still runs.
func TestUnreadableAppSettingsWarn(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the Mac app's settings are only checked on macOS")
	}
	withAppSettings(t, `{"vaniConfig":`)
	_, e := mustCode(t, 0, "init")
	if !strings.Contains(e, "vani: warning: the Mac app's settings could not be read") {
		t.Errorf("stderr %q", e)
	}
}
