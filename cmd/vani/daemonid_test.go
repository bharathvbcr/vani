package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDaemonIdentity(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)
	old := daemonIdentity
	t.Cleanup(func() { daemonIdentity = old })

	cases := []struct {
		name string
		id   procIdentity
		at   time.Time
		want bool
	}{
		{"uninspectable", procIdentity{}, start, false},
		{"uninspectable with fields", procIdentity{OK: false, Exe: self, Started: start.Add(-time.Hour)}, start, false},
		{"no executable", procIdentity{OK: true, Started: start}, start, false},
		{"zero start", procIdentity{OK: true, Exe: self}, start, false},
		{"wrong executable", procIdentity{OK: true, Exe: self + ".other", Started: start.Add(-time.Hour)}, start, false},
		{"reused pid", procIdentity{OK: true, Exe: self, Started: start.Add(5 * time.Second)}, start, false},
		{"past slack", procIdentity{OK: true, Exe: self, Started: start.Add(daemonStartSlack + time.Millisecond)}, start, false},
		{"equal start", procIdentity{OK: true, Exe: self, Started: start}, start, true},
		{"earlier start", procIdentity{OK: true, Exe: self, Started: start.Add(-time.Hour)}, start, true},
		{"slack boundary", procIdentity{OK: true, Exe: self, Started: start.Add(daemonStartSlack)}, start, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			daemonIdentity = func(int) procIdentity { return tc.id }
			got := daemonIsLive(daemonRecord{V: 1, PID: 4242, StartedAt: tc.at})
			if got != tc.want {
				t.Fatalf("daemonIsLive = %v, want %v", got, tc.want)
			}
		})
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "vani-bin")
	if err := os.WriteFile(target, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if !sameExecutable(link, target) {
		t.Error("a symlink and its target are the same executable")
	}
	if sameExecutable("", self) || sameExecutable(self, "") {
		t.Error("an empty path matched an executable")
	}
}

// TestDaemonIdentityThisProcess is the real lookup: this process, a same-user
// process that is not this binary, a gone pid, and daemonStartSlack.
func TestDaemonIdentityThisProcess(t *testing.T) {
	old := daemonIdentity
	daemonIdentity = readProcIdentity
	t.Cleanup(func() { daemonIdentity = old })

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	id := readProcIdentity(os.Getpid())
	if !id.OK || id.Started.IsZero() {
		t.Fatalf("this process: %+v", id)
	}
	if !sameExecutable(id.Exe, self) {
		t.Fatalf("executable %q, want %q", id.Exe, self)
	}
	d := daemonRecord{V: 1, PID: os.Getpid(), StartedAt: id.Started}
	if !daemonIsLive(d) {
		t.Fatal("a start equal to the announcement is not this daemon")
	}
	d.StartedAt = time.Now()
	if !daemonIsLive(d) {
		t.Fatal("this process, announced now, is not this daemon")
	}
	// daemonStartSlack: a kernel tick may land up to one second after the
	// wall clock time written at startup. One second is allowed; two is a
	// reused pid.
	d.StartedAt = id.Started.Add(-daemonStartSlack)
	if !daemonIsLive(d) {
		t.Fatalf("kernel start %s is outside %s of the announcement", id.Started, daemonStartSlack)
	}
	d.StartedAt = id.Started.Add(-daemonStartSlack - time.Second)
	if daemonIsLive(d) {
		t.Fatal("a start several seconds later is a reused pid")
	}

	for _, pid := range []int{0, -1, 1 << 30} {
		if got := readProcIdentity(pid); got.OK {
			t.Errorf("pid %d: %+v", pid, got)
		}
	}

	child := otherExecutable(t)
	foreign := readProcIdentity(child)
	if !foreign.OK {
		t.Fatalf("same-user child %d is not inspectable: %+v", child, foreign)
	}
	if sameExecutable(foreign.Exe, self) {
		t.Fatalf("child executable %q is this binary", foreign.Exe)
	}
	announced := foreign.Started.Add(time.Hour)
	if daemonIsLive(daemonRecord{V: 1, PID: child, StartedAt: announced}) {
		t.Fatal("a same-user pid with a different executable is this daemon")
	}
}

