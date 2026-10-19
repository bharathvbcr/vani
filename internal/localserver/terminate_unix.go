//go:build !windows

package localserver

import (
	"os"
	"syscall"
)

// terminate asks the server to exit: SIGTERM lets llama-server free the GPU
// and finish its slots. Stop kills it if it has not gone after stopGrace.
func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
