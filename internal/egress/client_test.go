package egress

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const publicIP = "93.184.216.34"

// fakeDNS maps names to answers; unknown names fail to resolve.
type fakeDNS struct {
	mu      sync.Mutex
	answers map[string][]string
	calls   map[string]int
	// after, when set, replaces the answer from the nth call on (rebinding).
	rebindAfter int
	rebindTo    []string
}

func (f *fakeDNS) resolve(_ context.Context, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[host]++
	list, ok := f.answers[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	if f.rebindAfter > 0 && f.calls[host] > f.rebindAfter {
		list = f.rebindTo
	}
	var out []netip.Addr
	for _, s := range list {
		out = append(out, netip.MustParseAddr(s))
	}
	return out, nil
}

// newTestClient builds a client whose DNS is fake and whose sockets all go to
// srv (when non-nil) via a test-only dialer. The production blocked-address
// rule is left untouched: the fake DNS hands out public addresses.
func newTestClient(t *testing.T, cfg Config, dns *fakeDNS, srv *httptest.Server) (*Client, *[]string) {
	t.Helper()
	if cfg.PerHostInterval == 0 {
		cfg.PerHostInterval = time.Nanosecond
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.resolve = dns.resolve
	dialed := &[]string{}
	var mu sync.Mutex
	c.dialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		*dialed = append(*dialed, addr)
		mu.Unlock()
		if srv == nil {
			return nil, errors.New("test: no server, dial must not happen")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}
	if srv != nil {
		pool := x509.NewCertPool()
		pool.AddCert(srv.Certificate())
		c.transp.TLSClientConfig.RootCAs = pool
	}
	return c, dialed
}

func get(t *testing.T, c *Client, rawURL string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c.Do(context.Background(), req)
}

func TestNewRejectsBadConfig(t *testing.T) {
	bad := map[string]Config{
		"empty allowlist":    {},
		"blank entry":        {AllowedHosts: []string{"  "}},
		"port":               {AllowedHosts: []string{"example.com:443"}},
		"scheme":             {AllowedHosts: []string{"https://example.com"}},
		"path":               {AllowedHosts: []string{"example.com/x"}},
		"ipv4 literal":       {AllowedHosts: []string{"127.0.0.1"}},
		"ipv6 literal":       {AllowedHosts: []string{"::1"}},
		"bracketed ipv6":     {AllowedHosts: []string{"[::1]"}},
		"numeric tld":        {AllowedHosts: []string{"127.1"}},
		"bare star":          {AllowedHosts: []string{"*"}},
		"single label wild":  {AllowedHosts: []string{"*.com"}},
		"mid star":           {AllowedHosts: []string{"a.*.example.com"}},
		"partial star":       {AllowedHosts: []string{"*example.com"}},
		"double dot":         {AllowedHosts: []string{"a..example.com"}},
		"underscore":         {AllowedHosts: []string{"a_b.example.com"}},
		"non-ascii":          {AllowedHosts: []string{"bücher.example"}},
		"userinfo":           {AllowedHosts: []string{"u@example.com"}},
		"negative timeout":   {AllowedHosts: []string{"example.com"}, Timeout: -1},
		"huge timeout":       {AllowedHosts: []string{"example.com"}, Timeout: time.Hour},
		"negative body":      {AllowedHosts: []string{"example.com"}, MaxBodyBytes: -1},
		"huge body":          {AllowedHosts: []string{"example.com"}, MaxBodyBytes: 1 << 30},
		"negative redirects": {AllowedHosts: []string{"example.com"}, MaxRedirects: -1},
		"huge redirects":     {AllowedHosts: []string{"example.com"}, MaxRedirects: 11},
		"negative interval":  {AllowedHosts: []string{"example.com"}, PerHostInterval: -1},
		"huge interval":      {AllowedHosts: []string{"example.com"}, PerHostInterval: time.Hour},
		"user agent newline": {AllowedHosts: []string{"example.com"}, UserAgent: "x\r\nX-Evil: 1"},
		"one bad among good": {AllowedHosts: []string{"example.com", "10.0.0.1"}},
	}
	for name, cfg := range bad {
		if c, err := New(cfg); err == nil || c != nil {
			t.Errorf("%s: New accepted %+v", name, cfg)
		}
	}
}

func TestNewDefaultsAndNormalisation(t *testing.T) {
	c, err := New(Config{AllowedHosts: []string{"  Boards.Example.COM. ", "*.Lever.Co"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.cfg.Timeout != 30*time.Second || c.cfg.MaxBodyBytes != 8<<20 || c.cfg.MaxRedirects != 5 ||
		c.cfg.PerHostInterval != time.Second {
		t.Errorf("defaults wrong: %+v", c.cfg)
	}
	if c.cfg.UserAgent != "Vani/0.1 (+personal job-listing reader)" {
		t.Errorf("user agent = %q", c.cfg.UserAgent)
	}
	if !c.allows("boards.example.com") || !c.allows("a.lever.co") {
		t.Error("normalised entries should match")
	}
	if c.transp.Proxy != nil {
		t.Error("transport must have no proxy")
	}
	if c.http.Jar != nil {
		t.Error("client must have no cookie jar")
	}
}

func TestAllowlistMatching(t *testing.T) {
	c, err := New(Config{AllowedHosts: []string{"example.com", "*.greenhouse.io"}})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"example.com":            true,
		"www.example.com":        false, // exact entry does not admit subdomains
		"notexample.com":         false,
		"example.com.evil.com":   false,
		"greenhouse.io":          false, // wildcard excludes the bare suffix
		"boards.greenhouse.io":   true,
		"a.b.greenhouse.io":      true,
		"xgreenhouse.io":         false,
		".greenhouse.io":         false,
		"evilgreenhouse.io":      false,
		"greenhouse.io.evil.com": false,
		"":                       false,
		"boards.greenhouse.io.":  false, // callers normalise via hostKey first
	}
	for host, want := range cases {
		if got := c.allows(host); got != want {
			t.Errorf("allows(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestDoRefusals(t *testing.T) {
	dns := &fakeDNS{answers: map[string][]string{"example.com": {publicIP}}}
	c, dialed := newTestClient(t, Config{AllowedHosts: []string{"example.com"}}, dns, nil)

	cases := []struct {
		name, url string
		want      error
	}{
		{"http scheme", "http://example.com/", ErrScheme},
		{"ftp scheme", "ftp://example.com/", ErrScheme},
		{"no scheme", "//example.com/", ErrScheme},
		{"unlisted host", "https://evil.com/", ErrNotAllowed},
		{"subdomain of exact entry", "https://www.example.com/", ErrNotAllowed},
		{"ip literal", "https://93.184.216.34/", ErrNotAllowed},
		{"loopback literal", "https://127.0.0.1/", ErrNotAllowed},
	}
	for _, tc := range cases {
		resp, err := get(t, c, tc.url)
		if resp != nil {
			resp.Body.Close()
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	for _, u := range []string{"https://example.com:8443/", "https://user:pw@example.com/", "https://example.com@evil.com/"} {
		if _, err := get(t, c, u); err == nil {
			t.Errorf("%s should be refused", u)
		}
	}
	if len(*dialed) != 0 {
		t.Errorf("refused requests dialed: %v", *dialed)
	}
	if _, err := c.Do(context.Background(), nil); err == nil {
		t.Error("nil request must error")
	}
}

func TestBlockedAddr(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.255.255.254", "::1",
		"10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"169.254.169.254", "169.254.0.1", "fe80::1",
		"100.64.0.1", "100.127.255.255",
		"fc00::1", "fd12:3456::1",
		"224.0.0.1", "239.255.255.255", "ff02::1", "ff0e::1",
		"0.0.0.0", "0.1.2.3", "::",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254", "::ffff:100.64.0.1", "::ffff:0.0.0.0",
		"fe80::1%eth0",
		"255.255.255.255", "240.0.0.1", "198.18.0.1", "192.0.2.1",
		"64:ff9b::7f00:1", "2002:7f00:1::1", "2001:db8::1",
	}
	for _, s := range blocked {
		a, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		if reason, ok := blockedAddr(a); !ok || reason == "" {
			t.Errorf("%s should be blocked", s)
		}
	}
	if _, ok := blockedAddr(netip.Addr{}); !ok {
		t.Error("invalid address should be blocked")
	}
	for _, s := range []string{"93.184.216.34", "8.8.8.8", "1.1.1.1", "172.32.0.1", "100.128.0.1", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if reason, ok := blockedAddr(netip.MustParseAddr(s)); ok {
			t.Errorf("%s should be allowed, blocked as %q", s, reason)
		}
	}
}

func TestBlockedRangesRefusedViaDNS(t *testing.T) {
	for _, bad := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "100.64.0.9", "fd00::1", "::ffff:192.168.0.1", "0.0.0.0", "224.0.0.251"} {
		dns := &fakeDNS{answers: map[string][]string{"example.com": {bad}}}
		c, dialed := newTestClient(t, Config{AllowedHosts: []string{"example.com"}}, dns, nil)
		_, err := get(t, c, "https://example.com/")
		if !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%s: err = %v, want ErrBlockedAddress", bad, err)
		}
		if len(*dialed) != 0 {
			t.Errorf("%s: dialed %v", bad, *dialed)
		}
	}
}

func TestMixedDNSAnswerRefused(t *testing.T) {
	for _, order := range [][]string{{publicIP, "10.0.0.5"}, {"10.0.0.5", publicIP}} {
		dns := &fakeDNS{answers: map[string][]string{"example.com": order}}
		c, dialed := newTestClient(t, Config{AllowedHosts: []string{"example.com"}}, dns, nil)
		if _, err := get(t, c, "https://example.com/"); !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%v: err = %v, want ErrBlockedAddress", order, err)
		}
		if len(*dialed) != 0 {
			t.Errorf("%v: dialed %v", order, *dialed)
		}
	}
}

func TestEmptyAndFailedDNS(t *testing.T) {
	dns := &fakeDNS{answers: map[string][]string{"example.com": {}}}
	c, _ := newTestClient(t, Config{AllowedHosts: []string{"example.com", "gone.example"}}, dns, nil)
	if _, err := get(t, c, "https://example.com/"); err == nil {
		t.Error("empty answer must be refused")
	}
	if _, err := get(t, c, "https://gone.example/"); err == nil {
		t.Error("resolver failure must be refused")
	}
}

func TestDNSRebindBetweenCheckAndDial(t *testing.T) {
	dns := &fakeDNS{
		answers:     map[string][]string{"example.com": {publicIP}},
		rebindAfter: 1,
		rebindTo:    []string{"169.254.169.254"},
	}
	c, dialed := newTestClient(t, Config{AllowedHosts: []string{"example.com"}}, dns, nil)
	_, err := get(t, c, "https://example.com/")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want ErrBlockedAddress", err)
	}
	if len(*dialed) != 0 {
		t.Errorf("rebound address was dialed: %v", *dialed)
	}
}

func okServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestRoundTripPinnedDialUserAgentAndNoCookies(t *testing.T) {
	var seenUA, seenCookie, seenHost string
	var n atomic.Int32
	srv := okServer(t, func(w http.ResponseWriter, r *http.Request) {
		seenUA, seenCookie, seenHost = r.Header.Get("User-Agent"), r.Header.Get("Cookie"), r.Host
		n.Add(1)
		http.SetCookie(w, &http.Cookie{Name: "s", Value: "1"})
		io.WriteString(w, "hello")
	})
	dns := &fakeDNS{answers: map[string][]string{"example.com": {publicIP}}}
	c, dialed := newTestClient(t, Config{AllowedHosts: []string{"example.com"}}, dns, srv)

	for i := 0; i < 2; i++ {
		resp, err := get(t, c, "https://example.com/x")
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || string(b) != "hello" {
			t.Fatalf("body = %q, err = %v", b, err)
		}
	}
	if seenUA != "Vani/0.1 (+personal job-listing reader)" {
		t.Errorf("UA = %q", seenUA)
	}
	if seenCookie != "" {
		t.Errorf("cookie leaked back: %q", seenCookie)
	}
	if seenHost != "example.com" {
		t.Errorf("Host = %q", seenHost)
	}
	if len(*dialed) == 0 || (*dialed)[0] != publicIP+":443" {
		t.Errorf("dialed %v, want the vetted literal IP on 443", *dialed)
	}
	if n.Load() != 2 {
		t.Errorf("server hits = %d", n.Load())
	}
}

func TestCallerUserAgentAndHostOverride(t *testing.T) {
	var seenUA, seenHost string
	srv := okServer(t, func(w http.ResponseWriter, r *http.Request) {
		seenUA, seenHost = r.Header.Get("User-Agent"), r.Host
	})
	dns := &fakeDNS{answers: map[string][]string{"example.com": {publicIP}}}
	c, _ := newTestClient(t, Config{AllowedHosts: []string{"example.com"}}, dns, srv)
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	req.Header.Set("User-Agent", "custom/1")
	req.Host = "evil.internal"
	resp, err := c.Do(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if seenUA != "custom/1" {
		t.Errorf("caller UA overwritten: %q", seenUA)
	}
	if seenHost != "example.com" {
		t.Errorf("Host override reached the server: %q", seenHost)
	}
	if req.Host != "evil.internal" {
		t.Error("caller's request was mutated")
	}
}

func TestTLSHostnameStillVerified(t *testing.T) {
	// The httptest certificate is for example.com. A different allowed name
	// pinned to the same socket must fail verification: the pinned IP does not
	// replace the hostname check.
	srv := okServer(t, func(w http.ResponseWriter, r *http.Request) {})
	dns := &fakeDNS{answers: map[string][]string{"other.example.net": {publicIP}}}
	c, _ := newTestClient(t, Config{AllowedHosts: []string{"other.example.net"}}, dns, srv)
	resp, err := get(t, c, "https://other.example.net/")
	if err == nil {
		resp.Body.Close()
		t.Fatal("certificate for another name was accepted")
	}
	var uerr interface{ Unwrap() error }
	if !errors.As(err, &uerr) || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("err = %v, want a certificate verification failure", err)
	}
}

func TestRedirects(t *testing.T) {
	srv := okServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			io.WriteString(w, "done")
		case "/to-ok":
			http.Redirect(w, r, "https://example.com/ok", http.StatusFound)
		case "/to-evil":
			http.Redirect(w, r, "https://evil.com/x", http.StatusFound)
		case "/to-internal":
			http.Redirect(w, r, "https://internal.example.com/x", http.StatusFound)
		case "/to-http":
			http.Redirect(w, r, "http://example.com/ok", http.StatusFound)
		case "/to-port":
			http.Redirect(w, r, "https://example.com:8443/ok", http.StatusFound)
		case "/to-mixed":
			http.Redirect(w, r, "https://mixed.example.com/x", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "https://example.com/loop", http.StatusFound)
		}
	})
	dns := &fakeDNS{answers: map[string][]string{
		"example.com":          {publicIP},
		"internal.example.com": {"10.0.0.7"},
		"mixed.example.com":    {publicIP, "127.0.0.1"},
	}}
	c, _ := newTestClient(t, Config{
		AllowedHosts: []string{"example.com", "*.example.com"},
		MaxRedirects: 3,
	}, dns, srv)

	body := func(path string) (string, error) {
		resp, err := get(t, c, "https://example.com"+path)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b), nil
	}

	if got, err := body("/to-ok"); err != nil || got != "done" {
		t.Errorf("allowed redirect: %q, %v", got, err)
	}
	for path, want := range map[string]error{
		"/to-evil":     ErrNotAllowed,
		"/to-internal": ErrBlockedAddress,
		"/to-mixed":    ErrBlockedAddress,
		"/to-http":     ErrScheme,
		"/loop":        ErrTooManyRedirects,
	} {
		if _, err := body(path); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", path, err, want)
		}
	}
	if _, err := body("/to-port"); err == nil {
		t.Error("redirect to a non-443 port must be refused")
	}
}

