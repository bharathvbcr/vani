package page

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// MaxStdout is the most output one lightpanda run may produce.
	MaxStdout = 16 << 20
	// maxStderr bounds the stderr kept for diagnostics.
	maxStderr = 64 << 10
	// stderrInError is how much of stderr an error message carries.
	stderrInError = 300
)

// ErrOutputTooLarge means a run wrote more than MaxStdout. The output is never
// returned truncated: a cut page could read as a smaller, valid one.
var ErrOutputTooLarge = errors.New("page: lightpanda output exceeds the size limit")

// Runner runs lightpanda with args (not including the binary) and returns its
// stdout. A non-zero exit, a timeout, a cancelled context or output over
// MaxStdout is an error; stdout is only trusted when err is nil. Tests
// substitute a fake.
type Runner interface {
	Run(ctx context.Context, args []string) ([]byte, error)
}

// ExecRunner runs a lightpanda binary as a separate process. Lightpanda is
// AGPL-licensed; it is only ever executed, never linked.
type ExecRunner struct {
	// Binary is the absolute path of the lightpanda executable.
	Binary string
	// Timeout bounds one run, start to exit.
	Timeout time.Duration
}

// Run executes the binary with a minimal environment: PATH, HOME and TMPDIR
// from this process, and telemetry off. Nothing else leaks into the child.
func (r ExecRunner) Run(ctx context.Context, args []string) ([]byte, error) {
	if !filepath.IsAbs(r.Binary) {
		return nil, fmt.Errorf("page: lightpanda binary %q is not an absolute path", r.Binary)
	}
	if r.Timeout <= 0 {
		return nil, errors.New("page: ExecRunner needs a positive Timeout")
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	var stdout capBuffer
	stdout.limit = MaxStdout
	stdout.onOverflow = cancel
	var stderr capBuffer
	stderr.limit = maxStderr
	stderr.discardOverflow = true

	cmd := exec.CommandContext(ctx, r.Binary, args...)
	cmd.Env = minimalEnv()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	switch {
	case stdout.overflowed():
		return nil, ErrOutputTooLarge
	case ctx.Err() != nil && errors.Is(context.Cause(ctx), context.DeadlineExceeded):
		return nil, fmt.Errorf("page: lightpanda timed out after %s", r.Timeout)
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case err != nil:
		return nil, fmt.Errorf("page: lightpanda failed: %w%s", err, tail(stderr.bytes()))
	}
	return stdout.bytes(), nil
}

func minimalEnv() []string {
	env := []string{"LIGHTPANDA_DISABLE_TELEMETRY=1"}
	for _, k := range []string{"PATH", "HOME", "TMPDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// tail renders the end of stderr for an error message: bounded, one line, no
// control characters.
func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > stderrInError {
		s = s[len(s)-stderrInError:]
	}
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if s == "" {
		return ""
	}
	return ": " + s
}

// capBuffer collects at most limit bytes. Past the limit it either fails the
// write (stdout: the process is told to stop) or silently drops the excess
// (stderr: a diagnostic, not data).
type capBuffer struct {
	mu              sync.Mutex
	buf             bytes.Buffer
	limit           int
	over            bool
	discardOverflow bool
	onOverflow      func()
}

func (c *capBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	room := c.limit - c.buf.Len()
	if len(p) <= room {
		return c.buf.Write(p)
	}
	if c.discardOverflow {
		// Keep the newest bytes: the end of stderr says why the run failed.
		keep := append(c.buf.Bytes(), p...)
		if len(keep) > c.limit {
			keep = keep[len(keep)-c.limit:]
		}
		c.buf = *bytes.NewBuffer(append([]byte(nil), keep...))
		return len(p), nil
	}
	c.over = true
	if c.onOverflow != nil {
		c.onOverflow()
	}
	return 0, ErrOutputTooLarge
}

func (c *capBuffer) overflowed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.over
}

func (c *capBuffer) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Bytes()
}
