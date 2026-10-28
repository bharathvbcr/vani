package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// A rewrite of daemon.json a year after started_at is still not a new
// announcement. Replacing the file with a symlink must not be followed.
func TestLiveDaemonMtimeAYearAheadAndSymlink(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC).Truncate(time.Millisecond)
	oldAlive, oldID := pidAlive, daemonIdentity
	t.Cleanup(func() { pidAlive, daemonIdentity = oldAlive, oldID })
	pidAlive = func(int) bool { return true }

	t.Run("mtime a year ahead", func(t *testing.T) {
		dir := t.TempDir()
		if err := writeDaemonRecord(dir, 4242, t0); err != nil {
			t.Fatal(err)
		}
		path := runsPath(dir, daemonRecordFile)
		year := t0.AddDate(1, 0, 0)
		if err := os.Chtimes(path, year, year); err != nil {
			t.Fatal(err)
		}
		set := func(start time.Time) {
			t.Helper()
			daemonIdentity = func(pid int) procIdentity {
				if pid != 4242 {
					return procIdentity{}
				}
				return procIdentity{OK: true, Exe: self, Started: start}
			}
		}
		set(t0.Add(30 * time.Minute))
		if _, err := liveDaemon(dir); !errors.Is(err, errNoDaemon) {
			t.Fatalf("start 30m after the record, mtime a year ahead: %v", err)
		}
		f := (&env{stateDir: dir}).daemonFacet()
		if f.Running || !f.Since.Equal(t0) || f.Since.Equal(year) {
			t.Fatalf("facet %+v, want not running since %s", f, t0)
		}
		set(t0)
		got, err := liveDaemon(dir)
		if err != nil || got.PID != 4242 || !got.StartedAt.Equal(t0) {
			t.Fatalf("start equal to the record: %+v %v", got, err)
		}
		f = (&env{stateDir: dir}).daemonFacet()
		if !f.Running || !f.Since.Equal(t0) || f.Since.Equal(year) {
			t.Fatalf("facet %+v, want running since %s", f, t0)
		}
	})

	t.Run("symlink is not followed", func(t *testing.T) {
		dir := t.TempDir()
		if err := writeDaemonRecord(dir, 4242, t0); err != nil {
			t.Fatal(err)
		}
		path := runsPath(dir, daemonRecordFile)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		side := filepath.Join(dir, "payload.json")
		if err := os.WriteFile(side, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(side, path); err != nil {
			t.Fatal(err)
		}
		daemonIdentity = func(pid int) procIdentity {
			if pid != 4242 {
				return procIdentity{}
			}
			return procIdentity{OK: true, Exe: self, Started: t0}
		}
		if got, err := readBoundedFile(path, maxDaemonFile); err == nil {
			t.Fatalf("symlink daemon.json was read: %q", got)
		}
		if _, err := liveDaemon(dir); !errors.Is(err, errNoDaemon) {
			t.Fatalf("symlink liveDaemon: %v", err)
		}
		f := (&env{stateDir: dir}).daemonFacet()
		if f.Running || f.OK {
			t.Fatalf("symlink facet %+v, want unreadable and not running", f)
		}
	})
}

// pidAlive answering yes does not make an uninspectable identity this daemon.
func TestLiveDaemonPidAliveDoesNotIdentify(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC).Truncate(time.Millisecond)
	dir := t.TempDir()
	if err := writeDaemonRecord(dir, 4242, at); err != nil {
		t.Fatal(err)
	}
	oldAlive, oldID := pidAlive, daemonIdentity
	t.Cleanup(func() { pidAlive, daemonIdentity = oldAlive, oldID })
	pidAlive = func(int) bool { return true }
	cases := []struct {
		name string
		id   procIdentity
	}{
		{"empty exe", procIdentity{OK: true, Started: at}},
		{"zero start", procIdentity{OK: true, Exe: self}},
		{"not ok", procIdentity{OK: false, Exe: self, Started: at}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			daemonIdentity = func(int) procIdentity { return tc.id }
			if daemonIsLive(daemonRecord{V: 1, PID: 4242, StartedAt: at}) {
				t.Fatal("daemonIsLive")
			}
			if _, err := liveDaemon(dir); !errors.Is(err, errNoDaemon) {
				t.Fatalf("liveDaemon: %v", err)
			}
			f := (&env{stateDir: dir}).daemonFacet()
			if !f.OK || f.Running || !f.Since.Equal(at) {
				t.Fatalf("facet %+v, want not running since %s", f, at)
			}
		})
	}
}

