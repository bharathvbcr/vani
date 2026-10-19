package egress

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Proxy defaults and the upper bound NewProxy enforces on each. As in New, a
// value above its bound is a configuration error rather than being clamped.
const (
	defaultProxyTunnels  = 16
	defaultProxyIdle     = 60 * time.Second
	defaultProxyBytes    = 64 << 20
	defaultProxyLifetime = 5 * time.Minute

	maxProxyTunnels  = 256
	maxProxyIdle     = 10 * time.Minute
	maxProxyBytes    = 1 << 30
	maxProxyLifetime = time.Hour
)

// ProxyConfig bounds a Proxy. Zero fields take the defaults.
type ProxyConfig struct {
	// MaxTunnels bounds concurrent CONNECT tunnels; the next is answered 503.
	MaxTunnels int
	// IdleTimeout closes a tunnel with no traffic in either direction.
	IdleTimeout time.Duration
	// MaxBytes bounds the bytes carried in each direction of one tunnel.
	MaxBytes int64
	// MaxLifetime bounds one tunnel from establishment to close.
	MaxLifetime time.Duration
}

// Proxy is an authenticated, loopback-only CONNECT proxy for a child process
// that opens its own sockets (a headless browser). It applies the Client's
// rules to every tunnel: only port 443, only allowlisted hosts, tunnels to one
// host spaced by PerHostInterval, only vetted addresses, via the Client's own
// pinned dial. A child opens one tunnel per host connection, not per request.
// The tunnel carries the child's TLS end to end; the proxy never sees
// plaintext and never forwards http://.
type Proxy struct {
	c   *Client
	cfg ProxyConfig

	mu      sync.Mutex
	started bool
	closed  bool
	srv     *http.Server
	token   string
	conns   map[net.Conn]struct{}
	slots   chan struct{}
	done    chan struct{} // closed by Close
	wg      sync.WaitGroup
}

// NewProxy validates cfg and binds the proxy to c's rules.
func NewProxy(c *Client, cfg ProxyConfig) (*Proxy, error) {
	if c == nil {
		return nil, errors.New("egress: proxy needs a client")
	}
	var err error
	switch {
	case cfg.MaxTunnels < 0 || cfg.MaxTunnels > maxProxyTunnels:
		return nil, fmt.Errorf("egress: MaxTunnels %d is outside 0..%d", cfg.MaxTunnels, maxProxyTunnels)
	case cfg.MaxTunnels == 0:
		cfg.MaxTunnels = defaultProxyTunnels
	}
	if cfg.IdleTimeout, err = boundedDuration("IdleTimeout", cfg.IdleTimeout, defaultProxyIdle, maxProxyIdle); err != nil {
		return nil, err
	}
	if cfg.MaxLifetime, err = boundedDuration("MaxLifetime", cfg.MaxLifetime, defaultProxyLifetime, maxProxyLifetime); err != nil {
		return nil, err
	}
	switch {
	case cfg.MaxBytes < 0 || cfg.MaxBytes > maxProxyBytes:
		return nil, fmt.Errorf("egress: MaxBytes %d is outside 0..%d", cfg.MaxBytes, int64(maxProxyBytes))
	case cfg.MaxBytes == 0:
		cfg.MaxBytes = defaultProxyBytes
	}
	return &Proxy{
		c:     c,
		cfg:   cfg,
		conns: map[net.Conn]struct{}{},
		slots: make(chan struct{}, cfg.MaxTunnels),
		done:  make(chan struct{}),
	}, nil
}

// Start listens on 127.0.0.1 (never another interface) and returns the address
// to pass as the child's proxy and the bearer token it must present. The token
// is fresh per Start. Cancelling ctx is the same as Close. A Proxy starts once.
func (p *Proxy) Start(ctx context.Context) (addr, token string, err error) {
	if ctx == nil {
		return "", "", errors.New("egress: nil context")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("egress: generating proxy token: %w", err)
	}
	token = hex.EncodeToString(raw)

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started || p.closed {
		return "", "", errors.New("egress: proxy already started or closed")
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return "", "", fmt.Errorf("egress: proxy listen: %w", err)
	}
	p.started = true
	p.token = token
	p.srv = &http.Server{
		Handler:           http.HandlerFunc(p.serve),
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	srv := p.srv
	go func() { _ = srv.Serve(ln) }()
	go func() {
		select {
		case <-ctx.Done():
			_ = p.Close()
		case <-p.done:
		}
	}()
	return ln.Addr().String(), token, nil
}

// Close stops the listener, tears down every open tunnel and waits for them.
// It is safe to call more than once.
func (p *Proxy) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	close(p.done)
	srv := p.srv
	for c := range p.conns {
		_ = c.Close()
	}
	p.mu.Unlock()
	var err error
	if srv != nil {
		err = srv.Close()
	}
	p.wg.Wait()
	return err
}

// begin registers a handler; false means the proxy is closing.
func (p *Proxy) begin() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.wg.Add(1)
	return true
}

