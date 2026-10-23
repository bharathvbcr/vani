package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// appHome is a temp home whose Mac app settings file holds body ("" writes
// none).
func appHome(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	if body != "" {
		p := AppSettingsPath(home)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestAppSettingsPath(t *testing.T) {
	want := "/h/Library/Group Containers/HAUHVD8M37.dev.vbcr.vani/apply/draft-settings.json"
	if got := AppSettingsPath("/h"); got != want {
		t.Fatalf("%q", got)
	}
}

func TestResolveAgainstAppSettings(t *testing.T) {
	const live, liveState = "/repo/.state/config.json", "/repo/.state/state"
	defaults := func(home string) (string, string) { return pathsFor("darwin", envOf(nil), home) }
	for _, c := range []struct {
		name     string
		settings string // "" writes no file; "%C"/"%S" become the home's defaults
		env      map[string]string
		mismatch bool
		app      string // AppSettings prefix
		warn     bool
	}{
		{name: "absent", app: "absent"},
		{name: "empty settings mean the default", settings: `{"vaniConfig":"","vaniStateDir":"","anthropicAccount":"x"}`, app: "agrees"},
		{name: "settings naming the defaults", settings: `{"vaniConfig":"%C","vaniStateDir":"%S"}`, app: "agrees"},
		{name: "settings naming them uncleaned", settings: `{"vaniConfig":"%C/","vaniStateDir":"%S/./"}`, app: "agrees"},
		{name: "both differ", settings: `{"vaniConfig":"` + live + `","vaniStateDir":"` + liveState + `"}`, mismatch: true, app: "names " + live},
		{name: "config differs only", settings: `{"vaniConfig":"` + live + `"}`, mismatch: true, app: "names " + live},
		{name: "state differs only", settings: `{"vaniStateDir":"` + liveState + `"}`, mismatch: true, app: "names "},
		{name: "both from env: not compared", settings: `{"vaniConfig":"` + live + `","vaniStateDir":"` + liveState + `"}`,
			env: map[string]string{"VANI_CONFIG": "/x/c.json", "VANI_STATE_DIR": "/x/s"}, app: "not checked"},
		{name: "env config agrees, default state differs", settings: `{"vaniConfig":"` + live + `","vaniStateDir":"` + liveState + `"}`,
			env: map[string]string{"VANI_CONFIG": live}, mismatch: true, app: "names "},
		{name: "env state agrees, default config differs", settings: `{"vaniConfig":"` + live + `","vaniStateDir":"` + liveState + `"}`,
			env: map[string]string{"VANI_STATE_DIR": liveState}, mismatch: true, app: "names "},
		{name: "env state, config agrees by default", settings: `{"vaniStateDir":"` + liveState + `"}`,
			env: map[string]string{"VANI_STATE_DIR": liveState}, app: "agrees"},
		{name: "malformed", settings: `{"vaniConfig":`, app: "unreadable", warn: true},
		{name: "wrong type", settings: `{"vaniConfig":42}`, app: "unreadable", warn: true},
		{name: "relative path", settings: `{"vaniConfig":"rel/config.json"}`, app: "unreadable", warn: true},
		{name: "oversized", settings: `{"pad":"` + strings.Repeat("x", 1<<20) + `"}`, app: "unreadable", warn: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			dc, ds := defaults(home)
			body := strings.NewReplacer("%C", dc, "%S", ds).Replace(c.settings)
			if body != "" {
				p := AppSettingsPath(home)
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			r, err := Resolve(envOf(c.env), home, "darwin")
			if c.mismatch != errors.Is(err, ErrAppPairMismatch) {
				t.Fatalf("err = %v, want mismatch %v", err, c.mismatch)
			}
			if c.mismatch && !strings.Contains(err.Error(), "Export VANI_CONFIG=") {
				t.Errorf("refusal does not say how to proceed: %v", err)
			}
			if !c.mismatch && err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(r.AppSettings, c.app) {
				t.Errorf("AppSettings %q, want prefix %q", r.AppSettings, c.app)
			}
			if (r.Warning != "") != c.warn {
				t.Errorf("warning %q, want one %v", r.Warning, c.warn)
			}
		})
	}
}

// The refusal names both the app's pair and the resolved one, filling an
// unnamed side with the resolved path.
func TestResolveRefusalText(t *testing.T) {
	home := appHome(t, `{"vaniConfig":"/repo/.state/config.json"}`)
	_, ds := pathsFor("darwin", envOf(nil), home)
	_, err := Resolve(envOf(nil), home, "darwin")
	if err == nil {
		t.Fatal("no refusal")
	}
	for _, want := range []string{"config /repo/.state/config.json and state dir " + ds, "VANI_CONFIG=/repo/.state/config.json VANI_STATE_DIR=" + ds, AppSettingsPath(home)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
}

func TestResolveSourcesAndPlatforms(t *testing.T) {
	home := appHome(t, `{"vaniConfig":"/repo/.state/config.json","vaniStateDir":"/repo/.state/state"}`)
	for _, goos := range []string{"linux", "windows"} {
		r, err := Resolve(envOf(nil), home, goos)
		if err != nil || r.AppSettings != "not checked" || r.ConfigFrom != FromDefault || r.StateFrom != FromDefault {
			t.Errorf("%s: %+v %v", goos, r, err)
		}
	}
	r, err := Resolve(envOf(map[string]string{"VANI_CONFIG": "/a/c.json", "VANI_STATE_DIR": "/a/s"}), home, "darwin")
	if err != nil || r.ConfigPath != "/a/c.json" || r.StateDir != "/a/s" || r.ConfigFrom != FromEnv || r.StateFrom != FromEnv {
		t.Fatalf("%+v %v", r, err)
	}
	// Resolve's paths are Paths' paths.
	c, s := pathsFor("darwin", envOf(nil), home)
	r, _ = Resolve(envOf(nil), t.TempDir(), "darwin")
	if r.ConfigFrom != FromDefault || filepath.Base(r.ConfigPath) != filepath.Base(c) || filepath.Base(r.StateDir) != filepath.Base(s) {
		t.Fatalf("%+v", r)
	}
}

// An unreadable settings file (a directory where the file should be) skips
// the check with a warning: never a refusal, never silence.
func TestResolveUnreadableSettings(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(AppSettingsPath(home), 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := Resolve(envOf(nil), home, "darwin")
	if err != nil || r.Warning == "" || !strings.HasPrefix(r.AppSettings, "unreadable") {
		t.Fatalf("%+v %v", r, err)
	}
}