func TestDaemonIdentitySameExecutable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "vani-bin")
	if err := os.WriteFile(target, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	linkA := filepath.Join(dir, "link-a")
	linkB := filepath.Join(dir, "link-b")
	if err := os.Symlink(target, linkA); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, linkB); err != nil {
		t.Fatal(err)
	}
	if !sameExecutable(linkA, linkB) || !sameExecutable(linkB, linkA) {
		t.Fatal("two symlinks to the same file did not match")
	}

	// ".." alone is lexical, so a missing parent does not fail EvalSymlinks.
	// A dangling symlink in front of ".." does: one side resolves, the other
	// does not, and the fallback is Clean of both. That Clean path is the
	// link, not the file the link points at.
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink("missing-target", dangling); err != nil {
		t.Fatal(err)
	}
	// Join would Clean the ".." away before EvalSymlinks sees the dangling link.
	viaDangling := dangling + "/../" + filepath.Base(linkA)
	if _, err := filepath.EvalSymlinks(viaDangling); err == nil {
		t.Fatal("fixture: EvalSymlinks succeeded, so this is not the Clean fallback")
	}
	if _, err := filepath.EvalSymlinks(linkA); err != nil {
		t.Fatalf("fixture: link: %v", err)
	}
	if !sameExecutable(linkA, viaDangling) || !sameExecutable(viaDangling, linkA) {
		t.Fatal("Clean fallback did not match the link path")
	}
	if sameExecutable(viaDangling, target) {
		t.Fatal("Clean fallback matched the symlink target")
	}

	if sameExecutable("", target) || sameExecutable(target, "") || sameExecutable("", "") {
		t.Fatal("an empty path matched")
	}

	longer := target + "-extra"
	if err := os.WriteFile(longer, []byte("y"), 0o700); err != nil {
		t.Fatal(err)
	}
	if sameExecutable(target, longer) || sameExecutable(longer, target) {
		t.Fatal("a path matched a path it is a prefix of")
	}
	if sameExecutable(target, target+"/extra") || sameExecutable(target+"/extra", target) {
		t.Fatal("a path matched a child path")
	}

	if runtime.GOOS == "darwin" {
		upper := filepath.Join(dir, "VaniCase")
		lower := filepath.Join(dir, "vanicase")
		if sameExecutable(upper, lower) || sameExecutable(lower, upper) {
			t.Fatal("darwin case difference matched")
		}
		if err := os.WriteFile(upper, []byte("z"), 0o700); err != nil {
			t.Fatal(err)
		}
		if sameExecutable(upper, lower) || sameExecutable(lower, upper) {
			t.Fatal("darwin case difference of an existing file matched")
		}
	}
}

func TestDaemonIdentitySlackNanosecond(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)
	old := daemonIdentity
	t.Cleanup(func() { daemonIdentity = old })
	rec := daemonRecord{V: 1, PID: 4242, StartedAt: at}
	set := func(start time.Time) {
		t.Helper()
		daemonIdentity = func(int) procIdentity {
			return procIdentity{OK: true, Exe: self, Started: start}
		}
	}
	set(at.Add(daemonStartSlack))
	if !daemonIsLive(rec) {
		t.Fatal("start exactly at started_at+slack is not live")
	}
	set(at.Add(daemonStartSlack + time.Nanosecond))
	if daemonIsLive(rec) {
		t.Fatal("start 1ns past slack is live")
	}
	// No lower bound: a kernel start far before the announcement is still
	// this process. A zero start is not a time, and stays not live.
	set(time.Unix(1, 0).UTC())
	if !daemonIsLive(rec) {
		t.Fatal("a start far before the announcement is not live")
	}
}

