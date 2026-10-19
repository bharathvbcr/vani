package egress

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// echoServer accepts connections and echoes every byte back until EOF.
func echoServer(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln
}

// startProxy builds a proxy over a Client whose DNS is fake and whose sockets
// all go to the echo server. Production blocked-address rules are untouched.
func startProxy(t *testing.T, cfg ProxyConfig, answers map[string][]string) (*Proxy, string, string, *[]string) {
	t.Helper()
	ln := echoServer(t)
	dns := &fakeDNS{answers: answers}
	c, dialed := newTestClient(t, Config{AllowedHosts: []string{"jobs.example.com"}}, dns, nil)
	var mu sync.Mutex
	c.dialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		*dialed = append(*dialed, addr)
		mu.Unlock()
		return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	}
	p, err := NewProxy(c, cfg)
	if err != nil {
		t.Fatal(err)
	}
	addr, token, err := p.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p, addr, token, dialed
}

func defaultAnswers() map[string][]string {
	return map[string][]string{"jobs.example.com": {publicIP}}
}

// rawRequest sends one request line plus headers and returns the status code
// and, for a 200, the open connection and its reader.
func rawRequest(t *testing.T, addr, method, target string, hdr map[string]string) (int, net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	var b strings.Builder
	b.WriteString(method + " " + target + " HTTP/1.1\r\nHost: jobs.example.com\r\n")
	for k, v := range hdr {
		b.WriteString(k + ": " + v + "\r\n")
	}
	b.WriteString("\r\n")
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, b.String()); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: method})
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, conn, br
}

func auth(token string) map[string]string {
	return map[string]string{"Proxy-Authorization": "Bearer " + token}
}

func TestProxyAuth(t *testing.T) {
	_, addr, token, dialed := startProxy(t, ProxyConfig{}, defaultAnswers())
	for name, hdr := range map[string]map[string]string{
		"missing":      nil,
		"wrong":        auth("nope"),
		"empty":        {"Proxy-Authorization": "Bearer "},
		"basic":        {"Proxy-Authorization": "Basic " + token},
		"bare token":   {"Proxy-Authorization": token},
		"prefix":       auth(token[:len(token)-1]),
		"token+suffix": auth(token + "x"),
	} {
		if code, _, _ := rawRequest(t, addr, "CONNECT", "jobs.example.com:443", hdr); code != http.StatusProxyAuthRequired {
			t.Errorf("%s: status %d, want 407", name, code)
		}
	}
	if len(*dialed) != 0 {
		t.Fatalf("unauthenticated requests dialed upstream: %v", *dialed)
	}
	// 407 precedes the method check so an unauthenticated caller learns nothing.
	if code, _, _ := rawRequest(t, addr, "GET", "http://jobs.example.com/", nil); code != http.StatusProxyAuthRequired {
		t.Errorf("unauthenticated GET: status %d, want 407", code)
	}
}

