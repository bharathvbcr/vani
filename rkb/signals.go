package main

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
)

// Interrupt handling. The first SIGINT or SIGTERM kills every process group
// runCmd started (they lead their own groups, so the terminal's Ctrl-C does
// not reach them), removes the scratch folders, and exits 130 or 143. A second
// signal while that runs exits at once.
//
// The watcher is installed lazily, by the first runCmd, scratchDir or main,
// not only from main: the same code paths run under `go test`, which has no
// rkb main, and a path that starts a process or stages the person's documents
// must be covered wherever it is called from.

var (
	sigOnce sync.Once
	sigCtx  context.Context // cancelled by the first signal

	// rootCtxOverride replaces sigCtx as the parent of every runCmd context.
	// Tests set it (and restore it); nothing else does.
	rootCtxOverride context.Context

	groupsMu sync.Mutex
	groups   = map[*exec.Cmd]struct{}{}
)

// rootContext is the parent of every runCmd context: cancelled by an
// interrupt, so a signal reaches the children through exec's own cancellation
// as well as through the explicit group kill below.
func rootContext() context.Context {
	if rootCtxOverride != nil {
		return rootCtxOverride
	}
	watchSignals()
	return sigCtx
}

func trackGroup(cmd *exec.Cmd) {
	groupsMu.Lock()
	groups[cmd] = struct{}{}
	groupsMu.Unlock()
}

func untrackGroup(cmd *exec.Cmd) {
	groupsMu.Lock()
	delete(groups, cmd)
	groupsMu.Unlock()
}

// killTracked kills every running command's group before the process exits;
// relying on context cancellation alone would race os.Exit.
func killTracked() {
	groupsMu.Lock()
	cmds := make([]*exec.Cmd, 0, len(groups))
	for c := range groups {
		cmds = append(cmds, c)
	}
	groupsMu.Unlock()
	for _, c := range cmds {
		killGroup(c)
	}
}

// signalExitCode is the shell's convention: 128 plus the signal number.
func signalExitCode(s os.Signal) int {
	if s == syscall.SIGTERM {
		return 143
	}
	return 130
}

// watchSignals installs the interrupt handler once.
func watchSignals() {
	sigOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		sigCtx = ctx
		ch := make(chan os.Signal, 2)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		go func() {
			s := <-ch
			code := signalExitCode(s)
			interrupted.Store(int32(code)) // before cancel: callers racing on the kill must exit with it
			// A second signal during cleanup must not wait for it.
			go func() {
				s2 := <-ch
				os.Exit(signalExitCode(s2))
			}()
			cancel()
			killTracked()
			exitNow(code) // runs the scratchDir cleanups, then os.Exit
		}()
	})
}