func TestDaemonIdentityGonePID(t *testing.T) {
	for _, pid := range []int{0, -1} {
		got := readProcIdentity(pid)
		if got.OK || got.Exe != "" || !got.Started.IsZero() {
			t.Errorf("pid %d: %+v", pid, got)
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if processExists(cmd.Process.Pid) {
		t.Fatalf("reaped child %d still exists", cmd.Process.Pid)
	}
	if got := readProcIdentity(cmd.Process.Pid); got.OK || got.Exe != "" || !got.Started.IsZero() {
		t.Fatalf("gone pid %d: %+v", cmd.Process.Pid, got)
	}

	old := daemonIdentity
	daemonIdentity = readProcIdentity
	t.Cleanup(func() { daemonIdentity = old })
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	id := readProcIdentity(os.Getpid())
	if !id.OK || id.Exe == "" || id.Started.IsZero() {
		t.Fatalf("this process: %+v", id)
	}
	if !sameExecutable(id.Exe, self) {
		t.Fatalf("executable %q, want %q", id.Exe, self)
	}
	if !daemonIsLive(daemonRecord{V: 1, PID: os.Getpid(), StartedAt: id.Started}) {
		t.Fatal("started_at equal to the kernel start is not live")
	}
}

// Assigning daemonIdentity while daemonIsLive reads it races the test hook
// (pidalive.go), not the check. The installed lookup swaps the identity it
// returns; the var itself is restored when the test ends.
func TestDaemonIdentityConcurrentSwap(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)
	live := procIdentity{OK: true, Exe: self, Started: at}
	var cur atomic.Value
	cur.Store(procIdentity{})
	old := daemonIdentity
	t.Cleanup(func() { daemonIdentity = old })
	daemonIdentity = func(int) procIdentity {
		id, _ := cur.Load().(procIdentity)
		return id
	}
	rec := daemonRecord{V: 1, PID: 4242, StartedAt: at}
	stop := make(chan struct{})
	var flip sync.WaitGroup
	flip.Add(1)
	go func() {
		defer flip.Done()
		on := false
		for {
			select {
			case <-stop:
				return
			default:
			}
			if on {
				cur.Store(live)
			} else {
				cur.Store(procIdentity{})
			}
			on = !on
		}
	}()
	var workers sync.WaitGroup
	var sawLive, sawEmpty atomic.Bool
	var panics atomic.Int32
	workers.Add(50)
	for i := 0; i < 50; i++ {
		go func() {
			defer workers.Done()
			for j := 0; j < 200; j++ {
				func() {
					defer func() {
						if recover() != nil {
							panics.Add(1)
						}
					}()
					if daemonIsLive(rec) {
						sawLive.Store(true)
						return
					}
					sawEmpty.Store(true)
				}()
			}
		}()
	}
	workers.Wait()
	close(stop)
	flip.Wait()
	if panics.Load() != 0 {
		t.Fatalf("%d panics", panics.Load())
	}
	if !sawLive.Load() || !sawEmpty.Load() {
		t.Fatalf("saw live %v, saw empty %v", sawLive.Load(), sawEmpty.Load())
	}
}

// processExists and readProcIdentity call the kernel directly, so EPERM and
// ERROR_ACCESS_DENIED cannot be injected below daemonIdentity. On Windows,
// pidalive_windows.go returns not-alive from every OpenProcess error,
// ERROR_ACCESS_DENIED included; that path was read and not executed.
func TestDaemonIdentityAccessDeniedIsNotLive(t *testing.T) {
	if runtime.GOOS != "windows" {
		err := syscall.Kill(1, 0)
		if !errors.Is(err, syscall.EPERM) {
			t.Fatalf("pid 1: %v, want EPERM", err)
		}
		if processExists(1) {
			t.Fatal("EPERM reads as alive")
		}
		if id := readProcIdentity(1); id.OK || id.Exe != "" || !id.Started.IsZero() {
			t.Fatalf("EPERM identity: %+v", id)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC).Truncate(time.Millisecond)
	dir := t.TempDir()
	if err := writeDaemonRecord(dir, 4242, at); err != nil {
		t.Fatal(err)
	}
	oldAlive, oldID := pidAlive, daemonIdentity
	t.Cleanup(func() { pidAlive, daemonIdentity = oldAlive, oldID })
	pidAlive = func(int) bool { return true }
	daemonIdentity = func(int) procIdentity {
		return procIdentity{OK: false, Exe: self, Started: at}
	}
	if daemonIsLive(daemonRecord{V: 1, PID: 4242, StartedAt: at}) {
		t.Fatal("an uninspectable identity is live")
	}
	if _, err := liveDaemon(dir); !errors.Is(err, errNoDaemon) {
		t.Fatalf("liveDaemon: %v", err)
	}
	f := (&env{stateDir: dir}).daemonFacet()
	if !f.OK || f.Running || !f.Since.Equal(at) {
		t.Fatalf("facet %+v", f)
	}
}

// runState stays an existence check. An identity that is not this daemon does
// not end the run, and a pid the kernel will not show us is not still going.
func TestInterruptedRunStaysAnExistenceCheck(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	rec := runRecord{PID: 4242, StartedAt: at.Add(-time.Minute), Outcome: outcomeRunning}
	oldAlive, oldID := pidAlive, daemonIdentity
	t.Cleanup(func() { pidAlive, daemonIdentity = oldAlive, oldID })

	pidAlive = func(int) bool { return true }
	daemonIdentity = func(int) procIdentity {
		return procIdentity{OK: true, Exe: self + ".other", Started: rec.StartedAt}
	}
	if daemonIsLive(daemonRecord{V: 1, PID: rec.PID, StartedAt: rec.StartedAt}) {
		t.Fatal("a different executable is this daemon")
	}
	if runState(rec, at) != outcomeRunning {
		t.Fatal("run-still-going followed identity instead of existence")
	}

	daemonIdentity = func(int) procIdentity { return procIdentity{} }
	if daemonIsLive(daemonRecord{V: 1, PID: rec.PID, StartedAt: rec.StartedAt}) {
		t.Fatal("an uninspectable identity is this daemon")
	}
	if runState(rec, at) != outcomeRunning {
		t.Fatal("run-still-going treated an unseen identity as gone while the pid exists")
	}

	pidAlive = func(int) bool { return false }
	if runState(rec, at) != outcomeInterrupted {
		t.Fatal("an uninspectable pid is still going")
	}
}
