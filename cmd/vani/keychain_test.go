package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/vault"
)

// fakeKeychain replaces keychainRead for one test, counting reads.
func fakeKeychain(t *testing.T, key string, err error) *atomic.Int32 {
	t.Helper()
	t.Setenv(noKeychainEnv, "")
	var n atomic.Int32
	old := keychainRead
	keychainRead = func(_ context.Context, service string) (string, error) {
		n.Add(1)
		if service != vault.OpenRouterService {
			t.Errorf("read service %q", service)
		}
		return key, err
	}
	t.Cleanup(func() { keychainRead = old })
	return &n
}

func keyWired(t *testing.T) (*wired, *bytes.Buffer) {
	t.Helper()
	clearKeys(t)
	cfg := config.Default()
	warn := &bytes.Buffer{}
	return &wired{cfg: cfg, stateDir: t.TempDir(), warn: warn}, warn
}

// nextRun is what startRun does to the per-run fields that matter here.
func nextRun(w *wired) {
	w.mu.Lock()
	w.keychainRun++
	w.mu.Unlock()
}

func TestOpenRouterKeyEnvironmentWins(t *testing.T) {
	w, _ := keyWired(t)
	n := fakeKeychain(t, "from-keychain", nil)
	t.Setenv(w.cfg.Decide.OpenRouterKeyEnv, "  from-env  ")
	if got := w.openRouterGetenv(w.cfg.Decide.OpenRouterKeyEnv); got != "from-env" {
		t.Fatalf("got %q", got)
	}
	if n.Load() != 0 {
		t.Fatal("the Keychain was read although the environment had the key")
	}
}

func TestOtherVariablesNeverReadTheKeychain(t *testing.T) {
	w, _ := keyWired(t)
	n := fakeKeychain(t, "from-keychain", nil)
	t.Setenv("SOME_OTHER_KEY", "")
	for _, name := range []string{"SOME_OTHER_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", ""} {
		if got := w.openRouterGetenv(name); got != "" {
			t.Errorf("%q: got %q", name, got)
		}
	}
	if n.Load() != 0 {
		t.Fatalf("%d Keychain reads for other variables", n.Load())
	}
}

func TestNoKeychainOptsOut(t *testing.T) {
	w, _ := keyWired(t)
	n := fakeKeychain(t, "from-keychain", nil)
	t.Setenv(noKeychainEnv, "1")
	if got := w.openRouterGetenv(w.cfg.Decide.OpenRouterKeyEnv); got != "" || n.Load() != 0 {
		t.Fatalf("got %q after %d reads", got, n.Load())
	}
}

// A success is kept for the process: a daemon's later runs do not prompt again.
func TestKeychainSuccessIsCachedAcrossRuns(t *testing.T) {
	w, warn := keyWired(t)
	n := fakeKeychain(t, "from-keychain", nil)
	for range 3 {
		nextRun(w)
		for range 2 {
			if got := w.openRouterGetenv(w.cfg.Decide.OpenRouterKeyEnv); got != "from-keychain" {
				t.Fatalf("got %q", got)
			}
		}
	}
	if n.Load() != 1 {
		t.Fatalf("%d reads, want 1", n.Load())
	}
	if strings.Contains(warn.String(), "from-keychain") {
		t.Fatal("the key reached the warn output")
	}
}

// A failure is kept for the run only: one run neither waits nor warns twice,
// and the next run tries again.
func TestKeychainFailureIsRetriedNextRunOnly(t *testing.T) {
	w, warn := keyWired(t)
	n := fakeKeychain(t, "", fmt.Errorf("%w: x", vault.ErrKeychainTimeout))
	nextRun(w)
	for range 3 {
		if got := w.openRouterGetenv(w.cfg.Decide.OpenRouterKeyEnv); got != "" {
			t.Fatalf("got %q", got)
		}
	}
	if n.Load() != 1 || strings.Count(warn.String(), "openrouter key:") != 1 {
		t.Fatalf("%d reads, warn %q", n.Load(), warn.String())
	}
	if !strings.Contains(warn.String(), "did not answer within 1m0s") || !strings.Contains(warn.String(), vault.OpenRouterService) {
		t.Fatalf("warn does not name the timeout: %q", warn.String())
	}
	nextRun(w)
	w.openRouterGetenv(w.cfg.Decide.OpenRouterKeyEnv)
	if n.Load() != 2 {
		t.Fatalf("%d reads after the next run, want 2", n.Load())
	}
}

