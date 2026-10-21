package main

import (
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
)

// writeLegacyConfig writes a default config that spells decide's model and key
// env with the pre-rename keys.
func writeLegacyConfig(t *testing.T, path string) {
	t.Helper()
	if err := config.Save(path, config.Default()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	d, _ := m["decide"].(map[string]any)
	if d == nil {
		d = map[string]any{}
	}
	delete(d, "openrouter_model")
	delete(d, "openrouter_key_env")
	d["jev_model"] = "openai/gpt-oss-20b"
	d["jev_key_env"] = "OPENROUTER_API_KEY"
	m["decide"] = d
	out, _ := json.Marshal(m)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, old, err := config.LoadWithDeprecations(path); err != nil || len(old) == 0 {
		t.Fatalf("fixture is not a legacy config: old=%v err=%v", old, err)
	}
}

// A config that still uses an old key name must say so on every command that
// reads it. Update-based commands read through config.Update, which drops the
// deprecation list and then rewrites the file with the new names: the user
// is never told what changed.
func TestAdvReadOnlyCommandWarnsAboutDeprecatedKeys(t *testing.T) {
	cfgPath, _ := setup(t)
	writeLegacyConfig(t, cfgPath)
	_, stderr := mustCode(t, 0, "watchlist", "list")
	if !strings.Contains(stderr, "jev_model") {
		t.Fatalf("control: the read-only command did not warn: %q", stderr)
	}
}

func scrubKept(raw, name string) bool {
	u, err := url.Parse(scrubPageURL(raw))
	if err != nil {
		return false
	}
	_, ok := u.Query()[name]
	return ok
}

func TestAdvScrubPageURLParamClasses(t *testing.T) {
	base := "https://careers.example.com/search?q=go&"
	// Must be dropped: they can carry a credential.
	for _, n := range []string{"token", "access_token", "id_token", "idToken", "refresh_token", "token_type", "sessionid",
		"sessionId", "session_id", "JSESSIONID", "sid", "auth", "apikey", "api_key", "client_secret", "secret", "email", "Email",
		"%61uth", "AUTH"} {
		if got := scrubPageURL(base + n + "=v"); strings.Contains(strings.ToLower(got), "=v") {
			t.Errorf("credential-like %q survived: %s", n, got)
		}
	}
	for _, n := range []string{"PHPSESSID", "password", "passwd", "pwd", "jwt", "bearer", "signature", "sig", "code", "state", "otp", "csrf", "x-amz-signature", "credential"} {
		if got := scrubPageURL(base + n + "=v"); strings.Contains(got, n+"=v") {
			t.Logf("INFO credential-like %q is kept: %s", n, got)
		}
	}
	// A real ATS search URL's parameters must survive.
	for _, n := range []string{"keyword", "keywords", "jk", "currentJobId", "gh_jid", "gh_src", "lever-source", "location", "department", "team", "q", "page", "author", "authority"} {
		if !scrubKept(base+n+"=v", n) {
			t.Errorf("search parameter %q was dropped from %s", n, base+n+"=v")
		}
	}
	for _, n := range []string{"monkey", "turkey", "hockey", "jobkey", "sortKey", "department_key", "jobKey", "categoryKey"} {
		if !scrubKept(base+n+"=v", n) {
			t.Logf("INFO false drop: %q is dropped (key-suffix rule)", n)
		}
	}
}

func TestAdvScrubPageURLStructure(t *testing.T) {
	for _, in := range []string{
		"https://x.example.com/s?q=1#access_token=abc&id_token=def",
		"https://u:pw@x.example.com/s?q=1",
		"https://x.example.com/s?q=1&token=a&token=b&q=2",
		"https://x.example.com/s?q=1&%74oken=abc",
		"https://x.example.com/s?q=1;token=abc",
		"https://x.example.com/s?q=1&to%6ben=abc",
	} {
		got := scrubPageURL(in)
		for _, leak := range []string{"abc", "def", "pw", "token=a", "token=b", "#"} {
			if strings.Contains(got, leak) {
				t.Errorf("scrubPageURL(%q) = %q leaks %q", in, got, leak)
			}
		}
	}
	if got := scrubPageURL("https://x.example.com/s?q=1&q=2&location=a+b"); got != "https://x.example.com/s?location=a+b&q=1&q=2" {
		t.Errorf("repeated or encoded values changed: %s", got)
	}
}