// Lightpanda sends its proxy credential on CONNECT only as Basic credentials
// taken from the proxy URL, so the token is accepted as a Basic password.
func TestProxyAcceptsTokenAsBasicPassword(t *testing.T) {
	_, addr, token, _ := startProxy(t, ProxyConfig{}, defaultAnswers())
	basic := func(user, pass string) map[string]string {
		return map[string]string{"Proxy-Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))}
	}
	if code, _, _ := rawRequest(t, addr, "CONNECT", "jobs.example.com:443", basic("vani", token)); code != 200 {
		t.Errorf("Basic with the token: status %d, want 200", code)
	}
	for name, hdr := range map[string]map[string]string{
		"wrong password": basic("vani", "nope"),
		"token as user":  basic(token, ""),
		"no colon":       {"Proxy-Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(token))},
		"bad base64":     {"Proxy-Authorization": "Basic !!!"},
	} {
		if code, _, _ := rawRequest(t, addr, "CONNECT", "jobs.example.com:443", hdr); code != http.StatusProxyAuthRequired {
			t.Errorf("%s: status %d, want 407", name, code)
		}
	}
}

func TestProxyTokenIsFreshPerStart(t *testing.T) {
	_, _, t1, _ := startProxy(t, ProxyConfig{}, defaultAnswers())
	_, _, t2, _ := startProxy(t, ProxyConfig{}, defaultAnswers())
	if len(t1) != 64 || t1 == t2 {
		t.Fatalf("tokens %q %q: want distinct 32-byte hex", t1, t2)
	}
}

func TestProxyRefusals(t *testing.T) {
	_, addr, token, dialed := startProxy(t, ProxyConfig{}, map[string][]string{
		"jobs.example.com": {publicIP},
		"evil.example.com": {publicIP},
	})
	// Allowlisted name that resolves to a private address.
	_, addr2, token2, dialed2 := startProxy(t, ProxyConfig{}, map[string][]string{"jobs.example.com": {"10.0.0.5"}})

	cases := []struct {
		name         string
		addr, token  string
		method, tgt  string
		want         int
		dialedAnyway *[]string
	}{
		{"GET", addr, token, "GET", "http://jobs.example.com/", http.StatusMethodNotAllowed, dialed},
		{"POST", addr, token, "POST", "/", http.StatusMethodNotAllowed, dialed},
		{"port 80", addr, token, "CONNECT", "jobs.example.com:80", http.StatusForbidden, dialed},
		{"port 8443", addr, token, "CONNECT", "jobs.example.com:8443", http.StatusForbidden, dialed},
		{"no port", addr, token, "CONNECT", "jobs.example.com", http.StatusBadRequest, dialed},
		{"not allowlisted", addr, token, "CONNECT", "evil.example.com:443", http.StatusForbidden, dialed},
		{"ip literal", addr, token, "CONNECT", "93.184.216.34:443", http.StatusForbidden, dialed},
		{"loopback literal", addr, token, "CONNECT", "127.0.0.1:443", http.StatusForbidden, dialed},
		{"private resolution", addr2, token2, "CONNECT", "jobs.example.com:443", http.StatusForbidden, dialed2},
	}
	for _, tc := range cases {
		code, _, _ := rawRequest(t, tc.addr, tc.method, tc.tgt, auth(tc.token))
		if code != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, code, tc.want)
		}
	}
	if len(*dialed) != 0 || len(*dialed2) != 0 {
		t.Fatalf("a refused request reached the dialer: %v %v", *dialed, *dialed2)
	}
}

func TestProxyTunnelsBytesBothWays(t *testing.T) {
	_, addr, token, dialed := startProxy(t, ProxyConfig{}, defaultAnswers())
	code, conn, br := rawRequest(t, addr, "CONNECT", "JOBS.example.com.:443", auth(token))
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200", code)
	}
	for _, msg := range []string{"hello", strings.Repeat("x", 100<<10)} {
		go io.WriteString(conn, msg)
		got := make([]byte, len(msg))
		if _, err := io.ReadFull(br, got); err != nil {
			t.Fatal(err)
		}
		if string(got) != msg {
			t.Fatalf("echo mismatch for %d bytes", len(msg))
		}
	}
	if len(*dialed) != 1 || !strings.HasPrefix((*dialed)[0], publicIP+":443") {
		t.Fatalf("dialed %v, want the vetted literal %s:443", *dialed, publicIP)
	}
}

func TestProxyConcurrencyCap(t *testing.T) {
	_, addr, token, _ := startProxy(t, ProxyConfig{MaxTunnels: 2}, defaultAnswers())
	for i := 0; i < 2; i++ {
		if code, _, _ := rawRequest(t, addr, "CONNECT", "jobs.example.com:443", auth(token)); code != 200 {
			t.Fatalf("tunnel %d: status %d", i, code)
		}
	}
	if code, _, _ := rawRequest(t, addr, "CONNECT", "jobs.example.com:443", auth(token)); code != http.StatusServiceUnavailable {
		t.Fatalf("third tunnel: status %d, want 503", code)
	}
}

func TestProxyConcurrencySlotIsReleased(t *testing.T) {
	_, addr, token, _ := startProxy(t, ProxyConfig{MaxTunnels: 1}, defaultAnswers())
	code, conn, _ := rawRequest(t, addr, "CONNECT", "jobs.example.com:443", auth(token))
	if code != 200 {
		t.Fatal(code)
	}
	conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		code, _, _ := rawRequest(t, addr, "CONNECT", "jobs.example.com:443", auth(token))
		if code == 200 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot never freed, last status %d", code)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProxyByteCap(t *testing.T) {
	_, addr, token, _ := startProxy(t, ProxyConfig{MaxBytes: 1024}, defaultAnswers())
	code, conn, br := rawRequest(t, addr, "CONNECT", "jobs.example.com:443", auth(token))
	if code != 200 {
		t.Fatal(code)
	}
	// Exactly at the cap passes.
	msg := strings.Repeat("a", 1024)
	if _, err := io.WriteString(conn, msg); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("bytes at the cap were not echoed: %v", err)
	}
	// One more byte trips the cap: the tunnel is cut, not silently truncated.
	_, _ = io.WriteString(conn, "b")
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, err := br.Read(make([]byte, 16)); err == nil || n != 0 {
		t.Fatalf("read after cap: n=%d err=%v, want the tunnel closed", n, err)
	}
}

