package vault

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// OpenRouterService is the Keychain service the Mac app keeps the decision
// model's OpenRouter key under, account the user's login name
// (Keychain.openRouterService in macos/Sources/VaniApplyKit/Drafting.swift).
const OpenRouterService = "openrouter-api-key"

// ModelKeyTimeout bounds one model-key read: long enough to answer the
// Keychain's access prompt, short enough that an unanswered prompt cannot hold
// a run.
const ModelKeyTimeout = 60 * time.Second

// modelServices lists the only services ModelKey reads. Anthropic's key is not
// here: the app reaches it through anthropic-account, which picks the Console
// workspace a direct read would bypass.
var modelServices = []string{OpenRouterService}

var (
	// ErrKeychainTimeout means security(1) did not answer in time, usually an
	// access prompt nobody answered.
	ErrKeychainTimeout = errors.New("vault: the Keychain did not answer in time")
	// ErrKeychainDenied means security(1) exited with an error other than "not
	// found": access denied, the prompt refused, or no user interaction allowed.
	ErrKeychainDenied = errors.New("vault: the Keychain refused the read")
)

// ModelKey reads a model key the Mac app keeps in the login Keychain. Errors:
// ErrNoKeychain off macOS, ErrNotFound (exit 44), ErrKeychainTimeout,
// ErrKeychainDenied (another exit status, named), or a refusal of a service
// off the table or a value with a line break. No error carries the value.
func (v *Vault) ModelKey(ctx context.Context, service, account string) (string, error) {
	if !slices.Contains(modelServices, service) {
		return "", fmt.Errorf("vault: %q is not a model-key service", truncate(service))
	}
	if account == "" {
		return "", errors.New("vault: model key needs an account")
	}
	if v.err != nil {
		return "", v.err
	}
	out, err := v.r.Run(ctx, securityBin, "find-generic-password", "-s", service, "-a", account, "-w")
	if err != nil {
		var ec exitCoder
		switch {
		case errors.As(err, &ec) && ec.ExitCode() == exitItemNotFound:
			return "", fmt.Errorf("%w: %s", ErrNotFound, service)
		case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
			return "", fmt.Errorf("%w: %s", ErrKeychainTimeout, service)
		case errors.As(err, &ec):
			return "", fmt.Errorf("%w: %s (security exit status %d)", ErrKeychainDenied, service, ec.ExitCode())
		}
		return "", fmt.Errorf("%w: %s", ErrKeychainDenied, service)
	}
	key := strings.TrimSuffix(string(out), "\n")
	if strings.ContainsAny(key, "\r\n") {
		return "", fmt.Errorf("vault: %s holds a value with a line break", service)
	}
	return strings.TrimSpace(key), nil
}
