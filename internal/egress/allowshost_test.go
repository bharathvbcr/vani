package egress

import "testing"

// AllowsHost is the allowlist's answer without a client: it must agree with
// the client New builds from the same entries, for every spelling.
func TestAllowsHostMatchesClient(t *testing.T) {
	entries := []string{"Boards-API.greenhouse.io.", "*.Lever.co", "api.ashbyhq.com", "https://bad.example.com", "*.com"}
	c, err := New(Config{AllowedHosts: []string{"boards-api.greenhouse.io", "*.lever.co", "api.ashbyhq.com"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"boards-api.greenhouse.io", "BOARDS-API.greenhouse.io.", "api.eu.lever.co", "lever.co", "api.ashbyhq.com",
		"ashbyhq.com", "bad.example.com", "example.com", "", " ", "evil-lever.co", "x.evil.com"} {
		if got, want := AllowsHost(entries, h), c.allows(normHost(h)); got != want {
			t.Errorf("AllowsHost(%q) = %v, client allows = %v", h, got, want)
		}
	}
}
