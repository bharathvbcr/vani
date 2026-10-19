package egress

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newLoopbackT(t *testing.T, maxBody int64) *Loopback {
	t.Helper()
	l, err := NewLoopback(2*time.Second, maxBody)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func lget(t *testing.T, l *Loopback, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return l.Do(context.Background(), req)
}

func TestLoopbackReachesALocalServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer srv.Close()
	resp, err := lget(t, newLoopbackT(t, 0), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || string(b) != "ok" {
		t.Fatalf("body %q err %v", b, err)
	}
}

func TestLoopbackRefusesEveryOtherHost(t *testing.T) {
	l := newLoopbackT(t, 0)
	for _, u := range []string{
		"http://example.com/v1/systemone",
		"https://openrouter.ai/api/alpha/decisions",
		"http://10.0.0.1:8080/",
		"http://192.168.1.10/",
		"http://127.0.0.1.example.com/", // a name that only looks like loopback
		"http://user:pw@127.0.0.1:1/",
		"ftp://127.0.0.1/",
	} {
		if _, err := lget(t, l, u); !errors.Is(err, ErrNotLoopback) {
			t.Errorf("%s: err %v, want ErrNotLoopback", u, err)
		}
	}
}

// The dialer check is the authoritative one: even a request that got past the
// URL check cannot open a socket to a non-loopback address.
func TestLoopbackDialerRefusesNonLoopbackAddress(t *testing.T) {
	l := newLoopbackT(t, 0)
	tr := l.hc.Transport.(*http.Transport)
	_, err := tr.DialContext(context.Background(), "tcp", "192.0.2.1:80") // TEST-NET-1, never routed
	if err == nil || !strings.Contains(err.Error(), ErrNotLoopback.Error()) {
		t.Fatalf("dial to 192.0.2.1: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := tr.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial to loopback: %v", err)
	}
	_ = c.Close()
}

func TestLoopbackFollowsNoRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/", http.StatusFound)
	}))
	defer srv.Close()
	resp, err := lget(t, newLoopbackT(t, 0), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status %d: a redirect was followed", resp.StatusCode)
	}
}

func TestLoopbackCapsTheBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 100))
	}))
	defer srv.Close()
	resp, err := lget(t, newLoopbackT(t, 10), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("read err %v, want ErrBodyTooLarge", err)
	}
}

func TestLoopbackBounds(t *testing.T) {
	for _, c := range []struct {
		d time.Duration
		n int64
	}{{-1, 0}, {3 * time.Minute, 0}, {0, -1}, {0, maxLoopbackBody + 1}} {
		if _, err := NewLoopback(c.d, c.n); err == nil {
			t.Errorf("NewLoopback(%v, %d) accepted", c.d, c.n)
		}
	}
	for _, h := range []string{"localhost", "LOCALHOST", "127.0.0.1", "127.8.9.10", "::1", "[::1]"} {
		if !IsLoopbackHost(h) {
			t.Errorf("%q not loopback", h)
		}
	}
	for _, h := range []string{"", "localhost.example.com", "10.0.0.1", "0.0.0.0", "::"} {
		if IsLoopbackHost(h) {
			t.Errorf("%q counted as loopback", h)
		}
	}
}
