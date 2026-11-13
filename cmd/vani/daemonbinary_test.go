package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// status names three different things that daemonIsLive alone calls "not
// live": the daemon of this binary, a live daemon of another vani binary, and
// no daemon. The first reading of "not live" was "is gone (it did not exit
// cleanly)" for all of them, which told a person looking at a running daemon
// from another build that it had crashed.
func TestDaemonFacetTellsApartLiveOtherBinaryAndGone(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const other = "/opt/vani-other/bin/vani"
	at := time.Now().UTC().Truncate(time.Millisecond)
	oldID := daemonIdentity
	t.Cleanup(func() { daemonIdentity = oldID })
	lookup := func(id procIdentity) { daemonIdentity = func(int) procIdentity { return id } }

	facetFor := func(t *testing.T, rec daemonRecord) daemonFacet {
		t.Helper()
		dir := t.TempDir()
		rec.V, rec.PID, rec.StartedAt = runRequestVersion, 4242, at
		if err := writeDaemonAnnouncement(dir, rec); err != nil {
			t.Fatal(err)
		}
		return (&env{stateDir: dir}).daemonFacet()
	}

	t.Run("this binary", func(t *testing.T) {
		lookup(procIdentity{OK: true, Exe: self, Started: at})
		f := facetFor(t, daemonRecord{Exe: self})
		if !f.OK || !f.Running || f.Binary != daemonBinaryThis || f.PID != 4242 {
			t.Fatalf("%+v", f)
		}
	})

	t.Run("a live daemon of another vani binary", func(t *testing.T) {
		lookup(procIdentity{OK: true, Exe: other, Started: at})
		f := facetFor(t, daemonRecord{Exe: other})
		if !f.OK || !f.Running || f.Binary != daemonBinaryOther || f.Exe != other || f.PID != 4242 {
			t.Fatalf("a daemon running another binary is running: %+v", f)
		}
		if !strings.Contains(f.Reason, other) || strings.Contains(f.Reason, "gone") || strings.Contains(f.Reason, "did not exit") {
			t.Fatalf("reason must name the other binary and not call the daemon gone: %q", f.Reason)
		}
	})

	t.Run("the other binary is still the one that announced", func(t *testing.T) {
		// The process at the pid must be the executable the record names. A
		// record from vani A whose pid now runs something else is not a daemon.
		lookup(procIdentity{OK: true, Exe: "/usr/bin/not-vani", Started: at})
		f := facetFor(t, daemonRecord{Exe: other})
		if f.Running || f.Binary != "" {
			t.Fatalf("a pid running an executable the record does not name is not a daemon: %+v", f)
		}
		if strings.Contains(f.Reason, "is gone") || !strings.Contains(f.Reason, "/usr/bin/not-vani") {
			t.Fatalf("it is alive, so not gone; the reason names what runs there: %q", f.Reason)
		}
	})

	t.Run("a record that names no executable cannot confirm another binary", func(t *testing.T) {
		lookup(procIdentity{OK: true, Exe: other, Started: at})
		f := facetFor(t, daemonRecord{})
		if f.Running || f.Binary != "" {
			t.Fatalf("an executable nothing recorded is not confirmed: %+v", f)
		}
		if strings.Contains(f.Reason, "did not exit cleanly") || !strings.Contains(f.Reason, other) {
			t.Fatalf("alive, so it did not exit; say what runs there: %q", f.Reason)
		}
	})

	t.Run("a reused pid is gone even when it runs the recorded executable", func(t *testing.T) {
		lookup(procIdentity{OK: true, Exe: other, Started: at.Add(time.Hour)})
		f := facetFor(t, daemonRecord{Exe: other})
		if f.Running || f.Binary != "" || !strings.Contains(f.Reason, "is gone") {
			t.Fatalf("started after the announcement: %+v", f)
		}
		lookup(procIdentity{OK: true, Exe: self, Started: at.Add(time.Hour)})
		if f := facetFor(t, daemonRecord{Exe: self}); f.Running || !strings.Contains(f.Reason, "is gone") {
			t.Fatalf("this binary started after the announcement: %+v", f)
		}
	})

	t.Run("gone", func(t *testing.T) {
		lookup(procIdentity{})
		f := facetFor(t, daemonRecord{Exe: other})
		if f.Running || f.Binary != "" || !strings.Contains(f.Reason, "is gone") {
			t.Fatalf("%+v", f)
		}
	})
}

// daemonIsLive keeps its meaning: only a daemon of this binary. The other
// binary is classified apart from it, never merged into it.
func TestDaemonIsLiveStaysThisBinaryOnly(t *testing.T) {
	const other = "/opt/vani-other/bin/vani"
	at := time.Now().UTC().Truncate(time.Millisecond)
	oldID := daemonIdentity
	t.Cleanup(func() { daemonIdentity = oldID })
	daemonIdentity = func(int) procIdentity { return procIdentity{OK: true, Exe: other, Started: at} }
	rec := daemonRecord{V: 1, PID: 4242, StartedAt: at, Exe: other}
	if daemonIsLive(rec) {
		t.Fatal("a daemon of another binary is not this binary's daemon")
	}
	if got, exe := classifyDaemon(rec); got != daemonOtherBinary || exe != other {
		t.Fatalf("classified %v %q", got, exe)
	}
}

// liveDaemon (the run request path) still refuses a daemon of another binary,
// but says why instead of calling it gone.
func TestLiveDaemonNamesTheOtherBinary(t *testing.T) {
	const other = "/opt/vani-other/bin/vani"
	dir := t.TempDir()
	at := time.Now().UTC().Truncate(time.Millisecond)
	if err := writeDaemonAnnouncement(dir, daemonRecord{V: runRequestVersion, PID: 4242, StartedAt: at, Exe: other}); err != nil {
		t.Fatal(err)
	}
	oldID := daemonIdentity
	t.Cleanup(func() { daemonIdentity = oldID })
	daemonIdentity = func(int) procIdentity { return procIdentity{OK: true, Exe: other, Started: at} }
	_, err := liveDaemon(dir)
	if !errors.Is(err, errNoDaemon) {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), "is gone") || !strings.Contains(err.Error(), other) {
		t.Fatalf("%v", err)
	}
}

// The facet's JSON carries the new keys additively; an old reader that knows
// neither still reads running.
func TestDaemonFacetJSONIsAdditive(t *testing.T) {
	raw, err := json.Marshal(daemonFacet{facet: facet{OK: true}, Running: true, PID: 7, Binary: daemonBinaryOther, Exe: "/x/vani"})
	if err != nil {
		t.Fatal(err)
	}
	var old struct {
		Running bool `json:"running"`
		PID     int  `json:"pid"`
	}
	if err := json.Unmarshal(raw, &old); err != nil || !old.Running || old.PID != 7 {
		t.Fatalf("%s: %v %+v", raw, err, old)
	}
	if !strings.Contains(string(raw), `"binary":"other"`) || !strings.Contains(string(raw), `"exe":"/x/vani"`) {
		t.Fatalf("%s", raw)
	}
	plain, _ := json.Marshal(daemonFacet{facet: facet{OK: true}, Running: true, PID: 7, Binary: daemonBinaryThis})
	if strings.Contains(string(plain), `"exe"`) {
		t.Fatalf("this binary's daemon names no other path: %s", plain)
	}
}