func TestProxyIdleTimeout(t *testing.T) {
	_, addr, token, _ := startProxy(t, ProxyConfig{IdleTimeout: 80 * time.Millisecond}, defaultAnswers())
	code, conn, br := rawRequest(t, addr, "CONNECT", "jobs.example.com:443", auth(token))
	if code != 200 {
		t.Fatal(code)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	start := time.Now()
	if _, err := br.Read(make([]byte, 1)); err == nil {
		t.Fatal("idle tunnel produced data")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("idle tunnel closed after %s, want about 80ms", time.Since(start))
	}
}

func TestProxyLifetime(t *testing.T) {
	_, addr, token, _ := startProxy(t, ProxyConfig{MaxLifetime: 100 * time.Millisecond, IdleTimeout: time.Minute}, defaultAnswers())
	code, conn, br := rawRequest(t, addr, "CONNECT", "jobs.example.com:443", auth(token))
	if code != 200 {
		t.Fatal(code)
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() { // keep the tunnel busy so only the lifetime can end it
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				if _, err := io.WriteString(conn, "."); err != nil {
					return
				}
			}
		}
	}()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	start := time.Now()
	for {
		if _, err := br.Read(make([]byte, 64)); err != nil {
			break
		}
		if time.Since(start) > 2*time.Second {
			t.Fatal("busy tunnel outlived MaxLifetime")
		}
	}
}

func TestProxyCloseStopsListenerAndTunnels(t *testing.T) {
	p, addr, token, _ := startProxy(t, ProxyConfig{}, defaultAnswers())
	code, conn, br := rawRequest(t, addr, "CONNECT", "jobs.example.com:443", auth(token))
	if code != 200 {
		t.Fatal(code)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := br.Read(make([]byte, 1)); err == nil {
		t.Fatal("open tunnel survived Close")
	}
	if c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("listener still accepting after Close")
	}
	if _, _, err := p.Start(context.Background()); err == nil {
		t.Fatal("Start after Close succeeded")
	}
}

func TestProxyContextCancel(t *testing.T) {
	c, _ := newTestClient(t, Config{AllowedHosts: []string{"jobs.example.com"}}, &fakeDNS{answers: defaultAnswers()}, nil)
	p, err := NewProxy(c, ProxyConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	addr, _, err := p.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("listener still accepting after ctx cancel")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProxyListensOnLoopbackOnly(t *testing.T) {
	_, addr, _, _ := startProxy(t, ProxyConfig{}, defaultAnswers())
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatalf("addr %q, want 127.0.0.1:port", addr)
	}
}

func TestProxyStartCancelledContext(t *testing.T) {
	c, _ := newTestClient(t, Config{AllowedHosts: []string{"jobs.example.com"}}, &fakeDNS{}, nil)
	p, _ := NewProxy(c, ProxyConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := p.Start(ctx); err == nil {
		t.Fatal("Start with a cancelled context succeeded")
	}
}

func TestNewProxyValidation(t *testing.T) {
	if _, err := NewProxy(nil, ProxyConfig{}); err == nil {
		t.Error("nil client accepted")
	}
	c, _ := newTestClient(t, Config{AllowedHosts: []string{"jobs.example.com"}}, &fakeDNS{}, nil)
	for name, cfg := range map[string]ProxyConfig{
		"tunnels<0":  {MaxTunnels: -1},
		"tunnels>":   {MaxTunnels: maxProxyTunnels + 1},
		"idle<0":     {IdleTimeout: -1},
		"idle>":      {IdleTimeout: maxProxyIdle + 1},
		"bytes<0":    {MaxBytes: -1},
		"bytes>":     {MaxBytes: maxProxyBytes + 1},
		"lifetime<0": {MaxLifetime: -1},
		"lifetime>":  {MaxLifetime: maxProxyLifetime + 1},
	} {
		if _, err := NewProxy(c, cfg); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	p, err := NewProxy(c, ProxyConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if p.cfg.MaxTunnels != 16 || p.cfg.IdleTimeout != time.Minute || p.cfg.MaxBytes != 64<<20 || p.cfg.MaxLifetime != 5*time.Minute {
		t.Errorf("defaults wrong: %+v", p.cfg)
	}
}