func otherExecutable(t *testing.T) int {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("ping", "-n", "30", "127.0.0.1")
	} else {
		cmd = exec.Command("/bin/sleep", "30")
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd.Process.Pid
}

func TestLiveDaemon(t *testing.T) {
	dir := t.TempDir()
	if _, err := liveDaemon(dir); !errors.Is(err, errNoDaemon) {
		t.Fatalf("no record: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Millisecond)
	if err := writeDaemonRecord(dir, 4242, at); err != nil {
		t.Fatal(err)
	}
	oldAlive, oldID := pidAlive, daemonIdentity
	t.Cleanup(func() { pidAlive, daemonIdentity = oldAlive, oldID })

	pidAlive = func(int) bool { return true }
	daemonIdentity = func(int) procIdentity { return procIdentity{} }
	if _, err := liveDaemon(dir); !errors.Is(err, errNoDaemon) {
		t.Fatalf("pidAlive alone: %v", err)
	}
	if f := (&env{stateDir: dir}).daemonFacet(); f.Running || !f.OK {
		t.Fatalf("status running from pidAlive alone: %+v", f)
	}

	daemonIdentity = func(pid int) procIdentity {
		if pid != 4242 {
			return procIdentity{}
		}
		return procIdentity{OK: true, Exe: "/usr/bin/not-vani", Started: at.Add(-time.Hour)}
	}
	if _, err := liveDaemon(dir); !errors.Is(err, errNoDaemon) {
		t.Fatalf("wrong executable: %v", err)
	}
	if f := (&env{stateDir: dir}).daemonFacet(); f.Running {
		t.Fatalf("status running for a different executable: %+v", f)
	}

	daemonIdentity = func(pid int) procIdentity {
		if pid != 4242 {
			return procIdentity{}
		}
		return procIdentity{OK: true, Exe: self, Started: at.Add(5 * time.Second)}
	}
	if _, err := liveDaemon(dir); !errors.Is(err, errNoDaemon) {
		t.Fatalf("reused pid: %v", err)
	}

	daemonIdentity = func(pid int) procIdentity {
		if pid != 4242 {
			return procIdentity{}
		}
		return procIdentity{OK: true, Exe: self, Started: at}
	}
	got, err := liveDaemon(dir)
	if err != nil || got.PID != 4242 {
		t.Fatalf("this binary: %+v, %v", got, err)
	}
	f := (&env{stateDir: dir}).daemonFacet()
	if !f.OK || !f.Running || f.PID != 4242 {
		t.Fatalf("status: %+v", f)
	}
}

// A logical started_at from a test clock can sit far from the kernel. The
// file's mtime is the write. An mtime from before this process started is a
// reused pid.
func TestLiveDaemonClockAndReusedFile(t *testing.T) {
	dir := t.TempDir()
	old := daemonIdentity
	daemonIdentity = readProcIdentity
	t.Cleanup(func() { daemonIdentity = old })

	logical := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Millisecond)
	if err := writeDaemonRecord(dir, os.Getpid(), logical); err != nil {
		t.Fatal(err)
	}
	got, err := liveDaemon(dir)
	if err != nil || got.PID != os.Getpid() || !got.StartedAt.Equal(logical) {
		t.Fatalf("fresh file, logical clock: %+v %v", got, err)
	}
	if f := (&env{stateDir: dir}).daemonFacet(); !f.Running || !f.Since.Equal(logical) {
		t.Fatalf("status: %+v", f)
	}

	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(runsPath(dir, daemonRecordFile), past, past); err != nil {
		t.Fatal(err)
	}
	if _, err := liveDaemon(dir); !errors.Is(err, errNoDaemon) {
		t.Fatalf("file older than this process: %v", err)
	}
}
