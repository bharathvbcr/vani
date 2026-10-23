package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/vault"
)

// The decision model's key has three owners that must agree: the environment
// (wins), vani's own bounded Keychain read (off with VANI_NO_KEYCHAIN), and the
// Mac app, which reads the Keychain itself and tells every vani it starts not
// to. What a daemon announces (daemon.model) and what status and the schedule
// hint say about it must be what the daemon will do.

const keychainSentinel = "sk-or-v1-KEYCHAIN-SENTINEL-must-never-reach-a-file"

// openRouterKey says where a key came from, and why the Keychain gave none.
func TestOpenRouterKeyProvenance(t *testing.T) {
	envName := config.Default().Decide.OpenRouterKeyEnv

	w, _ := keyWired(t)
	n := fakeKeychain(t, keychainSentinel, nil)
	t.Setenv(envName, "from-env")
	if k, from, why := w.openRouterKey(envName); k != "from-env" || from != keyFromEnvironment || why != "" || n.Load() != 0 {
		t.Errorf("environment: %q %q %q after %d reads", k, from, why, n.Load())
	}

	w, _ = keyWired(t)
	n = fakeKeychain(t, keychainSentinel, nil)
	if k, from, why := w.openRouterKey(envName); k != keychainSentinel || from != keyFromKeychain || why != "" || n.Load() != 1 {
		t.Errorf("keychain: %q %q %q after %d reads", k, from, why, n.Load())
	}

	w, _ = keyWired(t)
	n = fakeKeychain(t, keychainSentinel, nil)
	t.Setenv(noKeychainEnv, "1")
	k, from, why := w.openRouterKey(envName)
	if k != "" || from != "" || !strings.Contains(why, noKeychainEnv) || n.Load() != 0 {
		t.Errorf("opted out: %q %q %q after %d reads", k, from, why, n.Load())
	}

	for name, c := range map[string]struct {
		err  error
		want string
	}{
		"missing":  {vault.ErrNotFound, "there is no such item"},
		"refused":  {fmt.Errorf("%w: x", vault.ErrKeychainDenied), "refused the read"},
		"timeout":  {fmt.Errorf("%w: x", vault.ErrKeychainTimeout), "did not answer within"},
		"no macOS": {fmt.Errorf("%w, which linux does not have", vault.ErrNoKeychain), "no Keychain on this platform"},
	} {
		w, _ := keyWired(t)
		fakeKeychain(t, "", c.err)
		k, from, why := w.openRouterKey(envName)
		if k != "" || from != "" || !strings.Contains(why, c.want) {
			t.Errorf("%s: %q %q %q, want a reason with %q", name, k, from, why, c.want)
		}
		if strings.Contains(why, keychainSentinel) {
			t.Errorf("%s: the reason holds a key", name)
		}
	}
}

// startRun is what makes a failed read retry once per run: it must advance the
// counter the cache compares against.
func TestStartRunAdvancesTheKeychainRun(t *testing.T) {
	clearKeys(t)
	cfg := config.Default()
	cfg.Decide.OpenRouterModel = ""
	w, _ := openWired(t, cfg)
	for want := 1; want <= 2; want++ {
		_, _, stop, err := w.startRun(context.Background(), false)
		if err != nil {
			t.Fatal(err)
		}
		stop()
		if w.keychainRun != want {
			t.Fatalf("after %d starts keychainRun = %d", want, w.keychainRun)
		}
	}
}

// What a daemon announces for each way the key can (not) arrive.
func TestDaemonModelForKeychain(t *testing.T) {
	cfg := configWithModel("OPENROUTER_API_KEY")
	look := func(key, from, why string) keyLookup {
		return func(string) (string, string, string) { return key, from, why }
	}

	if m := daemonModelFor(config.Config{}, look("", "", "")); m != nil {
		t.Errorf("no model configured: %+v", m)
	}
	// The environment wins, and says nothing about the Keychain.
	if m := daemonModelFor(cfg, look(keychainSentinel, keyFromEnvironment, "")); m == nil || !m.Enabled || m.Source != "" || m.Keychain != "" {
		t.Errorf("environment: %+v", m)
	}
	// A key the daemon read from the Keychain itself says so.
	if m := daemonModelFor(cfg, look(keychainSentinel, keyFromKeychain, "")); m == nil || !m.Enabled || m.Source != "keychain" || m.Reason != "" {
		t.Errorf("keychain: %+v", m)
	}
	// No key: the reason carries the Keychain's account of itself.
	for _, why := range []string{
		"the Keychain was not read because VANI_NO_KEYCHAIN is set",
		`the login Keychain item "openrouter-api-key" could not be read: there is no such item`,
	} {
		m := daemonModelFor(cfg, look("", "", why))
		if m == nil || m.Enabled || m.Keychain != why || !strings.Contains(m.Reason, "OPENROUTER_API_KEY is not set in the daemon's environment and "+why) ||
			!strings.Contains(m.Reason, "disabled") {
			t.Errorf("%q: %+v", why, m)
		}
	}
	// The env-only form is unchanged.
	if m := daemonModelOf(cfg, func(string) string { return "" }); m == nil || m.Keychain != "" ||
		!strings.Contains(m.Reason, "is not set in the daemon's environment, so the decision model") {
		t.Errorf("env-only: %+v", m)
	}
}

