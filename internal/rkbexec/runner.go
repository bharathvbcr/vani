// Package rkbexec is the one place Vani runs the rkb binary (and the other
// bounded helper processes the knowledge-bank code starts): a Runner seam that
// tests fake, and an ExecRunner that bounds the run's time and captured output
// and stops the whole process tree when it is cancelled or times out.
// Both internal/kbsync (rkb claims export) and internal/rkbresume (every rkb
// step of a resume build) run through it.
package rkbexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// Command is one process invocation. Env is the complete environment.
type Command struct {
	Path string
	Args []string
	Env  []string
}

// Output is what a finished process left. A non-zero Exit is not an error:
// rkb's exit code is part of its answer (lint 1, fit 2-5).
type Output struct {
	Exit           int
	Stdout, Stderr string
}

// Runner runs one process under a timeout. It is the seam tests fake. An error
// means the process did not finish: it could not start, outlived timeout
// (ErrStepTimeout) or ctx ended.
type Runner interface {
	Run(ctx context.Context, c Command, timeout time.Duration) (Output, error)
}

// ErrStepTimeout means one step outlived its own bound.
var ErrStepTimeout = errors.New("did not finish within its time limit")

const (
	// MaxOutput bounds what is kept of each of a process's output streams. A
	// stream that reaches it was cut.
	MaxOutput = 64 << 10
	// MaxTimeout bounds every run; a zero or larger timeout means MaxTimeout.
	MaxTimeout = 5 * time.Minute

	waitDelay = 2 * time.Second
)

// ExecRunner runs real processes with a bounded timeout and bounded captured
// output.
type ExecRunner struct{}

type capWriter struct{ buf bytes.Buffer }

func (w *capWriter) Write(p []byte) (int, error) {
	if room := MaxOutput - w.buf.Len(); room > 0 {
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
	if timeout <= 0 || timeout > MaxTimeout {
		timeout = MaxTimeout
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(stepCtx, c.Path, c.Args...)
	cmd.Env = c.Env
	cmd.Stdin = nil
	var out, errw capWriter
	cmd.Stdout, cmd.Stderr = &out, &errw
	if err := configureProcess(cmd); err != nil {
		return Output{}, fmt.Errorf("%s: %w", c.Path, err)
	}
	defer dropGroup(cmd)
	cmd.WaitDelay = waitDelay
	// Start, then adopt, then Wait: Windows creates the child suspended and
	// adopt assigns its job before the child can start a renderer.
	err := cmd.Start()
	if err == nil {
		if aerr := adoptGroup(cmd); aerr != nil {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_ = cmd.Wait()
			err = aerr
		} else {
			err = cmd.Wait()
		}
	}
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
