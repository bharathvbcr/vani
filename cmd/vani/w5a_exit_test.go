package main

import (
	"strings"
	"testing"
)

// Every command, not only launchd plist, refuses a relative VANI_STATE_DIR or
// VANI_CONFIG with exit 2 (usage), naming the variable and "absolute": the
// Mac app reads the exit code.
func TestW5aRelativeEnvPathExitsUsageForEveryCommand(t *testing.T) {
	for _, tc := range []struct{ env, val string }{
		{"VANI_STATE_DIR", "rel/state"},
		{"VANI_CONFIG", "rel/config.json"},
	} {
		for _, cmd := range [][]string{{"status"}, {"jobs", "list"}} {
			t.Run(tc.env+"/"+cmd[0], func(t *testing.T) {
				t.Setenv(tc.env, tc.val)
				code, o, e := do(t, cmd...)
				if code != 2 {
					t.Fatalf("exit %d, want 2 (stderr %q)", code, e)
				}
				if o != "" {
					t.Errorf("printed on refusal: %q", o)
				}
				if !strings.Contains(e, tc.env) || !strings.Contains(e, "absolute") {
					t.Errorf("stderr %q does not name %s as not absolute", e, tc.env)
				}
			})
		}
	}
}