// daemonLines names the Keychain only when the daemon looked there.
func TestDaemonLinesNameTheKeychainWhenTheDaemonLooked(t *testing.T) {
	cfg := configWithModel("OPENROUTER_API_KEY")
	looked := daemonModelFor(cfg, func(string) (string, string, string) {
		return "", "", "the Keychain was not read because VANI_NO_KEYCHAIN is set"
	})
	lines := daemonLines(daemonFacet{Running: true, Model: looked})
	if len(lines) != 2 || !strings.HasPrefix(lines[1], "  fix: add the key to the login Keychain") || !strings.Contains(lines[1], "reads the item itself") {
		t.Errorf("looked: %q", lines)
	}
	plain := daemonLines(daemonFacet{Running: true, Model: daemonModelOf(cfg, func(string) string { return "" })})
	if len(plain) != 2 || strings.Contains(plain[1], "reads the item itself") {
		t.Errorf("a daemon that did not say it looked: %q", plain)
	}
}

// A daemon without the variable reads the Keychain when it starts, announces
// the model enabled with its source, and the key reaches no file or output.
func TestDaemonReadsTheKeychainAtStartAndSaysSo(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	t.Setenv("OPENROUTER_API_KEY", "")
	reads := fakeKeychain(t, keychainSentinel, nil)
	during, text, stderr := runDaemonWithKeys(t)
	if m := during.Daemon.Model; m == nil || !m.Enabled || m.Source != "keychain" || m.Reason != "" {
		t.Fatalf("status.daemon.model = %+v, want enabled from the keychain", m)
	}
	if reads.Load() != 1 {
		t.Errorf("%d Keychain reads at start, want 1", reads.Load())
	}
	if strings.Contains(text, "model disabled") {
		t.Errorf("text status warns about a model that has its key:\n%s", text)
	}
	if strings.Contains(stderr, "SENTINEL") || strings.Contains(text, "SENTINEL") {
		t.Errorf("a key reached stderr or status text:\n%s\n%s", stderr, text)
	}
}

// With the Keychain unreadable, or switched off as the Mac app switches it off
// in a daemon it starts, the daemon says why there is no key.
func TestDaemonWithoutAnyKeySaysWhyTheKeychainGaveNone(t *testing.T) {
	for name, c := range map[string]struct {
		prepare func(t *testing.T)
		want    string
	}{
		"item missing": {func(t *testing.T) { fakeKeychain(t, "", vault.ErrNotFound) }, "there is no such item"},
		"switched off": {func(t *testing.T) {
			n := fakeKeychain(t, keychainSentinel, nil)
			t.Setenv(noKeychainEnv, "1")
			t.Cleanup(func() {
				if n.Load() != 0 {
					t.Errorf("%d Keychain reads although %s was set", n.Load(), noKeychainEnv)
				}
			})
		}, noKeychainEnv + " is set"},
	} {
		t.Run(name, func(t *testing.T) {
			setup(t)
			mustCode(t, 0, "init")
			t.Setenv("OPENROUTER_API_KEY", "")
			c.prepare(t)
			during, text, stderr := runDaemonWithKeys(t)
			m := during.Daemon.Model
			if m == nil || m.Enabled || !strings.Contains(m.Reason, c.want) || !strings.Contains(m.Keychain, c.want) {
				t.Fatalf("status.daemon.model = %+v, want disabled, naming %q", m, c.want)
			}
			if !strings.Contains(stderr, "vani: daemon: OPENROUTER_API_KEY is not set") || !strings.Contains(text, "daemon: model disabled:") ||
				!strings.Contains(text, "reads the item itself") {
				t.Errorf("stderr %q\ntext %s", stderr, text)
			}
		})
	}
}

// runDaemonWithKeys is runDaemonOnce with the daemon's key lookup wired as the
// real app wires it (wired.openRouterKey).
func runDaemonWithKeys(t *testing.T) (during statusJSON, textDuring, stderr string) {
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
	fake := buildApp
	buildApp = func(cfg config.Config, dir string, warn io.Writer) (*App, error) {
		app, err := fake(cfg, dir, warn)
		if err != nil {
			return nil, err
		}
		app.ModelKey = (&wired{cfg: cfg, stateDir: dir, warn: io.Discard}).openRouterKey
		return app, nil
	}
	_, stderr = mustCode(t, exitRuntime, "daemon")
	return during, textDuring, stderr
}

// The schedule hint says what the daemon will really do about the key.
func TestKeepHintStatesTheDaemonsKeychainRead(t *testing.T) {
	h := keepHint(configWithModel("OPENROUTER_API_KEY"))
	// It does not advise writing the key into the agent's plist (EnvironmentVariables):
	// TestKeepHintNamesTheToggle holds that.
	for _, want := range []string{"reads the login Keychain item openrouter-api-key itself", "VANI_NO_KEYCHAIN=1", "vani status"} {
		if !strings.Contains(h, want) {
			t.Errorf("hint lacks %q: %s", want, h)
		}
	}
	if strings.Contains(h, "reaches only the daemon the app runs") {
		t.Errorf("hint still says the daemon has no Keychain path: %s", h)
	}
}
