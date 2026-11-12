package main

import (
	"io"
	"testing"
)

// serve sets a soft heap limit only when GOMEMLIMIT names none, never below
// its floor and always above the live heap it finds.
func TestServeLimitDefersToGOMEMLIMITAndKeepsHeadroom(t *testing.T) {
	for _, v := range []string{"off", "2GiB", "123456789"} {
		if serveLimitFunc(func(string) string { return v }) != nil {
			t.Errorf("GOMEMLIMIT=%s: serve would set its own limit", v)
		}
	}
	if serveLimitFunc(func(string) string { return "" }) == nil {
		t.Fatal("GOMEMLIMIT unset: serve would set no limit")
	}
	if got := serveLimit(0); got != serveMemoryLimit {
		t.Errorf("no live heap: %d, want the floor %d", got, serveMemoryLimit)
	}
	if got := serveLimit(400 << 20); got != serveMemoryLimit {
		t.Errorf("a small heap: %d, want the floor", got)
	}
	if got, want := serveLimit(1<<30), int64(1<<30)*8/5; got != want {
		t.Errorf("a 1 GiB heap: %d, want %d", got, want)
	}
}

// The limit is set once, after the first view is answered: not before it (a
// cold first list derives every posting's facts above the floor), not after a
// show, and not again.
func TestServeLimitAfterTheFirstView(t *testing.T) {
	e, _ := sameFixture(t)
	res, err := e.openJobsResident()
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	s := newJobsServer(e, res, io.Discard)
	n := 0
	s.limit = func() { n++ }
	run := func(op string, args ...string) {
		t.Helper()
		call := &serveCall{id: op, done: make(chan struct{})}
		s.mu.Lock()
		s.running++
		s.mu.Unlock()
		s.pending.Add(1) // answer releases it, as accept would have taken it
		s.compute(call, op, serveOps[op], args)
	}
	run("show", "gh:acme:00")
	if n != 0 {
		t.Fatal("a show set the limit")
	}
	run("list", "--view", "nope")
	if n != 0 {
		t.Fatal("a failed list set the limit")
	}
	run("list", "--view", "new")
	run("stats", "--view", "new")
	if n != 1 {
		t.Fatalf("the limit was set %d times, want once after the first view", n)
	}
}
