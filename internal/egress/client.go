package egress

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Errors callers branch on. Each means something different to an operator:
// the first four are policy decisions, the last two are limits.
var (
	// ErrNotAllowed is a host the operator did not name.
	ErrNotAllowed = errors.New("egress: host is not in the allowlist")
	// ErrScheme is anything but https.
	ErrScheme = errors.New("egress: only https is allowed")
	// ErrBlockedAddress is a host that resolves somewhere this must not reach.
	ErrBlockedAddress = errors.New("egress: host resolves to a blocked address")
	// ErrBodyTooLarge is a response body past Config.MaxBodyBytes. It is
	// returned from Read, never swallowed into a silently shortened body.
	ErrBodyTooLarge = errors.New("egress: response body exceeds the size limit")
	// ErrTooManyRedirects is a redirect chain longer than Config.MaxRedirects.
	ErrTooManyRedirects = errors.New("egress: too many redirects")
)

// Defaults applied when a Config field is zero, and the upper bound New
// enforces on each. A value above its bound is a configuration error rather
// than being clamped, so a typo cannot quietly become "unbounded".
const (
	defaultTimeout         = 30 * time.Second
	defaultMaxBodyBytes    = 8 << 20
	defaultMaxRedirects    = 5
	defaultPerHostInterval = time.Second

	maxTimeout         = 5 * time.Minute
	maxMaxBodyBytes    = 64 << 20
	maxMaxRedirects    = 10
	maxPerHostInterval = 5 * time.Minute

	defaultUserAgent = "Vani/0.1 (+personal job-listing reader)"
	dialTimeout      = 10 * time.Second
)

// Config configures a Client.
type Config struct {
	// AllowedHosts are exact hostnames or "*.suffix" wildcards. A wildcard
	// matches subdomains of the suffix only, never the bare suffix. Empty is an
	// error: there is no allow-all.
	AllowedHosts []string
	// Timeout bounds one request, connection through last body byte.
	Timeout time.Duration
	// MaxBodyBytes bounds a response body.
	MaxBodyBytes int64
	// MaxRedirects bounds the redirect chain; every hop is fully re-vetted.
	MaxRedirects int
	// UserAgent is sent when the caller did not set one.
	UserAgent string
	// PerHostInterval is the minimum spacing between requests to one host.
	PerHostInterval time.Duration
}

// Client is the Doer that enforces the egress rules.
type Client struct {
	exact    map[string]struct{}
	suffixes []string // wildcard suffixes, each without the "*." prefix
	cfg      Config
	http     *http.Client
	transp   *http.Transport

	// The three fields below are the injection points for tests. They are
	// unexported and assigned only here, so no caller outside this package can
	// weaken the policy; tests replace them to avoid real DNS and sockets.
	resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	blocked func(netip.Addr) (string, bool)
	dialer  func(ctx context.Context, network, address string) (net.Conn, error)

	mu   sync.Mutex
	next map[string]time.Time // per-host: earliest start of the next request
}

// New builds a Client, validating cfg and filling defaults.
func New(cfg Config) (*Client, error) {
	if len(cfg.AllowedHosts) == 0 {
		return nil, errors.New("egress: AllowedHosts is empty; there is no allow-all")
	}
	var err error
	if cfg.Timeout, err = boundedDuration("Timeout", cfg.Timeout, defaultTimeout, maxTimeout); err != nil {
		return nil, err
	}
	if cfg.PerHostInterval, err = boundedDuration("PerHostInterval", cfg.PerHostInterval, defaultPerHostInterval, maxPerHostInterval); err != nil {
		return nil, err
	}
	switch {
	case cfg.MaxBodyBytes < 0 || cfg.MaxBodyBytes > maxMaxBodyBytes:
		return nil, fmt.Errorf("egress: MaxBodyBytes %d is outside 0..%d", cfg.MaxBodyBytes, int64(maxMaxBodyBytes))
	case cfg.MaxBodyBytes == 0:
		cfg.MaxBodyBytes = defaultMaxBodyBytes
	}
	switch {
	case cfg.MaxRedirects < 0 || cfg.MaxRedirects > maxMaxRedirects:
		return nil, fmt.Errorf("egress: MaxRedirects %d is outside 0..%d", cfg.MaxRedirects, maxMaxRedirects)
	case cfg.MaxRedirects == 0:
		cfg.MaxRedirects = defaultMaxRedirects
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = defaultUserAgent
	}
	for _, r := range cfg.UserAgent {
		if r < 0x20 || r == 0x7f {
			return nil, errors.New("egress: UserAgent contains a control character")
		}
	}

	c := &Client{
		exact:   map[string]struct{}{},
		cfg:     cfg,
		resolve: systemResolve,
		blocked: blockedAddr,
		next:    map[string]time.Time{},
	}
	c.dialer = (&net.Dialer{Timeout: dialTimeout}).DialContext
	for _, raw := range cfg.AllowedHosts {
		host, wildcard, err := normalizeAllowed(raw)
		if err != nil {
			return nil, err
		}
		if wildcard {
			c.suffixes = append(c.suffixes, host)
		} else {
			c.exact[host] = struct{}{}
		}
	}

	c.transp = &http.Transport{
		// The transport never resolves anything itself: c.dial connects to an
		// address already vetted, which closes DNS rebinding. TLS is layered on
		// top by the transport using the URL's hostname for SNI and
		// certificate verification.
		DialContext:           c.dial,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: cfg.Timeout,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		// No proxy: one named by the environment would send the request
		// somewhere this never vetted.
		Proxy: nil,
	}
	c.http = &http.Client{
		Transport:     c.transp,
		Timeout:       cfg.Timeout,
		Jar:           nil, // no cookies, ever
		CheckRedirect: c.checkRedirect,
	}
	return c, nil
}

