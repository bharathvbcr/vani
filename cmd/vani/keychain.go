package main

import (
	"context"
	"errors"
	"os"
	"os/user"
	"strings"
	"sync"

	"github.com/bharathvbcr/Vani/internal/vault"
)

// noKeychainEnv, set to any value, stops vani reading the OpenRouter key from
// the Keychain. Agent shells and tests set it, so a vani they start can neither
// spend from the owner's key nor raise a Keychain prompt.
const noKeychainEnv = "VANI_NO_KEYCHAIN"

// keychainRead reads one model key from the login Keychain, under the user's
// login name, bounded by vault.ModelKeyTimeout. A variable so tests replace it:
// no test may run the real security(1).
var keychainRead = func(ctx context.Context, service string) (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, vault.ModelKeyTimeout)
	defer cancel()
	return vault.New().ModelKey(ctx, service, u.Username)
}

// keychainCache is the OpenRouter key read from the Keychain: a success is kept
// for the life of the process (a daemon reads it once), a failure only for the
// run it happened in, so one run neither waits on nor warns about the Keychain
// twice. mu serialises reads, so concurrent clients never prompt twice; it is
// not w.mu, which a read of up to a minute must not hold.
type keychainCache struct {
	mu      sync.Mutex
	key     string
	failed  bool
	failRun int // keychainRun of the last failed read, when failed
}

// openRouterGetenv is jev.Config.Getenv for every OpenRouter client vani
// builds (the run, suggest, notes, kbmatch). The environment wins. For the
// configured key variable only, when the environment has no value and
// VANI_NO_KEYCHAIN is unset, it reads the Keychain item the Mac app keeps the
// key in (vault.OpenRouterService). jev calls it only for an OpenRouter
// endpoint, so a Keychain key is never sent anywhere else. Off macOS the read
// fails with vault.ErrNoKeychain and is silent: there is no Keychain to ask.
//
// Security impact: before, only the Mac app could read this key and hand it to
// a vani it started. Now any vani process in the owner's login session can,
// once the owner grants the Keychain's access prompt to the vani binary (an
// unsigned rebuild is a new binary and prompts again). Spend stays bounded by
// decide.daily_usd.openrouter; VANI_NO_KEYCHAIN opts out; the Anthropic key is
// not read (the app reaches it through anthropic-account, which picks the
// workspace). The value is never logged or put in an error.
func (w *wired) openRouterGetenv(name string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" || name != w.cfg.Decide.OpenRouterKeyEnv {
		return v
	}
	if os.Getenv(noKeychainEnv) != "" {
		return ""
	}
	w.mu.Lock()
	run := w.keychainRun
	w.mu.Unlock()
	c := &w.keychain
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key != "" {
		return c.key
	}
	if c.failed && c.failRun == run {
		return ""
	}
	key, err := keychainRead(context.Background(), vault.OpenRouterService)
	if err != nil || key == "" {
		c.failed, c.failRun = true, run
		if !errors.Is(err, vault.ErrNoKeychain) {
			w.warnf("openrouter key: %s is unset and the Keychain item %q could not be read: %s", name, vault.OpenRouterService, keychainFailure(err))
		}
		return ""
	}
	c.key = key
	return key
}

// keychainFailure names why a Keychain read failed, without its text.
func keychainFailure(err error) string {
	switch {
	case err == nil:
		return "it is empty"
	case errors.Is(err, vault.ErrNotFound):
		return "there is no such item"
	case errors.Is(err, vault.ErrKeychainTimeout):
		return "the Keychain did not answer within " + vault.ModelKeyTimeout.String() + " (an access prompt nobody answered?)"
	case errors.Is(err, vault.ErrKeychainDenied):
		return "the Keychain refused the read (access denied, or no prompt can be shown here)"
	}
	return "the read failed"
}
