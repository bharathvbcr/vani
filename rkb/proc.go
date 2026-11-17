package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"
)

// Bounds for every subprocess rkb starts. They are variables so a test can
// shrink them; nothing else changes them.
var (
	timeoutPoppler   = 60 * time.Second // pdftotext, pdfinfo
	timeoutOffice    = 3 * time.Minute  // soffice --convert-to pdf
	timeoutLatex     = 5 * time.Minute  // one pdflatex pass
	timeoutChrome    = 90 * time.Second // headless Chrome print or measure
	timeoutValidator = 10 * time.Minute // the CV repo's validator and its build
)

// killGrace is how long Run keeps waiting for the output pipes after the
// process is killed (a surviving grandchild may hold them open).
const killGrace = 2 * time.Second

type runOpts struct {
	Dir      string   // working directory; "" keeps rkb's
	Env      []string // full environment; nil keeps rkb's
	Combined bool     // return stderr interleaved with stdout
}

// runCmd runs name with args and returns its output. The process leads its own
// process group, and when timeout expires the whole group is killed, so a
// converter's helpers (Chrome's renderers, soffice's oosplash) do not outlive
// it. Expiry is an error naming the bound; a command that cannot be started or
// exits non-zero returns exec's error with whatever it printed.
func runCmd(timeout time.Duration, o runOpts, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = o.Dir, o.Env
	var out bytes.Buffer
	cmd.Stdout = &out
	if o.Combined {
		cmd.Stderr = &out
	}
	ownGroup(cmd)
	cmd.WaitDelay = killGrace
	err := cmd.Run()
	// Whatever the outcome, nothing the command started may outlive it:
	// Chrome and soffice leave helpers in the group after the leader exits.
	killGroup(cmd)
	if ctx.Err() == context.DeadlineExceeded {
		return out.Bytes(), fmt.Errorf("%s did not finish within %s; it and its helpers were killed", filepath.Base(name), timeout)
	}
	if ctx.Err() == nil && errors.Is(err, exec.ErrWaitDelay) {
		// The leader exited 0 but a helper held the output pipe open until
		// WaitDelay ran out. The command succeeded; the helper is killed above.
		err = nil
	}
	return out.Bytes(), err
}
