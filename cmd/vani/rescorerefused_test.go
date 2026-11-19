package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// A refused rescore (the default config has no devprism.binary) must say it was
// refused and exit non-zero; it must not print a success-looking count.
func TestRescoreRefusedPrintsNoSuccessCount(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	code, stdout, stderr := do(t, "rescore")
	if code == 0 || code == 2 {
		t.Fatalf("exit %d, want a runtime failure", code)
	}
	if strings.Contains(stdout, "rescored") || strings.Contains(stdout, "still unscored") {
		t.Errorf("a refused rescore printed a count on stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "refused") || !strings.Contains(stderr, "devprism.binary is empty") {
		t.Errorf("stderr must say it was refused and why: %q", stderr)
	}
}

func TestRescoreRefusedJSONSaysRefused(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	code, stdout, stderr := do(t, "rescore", "--json")
	if code == 0 || code == 2 {
		t.Fatalf("exit %d, want a runtime failure\n%s\n%s", code, stdout, stderr)
	}
	var v struct {
		Refused bool   `json:"refused"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, stdout)
	}
	if !v.Refused || !strings.Contains(v.Error, "devprism.binary is empty") {
		t.Errorf("reply %+v", v)
	}
}
