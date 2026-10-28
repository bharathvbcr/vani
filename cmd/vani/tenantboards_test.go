package main

import (
	"slices"
	"strings"
	"testing"
)

// `vani watchlist add` takes a Recruitee, Pinpoint, BambooHR or Personio board
// by its subdomain, allows that tenant's own host with --allow-host, and
// refuses a token that is not one host label.
func TestWatchlistAddPerTenantBoards(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	for _, tc := range []struct{ ats, token, host string }{
		{"rc", "syrve", "syrve.recruitee.com"},
		{"pp", "workwithus", "workwithus.pinpointhq.com"},
		{"bh", "sortitt", "sortitt.bamboohr.com"},
		{"ps", "deskbird", "deskbird.jobs.personio.com"},
	} {
		mustCode(t, 0, "watchlist", "add", "--ats", tc.ats, "--token", tc.token, "--company", "Co", "--allow-host")
		cfg := watchlistOf(t, cfgPath)
		if !slices.Contains(cfg.AllowedHosts, tc.host) {
			t.Errorf("%s: allowed_hosts %v lacks %s", tc.ats, cfg.AllowedHosts, tc.host)
		}
		_, e := mustCode(t, 1, "watchlist", "add", "--ats", tc.ats, "--token", tc.token, "--company", "Co")
		if !strings.Contains(e, "already on the watchlist") {
			t.Errorf("%s: duplicate stderr %q", tc.ats, e)
		}
		for _, bad := range [][]string{
			{"--token", "a.b"},
			{"--token", "Acme"},
			{"--token", "a_b"},
			{"--token", "a/b"},
			{"--token", "evil.example"},
			{"--token", "ok", "--search", "engineer"},
			{"--token", "ok", "--host", tc.host},
			{"--token", "ok", "--url", "https://" + tc.host},
		} {
			args := append([]string{"watchlist", "add", "--ats", tc.ats, "--company", "Co"}, bad...)
			mustCode(t, 2, args...)
		}
	}
	mustCode(t, 0, "config", "check")
	o, _ := mustCode(t, 0, "watchlist", "list")
	for _, want := range []string{"rc\tsyrve", "pp\tworkwithus", "bh\tsortitt", "ps\tdeskbird"} {
		if !strings.Contains(o, want) {
			t.Errorf("list lacks %q: %q", want, o)
		}
	}
}