func TestRedirectCapCountsHops(t *testing.T) {
	var hits atomic.Int32
	srv := okServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "https://example.com/again", http.StatusFound)
	})
	dns := &fakeDNS{answers: map[string][]string{"example.com": {publicIP}}}
	c, _ := newTestClient(t, Config{AllowedHosts: []string{"example.com"}, MaxRedirects: 2}, dns, srv)
	if _, err := get(t, c, "https://example.com/"); !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("err = %v", err)
	}
	if hits.Load() != 3 { // the original plus exactly two followed redirects
		t.Errorf("server hits = %d, want 3", hits.Load())
	}
}

func TestBodyCap(t *testing.T) {
	srv := okServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := 0
		fmt.Sscanf(r.URL.Query().Get("n"), "%d", &n)
		w.Write([]byte(strings.Repeat("a", n)))
	})
	dns := &fakeDNS{answers: map[string][]string{"example.com": {publicIP}}}
	c, _ := newTestClient(t, Config{AllowedHosts: []string{"example.com"}, MaxBodyBytes: 100}, dns, srv)

	read := func(n int) (int, error) {
		resp, err := get(t, c, fmt.Sprintf("https://example.com/?n=%d", n))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return len(b), err
	}
	if n, err := read(100); err != nil || n != 100 {
		t.Errorf("exact cap: %d, %v", n, err)
	}
	if n, err := read(0); err != nil || n != 0 {
		t.Errorf("empty: %d, %v", n, err)
	}
	if n, err := read(101); !errors.Is(err, ErrBodyTooLarge) || n > 100 {
		t.Errorf("cap+1: %d, %v", n, err)
	}
	if n, err := read(100000); !errors.Is(err, ErrBodyTooLarge) || n > 100 {
		t.Errorf("huge: %d, %v", n, err)
	}
}

