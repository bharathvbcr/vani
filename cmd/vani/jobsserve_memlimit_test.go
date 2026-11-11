package main

import (
	"math"
	"runtime/debug"
	"testing"
)

// serve sets its soft heap limit only when GOMEMLIMIT names none: a limit the
// owner set (or GOMEMLIMIT=off) stands.
func TestServeMemoryLimitDefersToGOMEMLIMIT(t *testing.T) {
	prev := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(prev) })

	debug.SetMemoryLimit(math.MaxInt64)
	for _, v := range []string{"off", "2GiB", "123456789"} {
		if setServeMemoryLimit(func(string) string { return v }) {
			t.Errorf("GOMEMLIMIT=%s: serve set its own limit", v)
		}
		if got := debug.SetMemoryLimit(-1); got != math.MaxInt64 {
			t.Errorf("GOMEMLIMIT=%s: the limit moved to %d", v, got)
		}
	}
	if !setServeMemoryLimit(func(string) string { return "" }) {
		t.Fatal("GOMEMLIMIT unset: serve set no limit")
	}
	if got := debug.SetMemoryLimit(-1); got != serveMemoryLimit {
		t.Fatalf("limit %d, want %d", got, serveMemoryLimit)
	}
}
