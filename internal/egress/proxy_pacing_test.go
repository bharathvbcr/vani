package egress

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"
)

// pacedProxy is a started proxy whose Client paces at interval and whose
// sockets all go to an echo server, recording when each upstream dial began.
type pacedProxy struct {
	p     *Proxy
	addr  string
	token string

	mu    sync.Mutex
	dials []time.Time
}

func startPacedProxy(t *testing.T, interval time.Duration, cfg ProxyConfig) *pacedProxy {
	t.Helper()
	ln := echoServer(t)
	dns := &fakeDNS{answers: map[string][]string{
		"jobs.example.com":  {publicIP},
		"other.example.com": {publicIP},
		"evil.example.com":  {publicIP},
	}}
	c, _ := newTestClient(t, Config{
		AllowedHosts:    []string{"jobs.example.com", "other.example.com"},
		PerHostInterval: interval,
	}, dns, nil)
	pp := &pacedProxy{}
	c.dialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
		pp.mu.Lock()
		pp.dials = append(pp.dials, time.Now())
		pp.mu.Unlock()
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
	pp.p, pp.addr, pp.token = p, addr, token
	return pp
}

func (pp *pacedProxy) dialTimes() []time.Time {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	return slices.Clone(pp.dials)
}

func (pp *pacedProxy) next(host string) (time.Time, bool) {
	c := pp.p.c
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.next[host]
	return v, ok
}

// connect sends one authenticated CONNECT and returns its status. It is safe
// to call from any goroutine (it never touches t). The tunnel is left open
// until the returned conn is closed.
func connect(addr, token, target string, timeout time.Duration) (int, net.Conn, error) {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return 0, nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\nProxy-Authorization: Bearer " + token + "\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		conn.Close()
		return 0, nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		conn.Close()
		return 0, nil, err
	}
	return resp.StatusCode, conn, nil
}

// mustConnect is connect for the test goroutine; it closes the tunnel at cleanup.
func mustConnect(t *testing.T, pp *pacedProxy, target string) int {
	t.Helper()
	code, conn, err := connect(pp.addr, pp.token, target, 10*time.Second)
	if err != nil {
		t.Fatalf("CONNECT %s: %v", target, err)
	}
	t.Cleanup(func() { conn.Close() })
	return code
}

// assertSpaced checks that the i-th dial to one host began no earlier than i
// intervals after the first. Pacing guarantees a dial never starts before its
// reserved slot; a dial may start late (timer wake-up), so the gap between two
// neighbours can shrink while the schedule still holds, and only the
// cumulative bound is the invariant.
func assertSpaced(t *testing.T, dials []time.Time, gap time.Duration) {
	t.Helper()
	slices.SortFunc(dials, func(a, b time.Time) int { return a.Compare(b) })
	const slack = 10 * time.Millisecond
	for i := 1; i < len(dials); i++ {
		if d, want := dials[i].Sub(dials[0]), time.Duration(i)*gap; d < want-slack {
			t.Errorf("dial %d to one host began %v after the first, want at least %v", i, d, want)
		}
	}
}

// Back-to-back tunnels to one host are spaced by PerHostInterval, between the
// upstream dials themselves; spelling variants of the host share one schedule.
func TestProxyPacesConnectsToOneHost(t *testing.T) {
	const gap = 80 * time.Millisecond
	pp := startPacedProxy(t, gap, ProxyConfig{})
	start := time.Now()
	for _, tgt := range []string{"jobs.example.com:443", "JOBS.example.com.:443", "jobs.example.com:443"} {
		if code := mustConnect(t, pp, tgt); code != http.StatusOK {
			t.Fatalf("%s: status %d", tgt, code)
		}
	}
	if el := time.Since(start); el < 2*gap-10*time.Millisecond {
		t.Errorf("3 tunnels took %v, want at least %v", el, 2*gap)
	}
	dials := pp.dialTimes()
	if len(dials) != 3 {
		t.Fatalf("%d dials, want 3", len(dials))
	}
	assertSpaced(t, dials, gap)
}

// Another host has its own schedule: it does not wait behind a busy one.
func TestProxyPacingIsPerHost(t *testing.T) {
	const gap = 400 * time.Millisecond
	pp := startPacedProxy(t, gap, ProxyConfig{})
	if code := mustConnect(t, pp, "jobs.example.com:443"); code != http.StatusOK {
		t.Fatal(code)
	}
	start := time.Now()
	if code := mustConnect(t, pp, "other.example.com:443"); code != http.StatusOK {
		t.Fatal(code)
	}
	if el := time.Since(start); el > gap/2 {
		t.Errorf("first tunnel to another host took %v; it waited behind jobs.example.com", el)
	}
}