func boundedDuration(name string, v, def, max time.Duration) (time.Duration, error) {
	switch {
	case v < 0 || v > max:
		return 0, fmt.Errorf("egress: %s %s is outside 0..%s", name, v, max)
	case v == 0:
		return def, nil
	}
	return v, nil
}

// ValidAllowedHost reports why New would refuse raw as an allowlist entry, or
// nil when it accepts it. It is the one rule: New runs the same normaliser.
func ValidAllowedHost(raw string) error {
	_, _, err := normalizeAllowed(raw)
	return err
}

// normalizeAllowed canonicalises one allowlist entry. It returns the host (for
// a wildcard, the suffix without "*.") and whether the entry was a wildcard.
func normalizeAllowed(raw string) (host string, wildcard bool, err error) {
	s := normHost(strings.TrimSpace(raw))
	bad := func(why string) (string, bool, error) {
		return "", false, fmt.Errorf("egress: invalid allowlist entry %q: %s", raw, why)
	}
	if s == "" {
		return bad("empty")
	}
	if strings.Contains(s, "://") {
		return bad("contains a scheme")
	}
	if strings.HasPrefix(s, "*.") {
		wildcard = true
		s = s[2:]
	}
	if strings.ContainsAny(s, ":/@?#[]*% ") {
		return bad("contains a port, path, wildcard in the wrong place or other disallowed character")
	}
	if _, perr := netip.ParseAddr(s); perr == nil {
		return bad("IP literals are not allowed")
	}
	labels := strings.Split(s, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return bad("malformed label")
		}
		for i := 0; i < len(l); i++ {
			ch := l[i]
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				return bad("only ASCII letters, digits and hyphens are allowed")
			}
		}
	}
	if last := labels[len(labels)-1]; last[0] >= '0' && last[0] <= '9' {
		return bad("looks like a numeric address")
	}
	if wildcard && len(labels) < 2 {
		return bad("a wildcard must sit under at least two labels")
	}
	return s, wildcard, nil
}

// allows matches a normalised host against the allowlist. A wildcard entry
// matches whole labels beneath its suffix, never the bare suffix.
func (c *Client) allows(host string) bool {
	if _, ok := c.exact[host]; ok {
		return true
	}
	return suffixMatch(c.suffixes, host)
}

func suffixMatch(suffixes []string, host string) bool {
	for _, suf := range suffixes {
		if len(host) > len(suf)+1 && strings.HasSuffix(host, "."+suf) {
			return true
		}
	}
	return false
}

// AllowsHost reports whether a client built with these allowlist entries would
// let host through the allowlist: the same normaliser and matching as New and
// Do (case, trailing dot, "*.suffix" wildcards). Entries New would refuse are
// ignored, as they would not be on the list.
func AllowsHost(entries []string, host string) bool {
	host = normHost(strings.TrimSpace(host))
	var suffixes []string
	for _, raw := range entries {
		h, wildcard, err := normalizeAllowed(raw)
		if err != nil {
			continue
		}
		if !wildcard && h == host {
			return true
		}
		if wildcard {
			suffixes = append(suffixes, h)
		}
	}
	return suffixMatch(suffixes, host)
}

