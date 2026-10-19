//go:build windows

package localserver

import "os"

// terminate kills the server. Windows has no SIGTERM to send a child that
// has no console of its own, and os.Process.Signal refuses it, so asking
// first would only spend stopGrace before the same kill.
func terminate(p *os.Process) error { return p.Kill() }
