package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// A relative VANI_CONFIG or VANI_STATE_DIR is refused before any command runs.
// A caller that asked for JSON gets {"error","code"} naming the variable, still
// with exit 2 (TestLaunchdPlistRefusesRelativePaths holds the plain-text form).
func TestRelativeEnvRepliesInJSONNamingTheVariable(t *testing.T) {
	abs := t.TempDir()
	for _, tc := range []struct {
		env, val string
	}{
		{"VANI_CONFIG", "rel/config.json"},
		{"VANI_CONFIG", "./c.json"},
		{"VANI_STATE_DIR", "rel/state"},
		{"VANI_STATE_DIR", "."},
	} {
		for _, args := range [][]string{{"draft"}, {"--json", "status"}, {"status", "--json"}, {"jobs", "list", "--json=true"}, {"config", "models"}} {
			t.Run(tc.env+"="+tc.val+"/"+strings.Join(args, " "), func(t *testing.T) {
				t.Setenv("VANI_CONFIG", filepath.Join(abs, "c.json"))
				t.Setenv("VANI_STATE_DIR", filepath.Join(abs, "s"))
				t.Setenv(tc.env, tc.val)
				code, o, e := do(t, args...)
				if code != exitUsage {
					t.Fatalf("exit %d, want %d (stderr %q)", code, exitUsage, e)
				}
				var r errorReply
				if err := json.Unmarshal([]byte(o), &r); err != nil {
					t.Fatalf("stdout %q is not the error envelope: %v", o, err)
				}
				if r.Code != codeRelativePath || !strings.Contains(r.Error, tc.env) || !strings.Contains(r.Error, "absolute") {
					t.Errorf("envelope %+v does not name %s with code %q", r, tc.env, codeRelativePath)
				}
				if strings.Count(o, "\n") != 1 {
					t.Errorf("stdout %q is not exactly one reply", o)
				}
			})
		}
	}
}

// No JSON asked: stdout stays empty, as TestW5aRelativeEnvPathExitsUsageForEveryCommand
// and TestLaunchdPlistRefusesRelativePaths expect. --json=false and an unknown
// command are not asks either.
func TestRelativeEnvWritesNoReplyUnlessJSONAsked(t *testing.T) {
	for _, args := range [][]string{{"status"}, {"jobs", "list"}, {"status", "--json=false"}, {"bogus"}, {"--json=maybe", "status"}, nil} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Setenv("VANI_CONFIG", "rel.json")
			code, o, e := do(t, args...)
			if code != exitUsage || o != "" || !strings.Contains(e, "VANI_CONFIG") {
				t.Fatalf("exit %d stdout %q stderr %q", code, o, e)
			}
		})
	}
}
