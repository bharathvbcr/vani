package rkbresume

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// Command is one rkb invocation. Env is the complete environment.
type Command = kbsync.Command

// Output is what a finished process left. A non-zero Exit is not an error:
// rkb's exit code is part of its answer (lint 1, fit 2-5).
type Output struct {
	Exit           int
	Stdout, Stderr string
}

// Runner runs one process under a timeout. It is the seam tests fake. An error
// means the process did not finish: it could not start, outlived timeout
// (ErrStepTimeout) or ctx ended.
//
// kbsync.Runner cannot serve here: it drops stdout and the exit code, and rkb
// lint reports its errors on stdout and rkb fit its verdict in its exit code.
type Runner interface {
	Run(ctx context.Context, c Command, timeout time.Duration) (Output, error)
}

// ErrStepTimeout means one rkb step outlived its own bound.
var ErrStepTimeout = errors.New("did not finish within its time limit")

const (
	maxProcOutput = 64 << 10
	maxStepTime   = 5 * time.Minute
	waitDelay     = 2 * time.Second
)

// ExecRunner runs real processes with a bounded timeout and bounded captured
// output.
type ExecRunner struct{}

type capWriter struct{ buf bytes.Buffer }

func (w *capWriter) Write(p []byte) (int, error) {
	if room := maxProcOutput - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil // keep draining so a noisy child cannot block
}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, c Command, timeout time.Duration) (Output, error) {
	if timeout <= 0 || timeout > maxStepTime {
		timeout = maxStepTime
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(stepCtx, c.Path, c.Args...)
	cmd.Env = c.Env
	cmd.Stdin = nil
	var out, errw capWriter
	cmd.Stdout, cmd.Stderr = &out, &errw
	configureProcess(cmd)
	cmd.WaitDelay = waitDelay
	err := cmd.Run()
	o := Output{Stdout: out.buf.String(), Stderr: errw.buf.String()}
	if err == nil {
		return o, nil
	}
	if stepCtx.Err() != nil {
		reapGroup(cmd)
	}
	if ctx.Err() != nil {
		return o, ctx.Err()
	}
	if stepCtx.Err() != nil {
		return o, ErrStepTimeout
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() >= 0 {
		o.Exit = ee.ExitCode()
		return o, nil
	}
	return o, fmt.Errorf("%s: %w", c.Path, err)
}

// clip shortens s to n runes on one line, for an error message.
func clip(s string, n int) string {
	s = strings.ToValidUTF8(strings.TrimSpace(s), "?")
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