func TestLimitedBodyStaysFailed(t *testing.T) {
	b := &limitedBody{rc: io.NopCloser(strings.NewReader("abcdef")), remaining: 3}
	buf := make([]byte, 10)
	n, err := b.Read(buf)
	if n != 3 || err != nil {
		t.Fatalf("first read = %d, %v", n, err)
	}
	for i := 0; i < 3; i++ {
		if n, err := b.Read(buf); n != 0 || !errors.Is(err, ErrBodyTooLarge) {
			t.Fatalf("read %d after cap = %d, %v", i, n, err)
		}
	}
}

func TestPerHostIntervalHonoursContext(t *testing.T) {
	srv := okServer(t, func(w http.ResponseWriter, r *http.Request) {})
	dns := &fakeDNS{answers: map[string][]string{"example.com": {publicIP}, "www.example.com": {publicIP}}}
	c, _ := newTestClient(t, Config{
		AllowedHosts:    []string{"example.com", "www.example.com"},
		PerHostInterval: 4 * time.Minute,
	}, dns, srv)

	// First request to each host is immediate.
	for _, u := range []string{"https://example.com/", "https://www.example.com/"} {
		resp, err := get(t, c, u)
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		resp.Body.Close()
	}

	// A second request to the same host waits and must give up with the context.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	start := time.Now()
	_, err := c.Do(ctx, req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("wait ignored the context: %v", time.Since(start))
	}

	// Explicit cancel mid-wait.
	ctx2, cancel2 := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		r, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
		_, err := c.Do(ctx2, r)
		done <- err
	}()
	time.Sleep(30 * time.Millisecond)
	cancel2()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Do did not return after cancel")
	}

	// A cancelled wait does not consume the slot: with a short interval the
	// host is reusable straight away.
	c.mu.Lock()
	c.cfg.PerHostInterval = time.Nanosecond
	delete(c.next, "example.com")
	c.mu.Unlock()
	resp, err := get(t, c, "https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestPerHostIntervalSpacesRequests(t *testing.T) {
	srv := okServer(t, func(w http.ResponseWriter, r *http.Request) {})
	dns := &fakeDNS{answers: map[string][]string{"example.com": {publicIP}}}
	const gap = 80 * time.Millisecond
	c, _ := newTestClient(t, Config{AllowedHosts: []string{"example.com"}, PerHostInterval: gap}, dns, srv)
	start := time.Now()
	for i := 0; i < 3; i++ {
		resp, err := get(t, c, "https://example.com/")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if el := time.Since(start); el < 2*gap-10*time.Millisecond {
		t.Errorf("3 requests took %v, want at least %v", el, 2*gap)
	}
}

func TestAlreadyCancelledContext(t *testing.T) {
	dns := &fakeDNS{answers: map[string][]string{"example.com": {publicIP}}}
	c, dialed := newTestClient(t, Config{AllowedHosts: []string{"example.com"}}, dns, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	if _, err := c.Do(ctx, req); err == nil {
		t.Error("cancelled context must fail")
	}
	if len(*dialed) != 0 {
		t.Errorf("dialed with a cancelled context: %v", *dialed)
	}
}