// A command that never starts a run (suggest, kbmatch) still reads at most once.
func TestKeychainFailureOutsideARunIsReadOnce(t *testing.T) {
	w, _ := keyWired(t)
	n := fakeKeychain(t, "", vault.ErrNotFound)
	for range 3 {
		w.openRouterGetenv(w.cfg.Decide.OpenRouterKeyEnv)
	}
	if n.Load() != 1 {
		t.Fatalf("%d reads, want 1", n.Load())
	}
}

func TestKeychainFailureKinds(t *testing.T) {
	for _, c := range []struct {
		err  error
		key  string
		want string
	}{
		{vault.ErrNotFound, "", "there is no such item"},
		{fmt.Errorf("%w: x", vault.ErrKeychainDenied), "", "refused the read"},
		{errors.New("user: unknown"), "", "the read failed"},
		{nil, "", "it is empty"},
	} {
		w, warn := keyWired(t)
		fakeKeychain(t, c.key, c.err)
		w.openRouterGetenv(w.cfg.Decide.OpenRouterKeyEnv)
		if !strings.Contains(warn.String(), c.want) {
			t.Errorf("%v: warn %q, want %q", c.err, warn.String(), c.want)
		}
	}
	// Off macOS there is no Keychain to ask, and nothing to warn about.
	w, warn := keyWired(t)
	fakeKeychain(t, "", fmt.Errorf("%w, which linux does not have", vault.ErrNoKeychain))
	w.openRouterGetenv(w.cfg.Decide.OpenRouterKeyEnv)
	if warn.Len() != 0 {
		t.Errorf("warned off macOS: %q", warn.String())
	}
}

// Concurrent clients (triage and notes are built together) read once.
func TestKeychainConcurrentCallsReadOnce(t *testing.T) {
	w, _ := keyWired(t)
	n := fakeKeychain(t, "from-keychain", nil)
	nextRun(w)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := w.openRouterGetenv(w.cfg.Decide.OpenRouterKeyEnv); got != "from-keychain" {
				t.Errorf("got %q", got)
			}
			nextRun(w)
		}()
	}
	wg.Wait()
	if n.Load() != 1 {
		t.Fatalf("%d reads, want 1", n.Load())
	}
}

// The run's decision backend is built from the Keychain key when the
// environment has none, and is missing (errNoBackend) when the Keychain has
// none either. No request is sent: building the runner reads no network.
func TestRunnerTakesTheOpenRouterKeyFromTheKeychain(t *testing.T) {
	for _, c := range []struct {
		name string
		key  string
		err  error
		ok   bool
	}{
		{"keychain has it", "test-key", nil, true},
		{"keychain lacks it", "", vault.ErrNotFound, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			clearKeys(t)
			cfg := config.Default()
			if !cfg.OpenRouterConfigured() {
				t.Fatal("the default config has no OpenRouter model to test with")
			}
			w, warn := openWired(t, cfg)
			fakeKeychain(t, c.key, c.err)
			d, err := egress.New(egress.Config{AllowedHosts: cfg.AllowedHosts})
			if err != nil {
				t.Fatal(err)
			}
			w.backendErrs = newBackendErrors()
			nextRun(w)
			_, err = w.runner(context.Background(), d)
			switch {
			case c.ok && err != nil:
				t.Fatalf("runner: %v\nwarn: %s", err, warn.String())
			case !c.ok && !errors.Is(err, errNoBackend):
				t.Fatalf("runner err = %v, want errNoBackend", err)
			case !c.ok && !strings.Contains(warn.String(), "Keychain item"):
				t.Fatalf("warn does not say the Keychain was tried: %s", warn.String())
			}
		})
	}
}

// The Mac app stores the key under the service vani reads.
func TestKeychainServiceMatchesTheApp(t *testing.T) {
	src, err := os.ReadFile("../../macos/Sources/VaniApplyKit/Drafting.swift")
	if err != nil {
		t.Fatal(err)
	}
	if want := `openRouterService = "` + vault.OpenRouterService + `"`; !strings.Contains(string(src), want) {
		t.Fatalf("Drafting.swift does not declare %s", want)
	}
}
