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

// daemonStartSlack is the most a kernel start time may sit after the wall
// clock time recorded in daemon.json. The kernel clock is coarser than
// time.Now; a start several seconds later is a reused pid.
const daemonStartSlack = time.Second

// announcementInstant is the wall-clock time the daemon was announced.
// daemon.json's started_at is that instant when it was written with the wall
// clock. A test clock (var now) can sit days away from the kernel; the file's
// mtime is then the write, and a reused pid is still a process that started
// after the file was written.
func announcementInstant(stateDir string, d daemonRecord) time.Time {
	fi, err := os.Lstat(runsPath(stateDir, daemonRecordFile))
	if err != nil {
		return d.StartedAt
	}
	mt := fi.ModTime()
	if mt.IsZero() {
		return d.StartedAt
	}
	delta := mt.Sub(d.StartedAt)
	if delta < 0 {
		delta = -delta
	}
	if delta > daemonStartSlack {
		return mt
	}
	return d.StartedAt
}

// daemonIsLive reports whether d names this vani process. A different
// executable, a pid this process cannot inspect, or a process that started
// after the announcement (beyond daemonStartSlack) is not.
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
