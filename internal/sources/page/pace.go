package page

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// MaxInterval is the longest per-host wait Paced accepts. One board runs up to
// MaxLinks+1 fetches in sequence within boardTimeout (15 min); a 10 s gap
// leaves 51 runs about 8.5 min of waiting, with room for the runs themselves.
// A longer gap would time large boards out.
const MaxInterval = 10 * time.Second

// Paced is a Runner that spaces lightpanda runs per host: a run to a host
// starts only once the previous run to that host has ended and interval has
// passed since. Runs to different hosts do not wait on each other.
//
// The wait happens here, before the process starts, rather than in the egress
// proxy's CONNECT: lightpanda gives up on a first CONNECT that waits past its
// --wait-ms (5 s) and exits 0 with a bare doctype (ErrEmptyDocument), so the
// proxy's own pacing cannot honour a gap as long as a robots Crawl-delay of
// 5 s. With --http-max-host-open 1 and a gap of at least the proxy's
// per-host interval, the proxy then has nothing left to wait for.
//
// The host is that of the last argument, which fetch always sets to the
// validated page URL. A last argument that is not an https URL with a host is
// an error: the run is never made unpaced.
type Paced struct {
	next     Runner
	interval time.Duration
	now      func() time.Time

	mu    sync.Mutex
	hosts map[string]*hostSlot
}

// hostSlot is one host's schedule. token holds one value while a run to the
// host is waiting or in flight; free is when the next run may start.
type hostSlot struct {
	token chan struct{}
	free  time.Time
	users int // callers holding or queued for token; guarded by Paced.mu
}

// NewPaced wraps next so runs to one host are spaced by interval, which must
// be within (0, MaxInterval]. Zero is not accepted: a caller that wants no
// wait uses next directly.
func NewPaced(next Runner, interval time.Duration) (*Paced, error) {
	if next == nil {
		return nil, errors.New("page: NewPaced needs a runner")
	}
	if interval <= 0 || interval > MaxInterval {
		return nil, fmt.Errorf("page: per-host interval %s must be within (0, %s]", interval, MaxInterval)
	}
	return &Paced{next: next, interval: interval, now: time.Now, hosts: map[string]*hostSlot{}}, nil
}

// Run waits for the target host's turn, then runs next. A context cancelled
// while waiting returns its error without starting a run and without
// delaying the callers behind it.
func (p *Paced) Run(ctx context.Context, args []string) ([]byte, error) {
	host, err := targetHost(args)
	if err != nil {
		return nil, err
	}
	slot := p.join(host)
	defer p.leave(slot)

	select {
	case slot.token <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-slot.token }()

	p.mu.Lock()
	wait := slot.free.Sub(p.now())
	p.mu.Unlock()
	if wait > 0 {
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
	}

	out, err := p.next.Run(ctx, args)
	// Ended, successfully or not: the host gets its rest either way.
	p.mu.Lock()
	slot.free = p.now().Add(p.interval)
	p.mu.Unlock()
	return out, err
}

// join returns host's slot, creating it if needed, and counts the caller.
// Slots that nobody holds and whose rest has passed are dropped first, so the
// map stays as small as the set of recently fetched hosts.
func (p *Paced) join(host string) *hostSlot {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for h, s := range p.hosts {
		if h != host && s.users == 0 && !now.Before(s.free) {
			delete(p.hosts, h)
		}
	}
	s := p.hosts[host]
	if s == nil {
		s = &hostSlot{token: make(chan struct{}, 1)}
		p.hosts[host] = s
	}
	s.users++
	return s
}

func (p *Paced) leave(s *hostSlot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s.users--
}

// targetHost is the lower-cased host of args' last element.
func targetHost(args []string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("page: paced run has no target url")
	}
	u, err := url.Parse(args[len(args)-1])
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" {
		return "", errors.New("page: paced run's last argument is not an https url")
	}
	return strings.ToLower(u.Hostname()), nil
}
