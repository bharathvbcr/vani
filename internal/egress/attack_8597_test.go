package egress

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// shortProxy is a proxy with 150 ms server timeouts whose upstream dial is dial.
func shortProxy(t *testing.T, dial func(ctx context.Context) (net.Conn, error)) (string, string) {
	t.Helper()
	c, _ := newTestClient(t, Config{AllowedHosts: []string{"jobs.example.com"}}, &fakeDNS{answers: defaultAnswers()}, nil)
	c.dialer = func(ctx context.Context, network, addr string) (net.Conn, error) { return dial(ctx) }
	p, err := NewProxy(c, ProxyConfig{IdleTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	p.headerTimeout, p.idleConnTimeout = 150*time.Millisecond, 150*time.Millisecond
	addr, token, err := p.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return addr, token
}

// A slow upstream dial (several times both server timeouts) still gives the
// client its tunnel: the server timeouts must not run during the handler.
func TestAttackSlowUpstreamDialStillTunnels(t *testing.T) {
	ln := echoServer(t)
	addr, token := shortProxy(t, func(ctx context.Context) (net.Conn, error) {
		select {
		case <-time.After(900 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	})
	code, conn, br := rawRequest(t, addr, "CONNECT", "jobs.example.com:443", auth(token))
	if code != 200 {
		t.Fatalf("status %d after a slow dial", code)
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = io.WriteString(conn, "hi")
	buf := make([]byte, 2)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "hi" {
		t.Fatalf("echo %q, %v", buf, err)
	}
}

// A header trickled one byte at a time is cut at the header timeout, however
// steadily the bytes arrive.
func TestAttackSlowlorisHeaderIsCut(t *testing.T) {
	addr, _ := shortProxy(t, func(ctx context.Context) (net.Conn, error) { return nil, io.EOF })
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	line := "CONNECT jobs.example.com:443 HTTP/1.1\r\nHost: jobs.example.com\r\nX-A: "
	cut := false
	for i := 0; i < len(line) && time.Since(start) < 4*time.Second; i++ {
		_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
		if _, err := conn.Write([]byte{line[i]}); err != nil {
			cut = true
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if !cut {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, err := io.Copy(io.Discard, conn)
		var ne net.Error
		if ok := asNetErr(err, &ne); ok && ne.Timeout() {
			t.Fatalf("trickling client still connected after %s", time.Since(start))
		}
	}
}

func asNetErr(err error, target *net.Error) bool {
	ne, ok := err.(net.Error)
	if ok {
		*target = ne
	}
	return ok
}

// A kept-alive connection (a refused non-CONNECT request: net/http closes after
// a refused CONNECT, so that path never keeps the connection) serves a CONNECT
// on the same connection, and that tunnel outlives the idle bound.
func TestAttackKeepAliveReuseThenTunnel(t *testing.T) {
	ln := echoServer(t)
	addr, token := shortProxy(t, func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	})
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)
	_, _ = io.WriteString(conn, "GET http://jobs.example.com/ HTTP/1.1\r\nHost: jobs.example.com\r\n\r\n")
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil || resp.StatusCode != 407 {
		t.Fatalf("first: %v %v", resp, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	time.Sleep(60 * time.Millisecond) // inside the 150 ms idle bound
	_, _ = io.WriteString(conn, "CONNECT jobs.example.com:443 HTTP/1.1\r\nHost: jobs.example.com\r\nProxy-Authorization: Bearer "+token+"\r\n\r\n")
	resp, err = http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("second: %v %v", resp, err)
	}
	time.Sleep(600 * time.Millisecond)
	_, _ = io.WriteString(conn, "yo")
	buf := make([]byte, 2)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "yo" {
		t.Fatalf("echo after idle on a reused connection: %q, %v", buf, err)
	}
}

// The idle bound really applies to a connection that net/http keeps alive: a
// refused GET, then silence, is closed by the server.
func TestAttackKeptAliveConnectionIsClosedWhenIdle(t *testing.T) {
	addr, _ := shortProxy(t, func(ctx context.Context) (net.Conn, error) { return nil, io.EOF })
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	br := bufio.NewReader(conn)
	_, _ = io.WriteString(conn, "GET http://jobs.example.com/ HTTP/1.1\r\nHost: jobs.example.com\r\n\r\n")
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil || resp.Close {
		t.Fatalf("setup: connection not kept alive: %v close=%v", err, resp != nil && resp.Close)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	if _, err := br.ReadByte(); err != io.EOF {
		t.Fatalf("idle kept-alive connection not closed by the server: %v", err)
	}
}
