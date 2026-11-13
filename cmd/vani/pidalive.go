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
	state, _ := classifyDaemon(d)
	return state == daemonThisBinary
}

// daemonState is what daemon.json's pid is, as far as this process can tell.
type daemonState int

const (
	// daemonGone: no such process, one this process cannot inspect, or one that
	// started after the announcement (a reused pid).
	daemonGone daemonState = iota
	// daemonThisBinary: the announced daemon, running this executable.
	daemonThisBinary
	// daemonOtherBinary: the announced daemon, running another vani binary: the
	// process started no later than the announcement and runs the executable
	// the announcement recorded, which is not this one.
	daemonOtherBinary
	// daemonUnconfirmed: a process that started in time but runs neither this
	// executable nor one the announcement names (an older daemon.json names
	// none). Nothing ties it to the announcement, so it is not called a daemon.
	daemonUnconfirmed
)

// Values of daemonFacet.Binary.
const (
	daemonBinaryThis  = "this"
	daemonBinaryOther = "other"
)

// classifyDaemon says what d's pid is and, for any live process that is not
// this binary, the executable found there. The reused-pid guard is the same
// for every outcome: the process must have started no later than the
// announcement (beyond daemonStartSlack) and be inspectable. Past that guard,
// only an executable that is this one, or the one recorded, names a daemon.
func classifyDaemon(d daemonRecord) (daemonState, string) {
	id := daemonIdentity(d.PID)
	if !id.OK || id.Exe == "" || id.Started.IsZero() {
		return daemonGone, ""
	}
	if id.Started.After(d.StartedAt.Add(daemonStartSlack)) {
		return daemonGone, ""
	}
	if self, err := os.Executable(); err == nil && sameExecutable(id.Exe, self) {
		return daemonThisBinary, id.Exe
	}
	if d.Exe != "" && sameExecutable(id.Exe, d.Exe) {
		return daemonOtherBinary, id.Exe
	}
	return daemonUnconfirmed, id.Exe
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
