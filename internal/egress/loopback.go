package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

// Loopback is the Doer for services running on this Mac, such as a local
// decision model served by llama.cpp. It is the mirror image of Client:
// Client refuses loopback and private addresses, Loopback reaches nothing
// else. The check runs where the socket is opened (on the resolved address,
// so "localhost" cannot be pointed elsewhere), redirects are never followed,
// no environment proxy is used, and bodies are capped.
type Loopback struct {
	hc      *http.Client
	maxBody int64
}

const (
	defaultLoopbackTimeout = 30 * time.Second
	maxLoopbackTimeout     = 2 * time.Minute
	defaultLoopbackBody    = 2 << 20
	maxLoopbackBody        = 16 << 20
)

// ErrNotLoopback is returned for any request whose host is not this machine.
var ErrNotLoopback = errors.New("egress: loopback client reaches loopback addresses only")

// NewLoopback returns a loopback-only Doer. Zero timeout means 30 s (max 2 m);
// zero maxBody means 2 MiB (max 16 MiB).
func NewLoopback(timeout time.Duration, maxBody int64) (*Loopback, error) {
	t, err := boundedDuration("loopback timeout", timeout, defaultLoopbackTimeout, maxLoopbackTimeout)
	if err != nil {
		return nil, err
	}
	switch {
	case maxBody == 0:
		maxBody = defaultLoopbackBody
	case maxBody < 0 || maxBody > maxLoopbackBody:
		return nil, fmt.Errorf("egress: loopback body cap %d outside (0, %d]", maxBody, maxLoopbackBody)
	}
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		// Control sees the address actually being connected to, after any
		// name resolution: the authoritative check.
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !ap.Addr().Unmap().IsLoopback() {
				return fmt.Errorf("%w: %s", ErrNotLoopback, address)
			}
			return nil
		},
	}
	tr := &http.Transport{
		Proxy:                 nil, // never an environment proxy: the request must stay on this machine
		DialContext:           dialer.DialContext,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: t,
	}
	return &Loopback{
		hc: &http.Client{
			Transport:     tr,
			Timeout:       t,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		maxBody: maxBody,
	}, nil
}

// IsLoopbackHost is true for "localhost" and loopback IP literals. It never
// resolves a name; the dialer checks the resolved address separately.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	a, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && a.Unmap().IsLoopback()
}

// Do sends req to a loopback address and caps the response body.
func (l *Loopback) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	u := req.URL
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || !IsLoopbackHost(u.Hostname()) || u.User != nil {
		return nil, ErrNotLoopback
	}
	resp, err := l.hc.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	resp.Body = &limitedBody{rc: resp.Body, remaining: l.maxBody}
	return resp, nil
}
