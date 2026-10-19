// Package vault keeps profile values (name, contact, work authorization) in
// the macOS Keychain through the /usr/bin/security CLI. No cgo, no
// dependencies. Values never appear in errors or logs.
package vault

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"

	"github.com/bharathvbcr/Vani/internal/resume"
)

const (
	securityBin = "/usr/bin/security"
	service     = "vani"
	// exitItemNotFound is errSecItemNotFound as security(1) exits.
	exitItemNotFound = 44
)

// allowed lists the only keys the vault stores. It is unexported so no other
// package can append a key to it, or empty it, at run time.
var allowed = []string{
	"name", "email", "phone", "location",
	"linkedin_url", "github_url", "portfolio_url",
	"work_authorization", "requires_sponsorship",
}

// Allowed returns a copy of the keys the vault stores.
func Allowed() []string { return slices.Clone(allowed) }

var (
	// ErrNotFound means the key has no keychain item.
	ErrNotFound = errors.New("vault: not found")
	// ErrUnknownKey means the key is not on the allowlist.
	ErrUnknownKey = errors.New("vault: unknown key")
	// ErrNoKeychain means this platform has no macOS Keychain to keep values in.
	ErrNoKeychain = errors.New("vault: the profile vault keeps values in the macOS Keychain")
)

// Runner runs an external command and returns its stdout. Tests fake it.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

// Run discards stderr: it can echo item attributes, and errors must not carry
// anything but the exit status.
func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// Vault reads and writes allowlisted profile values.
type Vault struct {
	r   Runner
	err error // set when the platform has no Keychain: every call returns it
}

// New returns a Vault backed by /usr/bin/security. Off macOS every call fails
// with ErrNoKeychain instead of running a security(1) that is not there.
func New() *Vault { return &Vault{r: execRunner{}, err: platformErr()} }

// NewWithRunner returns a Vault using r (for tests).
func NewWithRunner(r Runner) *Vault { return &Vault{r: r} }

// noKeychain is ErrNoKeychain naming the platform it was refused on.
func noKeychain(goos string) error {
	return fmt.Errorf("%w, which %s does not have", ErrNoKeychain, goos)
}

func checkKey(key string) error {
	if slices.Contains(allowed, key) {
		return nil
	}
	return fmt.Errorf("%w: %q", ErrUnknownKey, truncate(key))
}

func truncate(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

type exitCoder interface{ ExitCode() int }

// Get returns the stored value. Errors: ErrUnknownKey, ErrNotFound (exit 44),
// or a generic error carrying only the key and exit status.
func (v *Vault) Get(ctx context.Context, key string) (string, error) {
	if err := checkKey(key); err != nil {
		return "", err
	}
	if v.err != nil {
		return "", v.err
	}
	out, err := v.r.Run(ctx, securityBin, "find-generic-password", "-s", service, "-a", key, "-w")
	if err != nil {
		var ec exitCoder
		if errors.As(err, &ec) && ec.ExitCode() == exitItemNotFound {
			return "", fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return "", failure("read", key, err)
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// Set stores value, updating an existing item.
//
// The value is passed as `-w <value>` and so is visible in this process's argv
// (ps) for the duration of the call. security(1) has no stdin form:
// `add-generic-password -h` says "Specify -w as the last option to be
// prompted", and that prompt reads the controlling terminal, not stdin, so it
// cannot be driven from a daemon; -X (hex) is argv as well. The exposure is
// brief and same-user only; the value is never logged or put in an error.
func (v *Vault) Set(ctx context.Context, key, value string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	if value == "" {
		return fmt.Errorf("vault: empty value for %s", key)
	}
	if v.err != nil {
		return v.err
	}
	if _, err := v.r.Run(ctx, securityBin, "add-generic-password", "-U", "-s", service, "-a", key, "-w", value); err != nil {
		return failure("write", key, err)
	}
	return nil
}

// failure describes an error without the underlying text, which could echo
// argv (and so the value).
func failure(op, key string, err error) error {
	var ec exitCoder
	if errors.As(err, &ec) {
		return fmt.Errorf("vault: %s %s failed: security exit status %d", op, key, ec.ExitCode())
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("vault: %s %s: %w", op, key, ctxOf(err))
	}
	return fmt.Errorf("vault: %s %s failed", op, key)
}

func ctxOf(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return context.Canceled
}

var _ resume.HeaderSource = (*Vault)(nil)

// Header builds the resume header. name is required; the rest are optional
// and skipped when absent. Contact lines: email | phone | location, then the
// URLs (at most two lines).
func (v *Vault) Header(ctx context.Context) (resume.Header, error) {
	name, err := v.Get(ctx, "name")
	if err != nil {
		return resume.Header{}, err
	}
	get := func(keys ...string) ([]string, error) {
		var vals []string
		for _, k := range keys {
			s, err := v.Get(ctx, k)
			switch {
			case errors.Is(err, ErrNotFound):
			case err != nil:
				return nil, err
			case s != "":
				vals = append(vals, s)
			}
		}
		return vals, nil
	}
	var h resume.Header
	h.Name = name
	for _, group := range [][]string{
		{"email", "phone", "location"},
		{"linkedin_url", "github_url", "portfolio_url"},
	} {
		vals, err := get(group...)
		if err != nil {
			return resume.Header{}, err
		}
		if len(vals) > 0 {
			h.ContactLines = append(h.ContactLines, strings.Join(vals, " | "))
		}
	}
	return h, nil
}
