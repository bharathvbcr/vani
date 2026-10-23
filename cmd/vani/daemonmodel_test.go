package main

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
)

// A daemon started without the decision model's key still runs, and says so:
// daemon.json carries the model's state and status reports it. The key's value
// is never written anywhere.

const keySentinel = "sk-or-v1-SENTINEL-must-never-reach-a-file"

func configWithModel(env string) config.Config {
	var cfg config.Config
	cfg.Decide.OpenRouterModel, cfg.Decide.OpenRouterKeyEnv = "liquid/d1-ultra", env
	return cfg
}

func TestDaemonModelOf(t *testing.T) {
	get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	if m := daemonModelOf(config.Config{}, get(nil)); m != nil {
		t.Errorf("no OpenRouter model configured: %+v, want nil (there is no key to lack)", m)
	}
	cfg := configWithModel("OPENROUTER_API_KEY")
	if m := daemonModelOf(cfg, get(map[string]string{"OPENROUTER_API_KEY": keySentinel})); m == nil || !m.Enabled || m.Reason != "" || m.KeyEnv != "OPENROUTER_API_KEY" {
		t.Errorf("key present: %+v", m)
	}
	for name, env := range map[string]map[string]string{
		"unset":       nil,
		"empty":       {"OPENROUTER_API_KEY": ""},
		"whitespace":  {"OPENROUTER_API_KEY": " \n\t"},
		"wrong name":  {"OPENROUTER_KEY": keySentinel},
		"anthropic's": {"ANTHROPIC_API_KEY": keySentinel},
	} {
		m := daemonModelOf(cfg, get(env))
		if m == nil || m.Enabled {
			t.Errorf("%s: %+v, want disabled", name, m)
			continue
		}
		for _, want := range []string{"OPENROUTER_API_KEY", "liquid/d1-ultra", "disabled"} {
			if !strings.Contains(m.Reason, want) {
				t.Errorf("%s: reason %q lacks %q", name, m.Reason, want)
			}
		}
		if strings.Contains(m.Reason, "SENTINEL") {
			t.Errorf("%s: the reason holds a key: %q", name, m.Reason)
		}
	}
	// A key variable the config renamed is the one asked about.
	if m := daemonModelOf(configWithModel("MY_KEY"), get(map[string]string{"OPENROUTER_API_KEY": keySentinel})); m == nil || m.Enabled || m.KeyEnv != "MY_KEY" {
		t.Errorf("renamed variable: %+v", m)
	}
}

// runDaemonOnce starts `vani daemon` with a fake app and returns what status
// said while it ran, and what the daemon wrote to stderr.
func runDaemonOnce(t *testing.T) (during statusJSON, textDuring, stderr string) {
	t.Helper()
	stateDir := os.Getenv("VANI_STATE_DIR")
	l := &brokenLedger{memLedger: fixture()}
	wireDaemon(t, l, func(context.Context) error {
		during = statusOf(t)
		textDuring, _ = mustCode(t, 0, "status")
		raw, err := os.ReadFile(runsPath(stateDir, daemonRecordFile))
		if err != nil || strings.Contains(string(raw), "SENTINEL") {
			t.Errorf("daemon.json while running: err=%v, holds the key: %v", err, strings.Contains(string(raw), "SENTINEL"))
		}
		l.poison(errors.New("stop the daemon"))
		return errors.New("stop")
	})
	_, stderr = mustCode(t, exitRuntime, "daemon")
	return during, textDuring, stderr
}

func TestDaemonWithoutTheKeyRunsWithTheModelDisabledAndSaysSo(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init") // the default config names the OpenRouter model
	t.Setenv("OPENROUTER_API_KEY", "")

	during, text, stderr := runDaemonOnce(t)
	d := during.Daemon
	if !d.Running {
		t.Fatalf("the daemon runs without the key: %+v", d)
	}
	if d.Model == nil || d.Model.Enabled || !strings.Contains(d.Model.Reason, "OPENROUTER_API_KEY is not set") {
		t.Fatalf("status.daemon.model = %+v, want disabled for the missing key", d.Model)
	}
	if !strings.Contains(stderr, "vani: daemon: OPENROUTER_API_KEY is not set") {
		t.Errorf("the daemon did not say so on stderr: %q", stderr)
	}
	for _, want := range []string{"daemon: model disabled: OPENROUTER_API_KEY is not set", "fix: add the key to the login Keychain"} {
		if !strings.Contains(text, want) {
			t.Errorf("text status lacks %q:\n%s", want, text)
		}
	}
	if during.Schedule.Kept == nil || !*during.Schedule.Kept {
		t.Errorf("a daemon with the model disabled still keeps the schedule: %v", during.Schedule.Kept)
	}
}

