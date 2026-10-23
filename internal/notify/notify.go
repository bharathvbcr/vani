// Package notify posts macOS notifications through /usr/bin/osascript.
//
// Title and body are passed as argv to an "on run argv" script and are never
// interpolated into the script text, so quotes in them cannot inject
// AppleScript.
package notify

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unicode"
)

const (
	osascriptPath = "/usr/bin/osascript"
	// MaxTitleRunes and MaxBodyRunes bound what is sent to the notifier.
	MaxTitleRunes = 80
	MaxBodyRunes  = 240
	// runTimeout bounds one osascript invocation.
	runTimeout = 10 * time.Second
)

// script is constant text; the only data it touches arrives through argv.
var script = []string{
	"on run argv",
	"display notification (item 2 of argv) with title (item 1 of argv)",
	"end run",
}

// Notifier delivers a user-visible notification.
type Notifier interface {
	Notify(ctx context.Context, title, body string) error
}

// Noop is a Notifier that does nothing.
type Noop struct{}

// Notify implements Notifier.
func (Noop) Notify(context.Context, string, string) error { return nil }

// Runner executes a command and returns its combined output. It exists so
// tests can observe the argv without running osascript.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ErrTestBinary is what the real runner returns inside a Go test binary. A
// test that wires the real App has no app lock in its temp state dir, so
// without this every `go test` posted Script Editor notices to the owner.
var ErrTestBinary = errors.New("notify: osascript is not run from a test binary")

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if testing.Testing() {
		return nil, ErrTestBinary
	}
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// OSAScript notifies through osascript.
type OSAScript struct {
	runner Runner
}

// New returns an OSAScript notifier. A nil runner uses the real osascript.
func New(r Runner) *OSAScript {
	if r == nil {
		r = execRunner{}
	}
	return &OSAScript{runner: r}
}

// Notify shows a notification. Empty title and body are an error.
func (o *OSAScript) Notify(ctx context.Context, title, body string) error {
	title, body = clean(title, MaxTitleRunes), clean(body, MaxBodyRunes)
	if title == "" || body == "" {
		return errors.New("notify: title and body are required")
	}
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	out, err := o.runner.Run(ctx, osascriptPath, Args(title, body)...)
	if err != nil {
		return fmt.Errorf("notify: osascript: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Args builds the osascript argument vector. "--" ends option parsing so a
// title that starts with "-" is data, not a flag.
func Args(title, body string) []string {
	args := make([]string, 0, 2*len(script)+3)
	for _, line := range script {
		args = append(args, "-e", line)
	}
	return append(args, "--", title, body)
}

// clean removes control characters and truncates to max runes.
func clean(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsControl(r) {
			r = ' '
		}
		if n >= max {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}