// Do sends req under the egress policy. The caller closes the response body,
// which returns ErrBodyTooLarge once more than MaxBodyBytes has been read.
func (c *Client) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, errors.New("egress: nil request or URL")
	}
	if ctx == nil {
		return nil, errors.New("egress: nil context")
	}
	if err := c.vetURL(ctx, req.URL); err != nil {
		return nil, err
	}
	if err := c.pace(ctx, hostKey(req.URL)); err != nil {
		return nil, err
	}
	r := req.Clone(ctx)
	// The Host header must be the vetted URL host; an override would let an
	// allowed address serve a different virtual host.
	r.Host = ""
	if r.Header.Get("User-Agent") == "" {
		r.Header.Set("User-Agent", c.cfg.UserAgent)
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return nil, err
	}
	resp.Body = &limitedBody{rc: resp.Body, remaining: c.cfg.MaxBodyBytes}
	return resp, nil
}

func hostKey(u *url.URL) string { return normHost(u.Hostname()) }

// normHost is the one form of a host name every check uses: lower case and
// without the root's trailing dot ("Example.com." is example.com), so the
// allowlist, pacing and the dial all see the same name.
func normHost(h string) string { return strings.TrimRight(strings.ToLower(h), ".") }

// vetURL applies every rule decidable before a socket opens. It runs on the
// initial request and again on every redirect hop.
func (c *Client) vetURL(ctx context.Context, u *url.URL) error {
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("%w: %q", ErrScheme, u.Scheme)
	}
	if u.User != nil {
		return errors.New("egress: URL carries credentials")
	}
	host := hostKey(u)
	if host == "" {
		return errors.New("egress: URL names no host")
	}
	if p := u.Port(); p != "" && p != "443" {
		return fmt.Errorf("egress: port %s is not allowed; only 443", p)
	}
	if !c.allows(host) {
		return fmt.Errorf("%w: %q", ErrNotAllowed, host)
	}
	addrs, err := c.resolve(ctx, host)
	if err != nil {
		return fmt.Errorf("egress: %q did not resolve: %w", host, err)
	}
	return c.checkAddrs(host, addrs)
}

// checkAddrs refuses the whole name if any answer is blocked: a name that
// returns one public and one internal address is being used to walk around the
// check, and picking the public one would cooperate with it.
func (c *Client) checkAddrs(host string, addrs []netip.Addr) error {
	if len(addrs) == 0 {
		return fmt.Errorf("egress: %q resolved to no addresses", host)
	}
	for _, a := range addrs {
		if reason, blocked := c.blocked(a); blocked {
			return fmt.Errorf("%w: %q resolves to %s (%s)", ErrBlockedAddress, host, a, reason)
		}
	}
	return nil
}

func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > c.cfg.MaxRedirects {
		return fmt.Errorf("%w: more than %d", ErrTooManyRedirects, c.cfg.MaxRedirects)
	}
	if err := c.vetURL(req.Context(), req.URL); err != nil {
		return err
	}
	return c.pace(req.Context(), hostKey(req.URL))
}