// trackPair records both halves of a tunnel so Close can tear them down, in
// one step: a Close cannot land between them and leave one half recorded.
// false means the proxy closed meanwhile and both were closed. release
// forgets them again.
func (p *Proxy) trackPair(cli, up net.Conn) (release func(), ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = cli.Close()
		_ = up.Close()
		return nil, false
	}
	p.conns[cli] = struct{}{}
	p.conns[up] = struct{}{}
	return func() {
		p.mu.Lock()
		delete(p.conns, cli)
		delete(p.conns, up)
		p.mu.Unlock()
	}, true
}

// authorized accepts the token as a bearer credential or, because Lightpanda
// sends --proxy-bearer-token on plain-HTTP proxying only and never on CONNECT
// (docs/PHASE0-lightpanda.md), as the password of Basic credentials; the user
// name is ignored. Both compare in constant time.
func (p *Proxy) authorized(r *http.Request) bool {
	h := r.Header.Get("Proxy-Authorization")
	scheme, got, ok := strings.Cut(h, " ")
	if !ok {
		return false
	}
	got = strings.TrimSpace(got)
	switch {
	case strings.EqualFold(scheme, "Bearer"):
	case strings.EqualFold(scheme, "Basic"):
		raw, err := base64.StdEncoding.DecodeString(got)
		if err != nil {
			return false
		}
		_, pass, found := strings.Cut(string(raw), ":")
		if !found {
			return false
		}
		got = pass
	default:
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(p.token)) == 1
}

func (p *Proxy) serve(w http.ResponseWriter, r *http.Request) {
	if !p.authorized(r) {
		w.Header().Set("Proxy-Authenticate", `Bearer realm="vani-egress"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if r.Method != http.MethodConnect {
		w.Header().Set("Allow", http.MethodConnect)
		http.Error(w, "only CONNECT is supported", http.StatusMethodNotAllowed)
		return
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || host == "" {
		http.Error(w, "CONNECT target must be host:443", http.StatusBadRequest)
		return
	}
	if port != "443" {
		http.Error(w, "only port 443 is allowed", http.StatusForbidden)
		return
	}
	if !p.begin() {
		http.Error(w, "proxy closing", http.StatusServiceUnavailable)
		return
	}
	defer p.wg.Done()

	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		http.Error(w, "too many tunnels", http.StatusServiceUnavailable)
		return
	}

	// The lifetime bounds the pacing wait as well as the tunnel: a CONNECT
	// parked behind a long PerHostInterval holds a slot only until the child
	// gives up (its connection closes, which ends r.Context) or this lapses.
	ctx, cancel := context.WithTimeout(r.Context(), p.cfg.MaxLifetime)
	defer cancel()
	up, err := p.c.dialVetted(ctx, host, port)
	if err != nil {
		code := http.StatusBadGateway
		switch {
		case errors.Is(err, ErrNotAllowed) || errors.Is(err, ErrBlockedAddress):
			code = http.StatusForbidden
		case errors.Is(err, context.DeadlineExceeded):
			code = http.StatusGatewayTimeout
		}
		// The status carries the policy outcome; the error text names a host
		// and address, which the child may see, and never the token.
		http.Error(w, err.Error(), code)
		return
	}
	defer up.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "connection cannot be hijacked", http.StatusInternalServerError)
		return
	}
	cli, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer cli.Close()
	_ = cli.SetDeadline(time.Time{})
	release, ok := p.trackPair(cli, up)
	if !ok {
		return
	}
	defer release()

	if _, err := io.WriteString(cli, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	p.tunnel(ctx, cli, rw.Reader, up)
}

// tunnel pumps bytes both ways until either side ends, a cap trips, the
// tunnel idles out, its lifetime lapses or the proxy closes.
func (p *Proxy) tunnel(ctx context.Context, cli net.Conn, cliBuffered io.Reader, up net.Conn) {
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	touch := func() { last.Store(time.Now().UnixNano()) }

	stop := make(chan struct{})
	var once sync.Once
	shutdown := func() {
		once.Do(func() {
			close(stop)
			_ = cli.Close()
			_ = up.Close()
		})
	}
	defer shutdown()

	go func() {
		interval := p.cfg.IdleTimeout / 4
		if interval < time.Millisecond {
			interval = time.Millisecond
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				shutdown()
				return
			case <-p.done:
				shutdown()
				return
			case <-t.C:
				if time.Since(time.Unix(0, last.Load())) > p.cfg.IdleTimeout {
					shutdown()
					return
				}
			}
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if copyCapped(up, cliBuffered, p.cfg.MaxBytes, touch) != nil {
			shutdown()
		}
	}()
	go func() {
		defer wg.Done()
		if copyCapped(cli, up, p.cfg.MaxBytes, touch) != nil {
			shutdown()
		}
	}()
	wg.Wait()
}

var errTunnelCap = errors.New("egress: tunnel byte cap reached")

// copyCapped copies src to dst, at most limit bytes, and half-closes dst on a
// clean EOF. Reaching the cap with more data pending is an error: the tunnel
// is cut rather than silently truncated.
func copyCapped(dst net.Conn, src io.Reader, limit int64, touch func()) error {
	buf := make([]byte, 32<<10)
	var total int64
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if total+int64(n) > limit {
				return errTunnelCap
			}
			total += int64(n)
			touch()
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				if cw, ok := dst.(interface{ CloseWrite() error }); ok {
					_ = cw.CloseWrite()
					return nil
				}
			}
			return rerr
		}
	}
}
