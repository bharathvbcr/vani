package page

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
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
// Built by NewRobotsPaced, a host's interval is the longer of the configured
// one and the Crawl-delay its robots.txt sets for Vani, read once per host for
// the life of the Paced (one run). A host whose robots.txt cannot be read, or
// whose Crawl-delay is above MaxInterval, gets no run: the error fails its
// board rather than pacing it faster than the site asked.
//
// The host is that of the last argument, which fetch always sets to the
// validated page URL. A last argument that is not an https URL with a host is
// an error: the run is never made unpaced.
type Paced struct {
	next     Runner
	interval time.Duration // the fixed interval, or the floor under Crawl-delay
	now      func() time.Time

	robots        egress.Doer // nil: interval alone
	robotsTimeout time.Duration

	mu        sync.Mutex
	hosts     map[string]*hostSlot
	intervals map[string]time.Duration // per host, once its robots.txt is read
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

// NewRobotsPaced wraps next so runs to one host are spaced by the longer of
// floor and the host's robots.txt Crawl-delay, which is fetched through d.
// floor must be within [0, MaxInterval]; zero leaves Crawl-delay alone.
func NewRobotsPaced(next Runner, d egress.Doer, floor time.Duration) (*Paced, error) {
	if next == nil {
		return nil, errors.New("page: NewRobotsPaced needs a runner")
	}
	if d == nil {
		return nil, errors.New("page: NewRobotsPaced needs an egress doer")
	}
	if floor < 0 || floor > MaxInterval {
		return nil, fmt.Errorf("page: per-host interval %s must be within [0, %s]", floor, MaxInterval)
	}
	return &Paced{next: next, interval: floor, now: time.Now, hosts: map[string]*hostSlot{},
		robots: d, robotsTimeout: robotsTimeout, intervals: map[string]time.Duration{}}, nil
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

	// Under the token, so one host's robots.txt is fetched by one caller.
	interval, err := p.intervalFor(ctx, host)
	if err != nil {
		return nil, err
	}
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
	slot.free = p.now().Add(interval)
	p.mu.Unlock()
	return out, err
}

// intervalFor is host's interval: the fixed one, or the longer of the floor
// and the host's Crawl-delay. A successful read is kept apart from the host's
// slot, which join may drop between runs; a failed one is not kept.
func (p *Paced) intervalFor(ctx context.Context, host string) (time.Duration, error) {
	if p.robots == nil {
		return p.interval, nil
	}
	p.mu.Lock()
	iv, ok := p.intervals[host]
	p.mu.Unlock()
	if ok {
		return iv, nil
	}
	secs, err := crawlDelay(ctx, p.robots, host, p.robotsTimeout)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return 0, cerr
		}
		return 0, err
	}
	if secs > MaxInterval.Seconds() {
		return 0, fmt.Errorf("%w: %s asks %gs, the cap is %s; its pages are not fetched", ErrCrawlDelayTooLong, host, secs, MaxInterval)
	}
	iv = max(p.interval, delayDuration(secs))
	p.mu.Lock()
	p.intervals[host] = iv
	p.mu.Unlock()
	return iv, nil
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