// dial connects only to an address it has vetted itself. It re-resolves and
// re-checks because the window between vetURL and here is what a rebinding
// attack aims at, then dials the literal IP so no further lookup happens.
func (c *Client) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("egress: %q is not a dialable address: %w", address, err)
	}
	host = normHost(host)
	if !c.allows(host) {
		return nil, fmt.Errorf("%w: %q", ErrNotAllowed, host)
	}
	addrs, err := c.resolve(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("egress: %q did not resolve: %w", host, err)
	}
	if err := c.checkAddrs(host, addrs); err != nil {
		return nil, err
	}
	var lastErr error
	for _, a := range addrs {
		conn, err := c.dialer(ctx, network, net.JoinHostPort(a.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("egress: connecting to %q: %w", host, lastErr)
}

// pace reserves the next slot for host and waits for it, returning early with
// the context's error if ctx ends first. A cancelled wait gives its slot back.
func (c *Client) pace(ctx context.Context, host string) error {
	interval := c.cfg.PerHostInterval
	c.mu.Lock()
	now := time.Now()
	prev, seen := c.next[host]
	slot := now
	if seen && prev.After(now) {
		slot = prev
	}
	c.next[host] = slot.Add(interval)
	c.mu.Unlock()

	wait := time.Until(slot)
	if wait <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		if c.next[host].Equal(slot.Add(interval)) {
			if seen {
				c.next[host] = prev
			} else {
				delete(c.next, host)
			}
		}
		c.mu.Unlock()
		return ctx.Err()
	}
}

// limitedBody enforces the body cap. Unlike io.LimitReader it reports the
// overrun, so a caller can never mistake a cut body for a whole one.
type limitedBody struct {
	rc        io.ReadCloser
	remaining int64
	over      bool
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.over {
		return 0, ErrBodyTooLarge
	}
	if len(p) == 0 {
		return 0, nil
	}
	if b.remaining <= 0 {
		// At the cap: one more byte existing is the overrun; none is a body
		// that is exactly the cap.
		var one [1]byte
		n, err := b.rc.Read(one[:])
		if n > 0 {
			b.over = true
			return 0, ErrBodyTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.rc.Read(p)
	b.remaining -= int64(n)
	return n, err
}

func (b *limitedBody) Close() error { return b.rc.Close() }

// blockedAddr reports whether an address must not be reached, and why.
//
// DNS64 publishes a public IPv4 host as 64:ff9b::/96 (and, on a private
// translator, 64:ff9b:1::/48). Those prefixes are judged by the IPv4 they
// embed (RFC 6052): a public embedding is that public host, and an embedding
// of an address this function would refuse is refused with it. An embedding
// whose u octet is not zero is not an embedding and is refused. The check
// does not unwrap twice, and it does not unwrap 6to4 or Teredo.
func blockedAddr(addr netip.Addr) (string, bool) {
	return classifyAddr(addr, true)
}

func classifyAddr(addr netip.Addr, unwrapNAT64 bool) (string, bool) {
	addr = addr.WithZone("").Unmap()
	switch {
	case !addr.IsValid():
		return "not a valid address", true
	case addr.IsUnspecified():
		return "the unspecified address", true
	case addr.IsLoopback():
		return "loopback", true
	case addr.IsPrivate():
		return "a private network", true
	case addr.IsLinkLocalUnicast(), addr.IsLinkLocalMulticast():
		return "link-local (cloud instance metadata lives here)", true
	case addr.IsInterfaceLocalMulticast(), addr.IsMulticast():
		return "multicast", true
	}
	if unwrapNAT64 {
		if v4, bad, ok := embeddedNAT64(addr); ok || bad != "" {
			if bad != "" {
				return bad, true
			}
			if why, blocked := classifyAddr(v4, false); blocked {
				return "NAT64 translation of " + why, true
			}
			return "", false
		}
	}
	for _, r := range reservedPrefixes {
		if r.prefix.Contains(addr) {
			return r.reason, true
		}
	}
	return "", false
}

// embeddedNAT64 extracts the IPv4 address RFC 6052 places in the well-known
// NAT64 prefix (64:ff9b::/96) or the local-use prefix (64:ff9b:1::/48).
// bad is set when the address is in the local-use prefix but its u octet is
// not zero. ok is false for every other address.
func embeddedNAT64(addr netip.Addr) (v4 netip.Addr, bad string, ok bool) {
	if !addr.Is6() {
		return netip.Addr{}, "", false
	}
	b := addr.As16()
	switch {
	case wellKnownNAT64.Contains(addr):
		return netip.AddrFrom4([4]byte(b[12:16])), "", true
	case localUseNAT64.Contains(addr):
		if b[8] != 0 {
			return netip.Addr{}, "NAT64 translation with a non-zero u octet", false
		}
		return netip.AddrFrom4([4]byte{b[6], b[7], b[9], b[10]}), "", true
	default:
		return netip.Addr{}, "", false
	}
}

// wellKnownNAT64 and localUseNAT64 are unwrapped by embeddedNAT64 before
// reservedPrefixes is consulted. An address in either prefix that the unwrap
// did not recognize stays refused by that list.
var (
	wellKnownNAT64 = netip.MustParsePrefix("64:ff9b::/96")
	localUseNAT64  = netip.MustParsePrefix("64:ff9b:1::/48")
)

// reservedPrefixes are the ranges netip has no predicate for.
var reservedPrefixes = []struct {
	prefix netip.Prefix
	reason string
}{
	{netip.MustParsePrefix("0.0.0.0/8"), "this-network range"},
	{netip.MustParsePrefix("100.64.0.0/10"), "carrier-grade NAT"},
	{netip.MustParsePrefix("192.0.0.0/24"), "IETF protocol assignments"},
	{netip.MustParsePrefix("192.0.2.0/24"), "documentation range"},
	{netip.MustParsePrefix("198.18.0.0/15"), "benchmarking range"},
	{netip.MustParsePrefix("198.51.100.0/24"), "documentation range"},
	{netip.MustParsePrefix("203.0.113.0/24"), "documentation range"},
	{netip.MustParsePrefix("240.0.0.0/4"), "reserved"},
	{netip.MustParsePrefix("::/96"), "IPv4-compatible / unspecified range"},
	{wellKnownNAT64, "NAT64 translation of the v4 space"},
	{localUseNAT64, "local-use NAT64 translation (RFC 8215)"},
	{netip.MustParsePrefix("2001::/32"), "Teredo tunnel"},
	{netip.MustParsePrefix("2001:db8::/32"), "documentation range"},
	{netip.MustParsePrefix("2002::/16"), "6to4 tunnel (embeds a v4 address)"},
}

// systemResolve is the production lookup.
func systemResolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{addr}, nil
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}
