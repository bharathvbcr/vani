package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// procIdentity is the executable and start time of a pid this process can
// inspect. OK is false when the pid is gone or the kernel refuses the lookup
// (EPERM, ERROR_ACCESS_DENIED). An uninspectable pid is not this daemon.
type procIdentity struct {
	Exe     string
	Started time.Time
	OK      bool
}

// daemonIdentity is the process lookup behind daemonIsLive. Tests replace it
// so the daemon check does not fork. pidAlive stays the existence check.
var daemonIdentity = readProcIdentity

// daemonStartSlack is the most a kernel start time may sit after the
// started_at recorded in daemon.json. The kernel clock is coarser than
// time.Now; a start several seconds later is a reused pid. A later rewrite
// of the file is not a new announcement.
const daemonStartSlack = time.Second

// daemonIsLive reports whether d names this vani process. A different
// executable, a pid this process cannot inspect, or a process that started
// after the recorded started_at (beyond daemonStartSlack) is not. The file's
// mtime is not that time.
func daemonIsLive(d daemonRecord) bool {
	id := daemonIdentity(d.PID)
	if !id.OK || id.Exe == "" || id.Started.IsZero() {
		return false
	}
	self, err := os.Executable()
	if err != nil || !sameExecutable(id.Exe, self) {
		return false
	}
	return !id.Started.After(d.StartedAt.Add(daemonStartSlack))
}

// sameExecutable reports whether two paths name the same binary. Symlinks
// are resolved when both can be; Windows compares without case.
func sameExecutable(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ar, aerr := filepath.EvalSymlinks(a)
	br, berr := filepath.EvalSymlinks(b)
	if aerr != nil || berr != nil {
		ar, br = filepath.Clean(a), filepath.Clean(b)
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ar, br)
	}
	return ar == br
}
