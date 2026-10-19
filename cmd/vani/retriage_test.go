package main

import (
	"strings"
	"testing"
)

func TestRetriageCommandOnAnEmptyLedger(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	out, _ := mustCode(t, 0, "retriage", "--dry-run")
	if !strings.Contains(out, "0 posting(s) would return to live") {
		t.Errorf("dry run = %q", out)
	}
	out, _ = mustCode(t, 0, "retriage")
	if !strings.Contains(out, "0 posting(s) returned to live") {
		t.Errorf("run = %q", out)
	}
	if code, _, _ := do(t, "retriage", "extra"); code != 2 {
		t.Errorf("a stray argument exited %d, want 2", code)
	}
}
