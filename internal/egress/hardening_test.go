package egress

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// rawConnect sends a CONNECT whose request-target and Host header are chosen
// independently (host "" sends no Host header) and returns the status.
func rawConnect(t *testing.T, addr, token, target, host string) int {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	req := "CONNECT " + target + " HTTP/1.1\r\n"
	if host != "" {
		req += "Host: " + host + "\r\n"
	}
	req += "Proxy-Authorization: Bearer " + token + "\r\n\r\n"
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

// RFC 9110 §9.3.6: the CONNECT destination is the request-target, and a
// proxy ignores the Host header (RFC 9112 §3.2.2). net/http does exactly
// that for an authority-form target: it sets Request.Host from the target
// and drops the header, so a Host without the port, or naming another host,
// cannot steer the dial. (The 2026-10-19 audit read this as a 400 for a Host
// without ":443" and a Host/target desync; neither happens.) A target that
// is not host:port is refused whatever the Host header says.
func TestProxyConnectTargetIsTheRequestTarget(t *testing.T) {
	_, addr, token, dialed := startProxy(t, ProxyConfig{}, map[string][]string{
		"jobs.example.com": {publicIP},
		"evil.example.com": {"93.184.216.35"},
	})
	for name, tc := range map[string]struct {
		target, host string
		want         int
	}{
		"host header with port":        {"jobs.example.com:443", "jobs.example.com:443", http.StatusOK},
		"host header without port":     {"jobs.example.com:443", "jobs.example.com", http.StatusOK},
		"no host header":               {"jobs.example.com:443", "", http.StatusOK},
		"host header names other host": {"jobs.example.com:443", "evil.example.com", http.StatusOK},
		"host header other port":       {"jobs.example.com:443", "jobs.example.com:8443", http.StatusOK},
		"target without port":          {"jobs.example.com", "jobs.example.com:443", http.StatusBadRequest},
		"target not allowed":           {"evil.example.com:443", "jobs.example.com", http.StatusForbidden},
	} {
		if code := rawConnect(t, addr, token, tc.target, tc.host); code != tc.want {
			t.Errorf("%s: status %d, want %d", name, code, tc.want)
		}
	}
	// Every dial went to the target's vetted address, never the Host header's.
	if len(*dialed) != 5 {
		t.Fatalf("dialed %v, want the 5 accepted tunnels", *dialed)
	}
	for _, d := range *dialed {
		if d != publicIP+":443" {
			t.Fatalf("dialed %v", *dialed)
		}
	}
}

// A request to an allowlisted host written with the root's trailing dot is
// the same host: the allowlist check before the request and the dial agree.
func TestDialAcceptsTheRootDot(t *testing.T) {
	srv := okServer(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })
	dns := &fakeDNS{answers: map[string][]string{"example.com": {publicIP}}}
	c, dialed := newTestClient(t, Config{AllowedHosts: []string{"example.com"}}, dns, srv)
	resp, err := get(t, c, "https://example.com./")
	if err != nil {
		t.Fatalf("root dot: %v", err)
	}
	resp.Body.Close()
	if len(*dialed) != 1 || !strings.HasPrefix((*dialed)[0], publicIP+":") {
		t.Fatalf("dialed %v", *dialed)
	}
	// And the dial itself, as the transport calls it.
	if _, err := c.dial(t.Context(), "tcp", "EXAMPLE.com.:443"); err != nil {
		t.Fatalf("dial with root dot: %v", err)
	}
	if _, err := c.dial(t.Context(), "tcp", "evil.com.:443"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("dial of a host not allowed: %v", err)
	}
}

// RFC 8215 local-use NAT64 (64:ff9b:1::/48) embeds a v4 address like the
// well-known prefix and is refused the same way.
func TestBlockedAddrLocalUseNAT64(t *testing.T) {
	for _, s := range []string{"64:ff9b:1::a00:1", "64:ff9b:1:ffff::7f00:1", "64:ff9b:1::"} {
		if _, ok := blockedAddr(netip.MustParseAddr(s)); !ok {
			t.Errorf("%s should be blocked", s)
		}
	}
	// Just outside it is ordinary global space.
	if reason, ok := blockedAddr(netip.MustParseAddr("64:ff9b:2::1")); ok {
		t.Errorf("64:ff9b:2::1 blocked as %q", reason)
	}
}
