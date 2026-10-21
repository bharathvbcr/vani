package main

import (
	"strings"
	"testing"
)

// Update-based commands read through config.Update, which drops the
// deprecation list and then rewrites the file with the new names: the user
// is never told which key was renamed.
func TestAdvUpdateCommandsStillWarnAboutDeprecatedKeys(t *testing.T) {
	cfgPath, _ := setup(t)
	writeLegacyConfig(t, cfgPath)
	_, stderr := mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "acme", "--company", "Acme", "--allow-host")
	if !strings.Contains(stderr, "jev_model") {
		t.Errorf("watchlist add rewrote a config using decide.jev_model and said nothing; stderr %q", stderr)
	}
}

// A PHP session id in a saved listing URL is a credential in a 0600 file that
// is fetched daily.
func TestAdvScrubDropsSessionAndPasswordParams(t *testing.T) {
	for _, n := range []string{"PHPSESSID", "password", "passwd", "jwt"} {
		if got := scrubPageURL("https://careers.example.com/search?q=go&" + n + "=v"); strings.Contains(got, n+"=v") {
			t.Errorf("%q kept: %s", n, got)
		}
	}
}
