package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strings"
	"sync"

	"github.com/bharathvbcr/Vani/internal/vault"
)

// noKeychainEnv, set to any non-empty value, stops vani reading the OpenRouter key from
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

// keychainCache is one model key read from the Keychain: a success is kept
// for the life of the process (a daemon reads it once), a failure only for the
// run it happened in, so one run neither waits on nor warns about the Keychain
// twice. mu serialises reads, so concurrent clients never prompt twice; it is
// not w.mu, which a read of up to a minute must not hold.
type keychainCache struct {
	mu      sync.Mutex
	key     string
	failed  bool
	failRun int    // keychainRun of the last failed read, when failed
	why     string // what the last failed read said, for status; never the value
}

// Where a key came from (openRouterKey).
const (
	keyFromEnvironment = "environment"
	keyFromKeychain    = "keychain"
)

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
	key, _, _ := w.openRouterKey(name)
	return key
}

// openRouterKey is openRouterGetenv with where the key came from
// (keyFromEnvironment or keyFromKeychain) and, when there is none, a phrase
// saying why the Keychain gave none ("" when the environment named a key or
// name is not the OpenRouter key variable). The phrase never carries a value.
// The daemon announces it (daemon.model), so a status that says the model is
// off says the truth about the Keychain too.
func (w *wired) openRouterKey(name string) (key, from, why string) {
	w.mu.Lock()
	keyEnv := w.cfg.Decide.OpenRouterKeyEnv
	w.mu.Unlock()
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v, keyFromEnvironment, ""
	}
	if name != keyEnv {
		return "", "", ""
	}
	return w.modelKey("openrouter", []string{name}, vault.OpenRouterService)
}

// modelKey is a model key: the first of envs that holds a value, else, unless
// VANI_NO_KEYCHAIN is set, the login Keychain item service, cached as
// keychainCache says. what names the key in a warning ("subq", "gemini").
// It returns where the key came from, or why there is none; never a value in
// a message. Only services vault.ModelKey's table holds can be read.
func (w *wired) modelKey(what string, envs []string, service string) (key, from, why string) {
	for _, name := range envs {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v, keyFromEnvironment, ""
		}
	}
	if os.Getenv(noKeychainEnv) != "" {
		return "", "", "the Keychain was not read because " + noKeychainEnv + " is set"
	}
	w.mu.Lock()
	run := w.keychainRun
	if w.keychains == nil {
		w.keychains = map[string]*keychainCache{}
	}
	c := w.keychains[service]
	if c == nil {
		c = &keychainCache{}
		w.keychains[service] = c
	}
	w.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key != "" {
		return c.key, keyFromKeychain, ""
	}
	if c.failed && c.failRun == run {
		return "", "", c.why
	}
	k, err := keychainRead(context.Background(), service)
	if err != nil || k == "" {
		c.failed, c.failRun = true, run
		if errors.Is(err, vault.ErrNoKeychain) {
			c.why = "there is no Keychain on this platform"
		} else {
			c.why = fmt.Sprintf("the login Keychain item %q could not be read: %s", service, keychainFailure(err))
			unset := "no key variable is set"
			switch len(envs) {
			case 0:
			case 1:
				unset = envs[0] + " is unset"
			default:
				unset = strings.Join(envs, " and ") + " are unset"
			}
			w.warnf("%s key: %s and the Keychain item %q could not be read: %s", what, unset, service, keychainFailure(err))
		}
		return "", "", c.why
	}
	c.key = k
	return k, keyFromKeychain, ""
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
