package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// The restore message must be true for what the serve then does: a fresh
// request runs, an expired or invalid one is dropped without running, and a
// newer pending request displaces the claim.
func TestKeptClaimRestoreMessageMatchesWhatHappensNext(t *testing.T) {
	cases := []struct {
		name    string
		at      time.Duration
		newer   bool
		want    []string
		wantNot []string
	}{
		{name: "fresh", at: -time.Second, want: []string{"put back as the pending request", "runs once at the next poll"}, wantNot: []string{"without running", "older than"}},
		{name: "expired", at: -2 * runRequestMaxAge, want: []string{"put back as the pending request", fmt.Sprintf("older than %s", runRequestMaxAge), "without running it"}, wantNot: []string{"runs once"}},
		{name: "just past the limit", at: -runRequestMaxAge - time.Minute, want: []string{"without running it"}, wantNot: []string{"runs once"}},
		{name: "future dated", at: time.Hour, want: []string{"not valid", "without running it"}, wantNot: []string{"runs once"}},
		{name: "newer pending", at: -time.Second, newer: true, want: []string{"dropped", "newer request"}, wantNot: []string{"runs once at the next poll"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, stateDir := setup(t)
			handKept(t, stateDir, requestJSON(t, runRequest{V: 1, RequestedAt: time.Now().Add(c.at)}))
			if c.newer {
				writeRequestFile(t, stateDir, requestJSON(t, runRequest{V: 1, RequestedAt: time.Now()}))
			}
			out := clearOnNextDaemon(stateDir)
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("stderr lacks %q:\n%s", w, out)
				}
			}
			for _, w := range c.wantNot {
				if strings.Contains(out, w) {
					t.Errorf("stderr must not contain %q:\n%s", w, out)
				}
			}
			if requestExists(stateDir, runKeptFile) {
				t.Error("marker left behind")
			}
		})
	}
}