func TestDaemonWithTheKeyAnnouncesTheModelEnabledAndWritesNoKey(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	t.Setenv("OPENROUTER_API_KEY", keySentinel)

	during, text, stderr := runDaemonOnce(t)
	if m := during.Daemon.Model; m == nil || !m.Enabled || m.Reason != "" {
		t.Fatalf("status.daemon.model = %+v, want enabled", m)
	}
	if strings.Contains(text, "model disabled") {
		t.Errorf("text status warns about a model that is enabled:\n%s", text)
	}
	if strings.Contains(stderr, "SENTINEL") || strings.Contains(text, "SENTINEL") {
		t.Errorf("a key reached stderr or status text:\n%s\n%s", stderr, text)
	}
}

// A daemon.json without `model` (a daemon from before the field) is a running
// daemon whose model state is unknown, never "enabled" and never "disabled".
func TestDaemonRecordWithoutModelIsUnknownNotDisabled(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	announceDaemon(t, stateDir)
	d := statusOf(t).Daemon
	if !d.Running || d.Model != nil {
		t.Errorf("an older daemon's record: %+v, want running with no model state", d)
	}
}

// A newer vani may add fields to daemon.json: the daemon still reads as running
// (strictness belongs to request.json, which runs something), and a request
// with an unknown key is still refused.
func TestDaemonRecordToleratesFieldsANewerVaniAdded(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	path := runsPath(stateDir, daemonRecordFile)
	if err := os.MkdirAll(runsPath(stateDir, ""), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := `{"v":1,"pid":` + strconv.Itoa(os.Getpid()) + `,"started_at":"2026-10-22T10:00:00Z","model":{"enabled":false,"reason":"x"},"from_the_future":[1,2]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if d, err := readDaemonRecord(stateDir); err != nil || d.PID != os.Getpid() || d.Model == nil || d.Model.Enabled {
		t.Fatalf("readDaemonRecord: %+v, %v", d, err)
	}
	// Still one object: trailing data is corrupt, and a request stays strict.
	if err := os.WriteFile(path, []byte(raw+` {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readDaemonRecord(stateDir); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("trailing data: %v, want corrupt", err)
	}
	reqPath := writeRequestFile(t, stateDir, []byte(`{"v":1,"requested_at":"`+time.Now().UTC().Format(time.RFC3339)+`","options":{},"extra":1}`))
	if _, err := readRunRequest(reqPath, time.Now()); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("a request with an unknown key: %v, want refused", err)
	}
}

// A record whose process is gone says nothing about the model: nothing runs.
func TestStaleDaemonRecordReportsNoModel(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	old := pidAlive
	t.Cleanup(func() { pidAlive = old })
	pidAlive = func(int) bool { return false }
	if err := writeDaemonAnnouncement(stateDir, daemonRecord{V: 1, PID: 1 << 30, StartedAt: time.Now(), Model: &daemonModel{Reason: "x"}}); err != nil {
		t.Fatal(err)
	}
	d := statusOf(t).Daemon
	if d.Running || d.Model != nil {
		t.Errorf("stale record: %+v", d)
	}
	if lines := daemonLines(d); len(lines) != 0 {
		t.Errorf("a daemon that is not running has no model warning: %v", lines)
	}
}

// The way out the status text names is the app's toggle, which holds the key
// for the daemon; the plain launchd agent stays the fallback, with its caveat.
func TestKeepHintNamesTheToggle(t *testing.T) {
	h := keepHint(configWithModel("OPENROUTER_API_KEY"))
	for _, want := range []string{"Run on schedule", "Pipeline tab", "never written to disk", "vani launchd plist", "EnvironmentVariables"} {
		if !strings.Contains(h, want) {
			t.Errorf("hint lacks %q: %s", want, h)
		}
	}
	if h := keepHint(config.Config{}); !strings.Contains(h, "Run on schedule") || strings.Contains(h, "key") {
		t.Errorf("no model configured: %s", h)
	}
}