// Tunnels and Client requests to one host share one budget.
func TestProxyPacingSharesTheClientSchedule(t *testing.T) {
	const gap = 80 * time.Millisecond
	pp := startPacedProxy(t, gap, ProxyConfig{})
	if err := pp.p.c.pace(context.Background(), "jobs.example.com"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if code := mustConnect(t, pp, "jobs.example.com:443"); code != http.StatusOK {
		t.Fatal(code)
	}
	if el := time.Since(start); el < gap-10*time.Millisecond {
		t.Errorf("tunnel right after a Client request took %v, want at least %v", el, gap)
	}
}

// Concurrent CONNECTs to one host are serialised onto distinct slots: none
// shares a slot and none deadlocks.
func TestProxyPacesConcurrentConnects(t *testing.T) {
	const gap, n = 50 * time.Millisecond, 5
	pp := startPacedProxy(t, gap, ProxyConfig{MaxTunnels: n})
	type result struct {
		code int
		err  error
	}
	results := make(chan result, n)
	var conns sync.Map
	for i := 0; i < n; i++ {
		go func(i int) {
			code, conn, err := connect(pp.addr, pp.token, "jobs.example.com:443", 10*time.Second)
			if conn != nil {
				conns.Store(i, conn)
			}
			results <- result{code, err}
		}(i)
	}
	for i := 0; i < n; i++ {
		select {
		case r := <-results:
			if r.err != nil || r.code != http.StatusOK {
				t.Errorf("concurrent CONNECT: code %d err %v", r.code, r.err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent CONNECTs did not finish")
		}
	}
	conns.Range(func(_, v any) bool { v.(net.Conn).Close(); return true })
	dials := pp.dialTimes()
	if len(dials) != n {
		t.Fatalf("%d dials, want %d", len(dials), n)
	}
	assertSpaced(t, dials, gap)
}

// A refused CONNECT neither waits nor takes a pacing slot, so a child cannot
// grow the schedule with invented names or delay an allowed host.
func TestProxyRefusedConnectTakesNoSlot(t *testing.T) {
	const gap = 4 * time.Minute
	pp := startPacedProxy(t, gap, ProxyConfig{})
	for _, tgt := range []string{"evil.example.com:443", "unknown.example.net:443", "93.184.216.34:443"} {
		start := time.Now()
		if code := mustConnect(t, pp, tgt); code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", tgt, code)
		}
		if time.Since(start) > 2*time.Second {
			t.Errorf("%s: refusal waited %v", tgt, time.Since(start))
		}
	}
	pp.p.c.mu.Lock()
	entries := len(pp.p.c.next)
	pp.p.c.mu.Unlock()
	if entries != 0 {
		t.Errorf("refused hosts reserved %d pacing slots", entries)
	}
	start := time.Now()
	if code := mustConnect(t, pp, "jobs.example.com:443"); code != http.StatusOK {
		t.Fatal(code)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("first allowed tunnel waited %v after refusals", time.Since(start))
	}
}

// A huge interval cannot park a CONNECT past the tunnel lifetime: it fails
// with 504 at MaxLifetime, never dials, and gives its slot back.
func TestProxyPacingBoundedByLifetime(t *testing.T) {
	pp := startPacedProxy(t, maxPerHostInterval, ProxyConfig{MaxLifetime: 100 * time.Millisecond})
	if code := mustConnect(t, pp, "jobs.example.com:443"); code != http.StatusOK {
		t.Fatal(code)
	}
	before, _ := pp.next("jobs.example.com")
	start := time.Now()
	if code := mustConnect(t, pp, "jobs.example.com:443"); code != http.StatusGatewayTimeout {
		t.Errorf("parked CONNECT: status %d, want 504", code)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Errorf("parked CONNECT returned after %v, want about MaxLifetime", el)
	}
	if n := len(pp.dialTimes()); n != 1 {
		t.Errorf("%d dials, want 1: a timed-out wait must not dial", n)
	}
	if after, _ := pp.next("jobs.example.com"); !after.Equal(before) {
		t.Errorf("schedule moved from %v to %v: a cancelled wait kept its slot", before, after)
	}
}

// A child that gives up mid-wait (closes its connection) ends the wait: the
// handler returns, frees its tunnel slot, never dials and gives its pacing
// slot back.
func TestProxyPacingEndsWhenChildLeaves(t *testing.T) {
	pp := startPacedProxy(t, maxPerHostInterval, ProxyConfig{})
	if code := mustConnect(t, pp, "jobs.example.com:443"); code != http.StatusOK {
		t.Fatal(code)
	}
	before, _ := pp.next("jobs.example.com")
	conn, err := net.DialTimeout("tcp", pp.addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "CONNECT jobs.example.com:443 HTTP/1.1\r\nHost: jobs.example.com:443\r\nProxy-Authorization: Bearer "+pp.token+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the CONNECT to park", func() bool { n, _ := pp.next("jobs.example.com"); return n.After(before) })
	conn.Close()
	waitFor(t, "the parked handler to free its slot", func() bool { return len(pp.p.slots) == 1 })
	if after, _ := pp.next("jobs.example.com"); !after.Equal(before) {
		t.Errorf("schedule moved from %v to %v after the child left", before, after)
	}
	if n := len(pp.dialTimes()); n != 1 {
		t.Errorf("%d dials, want 1", n)
	}
}

// Close does not wait out a parked CONNECT.
func TestProxyCloseEndsPacedWait(t *testing.T) {
	pp := startPacedProxy(t, maxPerHostInterval, ProxyConfig{})
	if code := mustConnect(t, pp, "jobs.example.com:443"); code != http.StatusOK {
		t.Fatal(code)
	}
	before, _ := pp.next("jobs.example.com")
	done := make(chan error, 1)
	go func() {
		_, conn, err := connect(pp.addr, pp.token, "jobs.example.com:443", 10*time.Second)
		if conn != nil {
			conn.Close()
		}
		done <- err
	}()
	waitFor(t, "the CONNECT to park", func() bool { n, _ := pp.next("jobs.example.com"); return n.After(before) })
	closed := make(chan error, 1)
	go func() { closed <- pp.p.Close() }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close waited on a parked CONNECT")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("parked client was not released by Close")
	}
	if n := len(pp.dialTimes()); n != 1 {
		t.Errorf("%d dials, want 1", n)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
