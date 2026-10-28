package srcutil

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
)

// A host that answers 429 to everything (an IP block, a long rate limit) would
// otherwise be asked again, with a full retry budget, by every board that uses
// it and on every run. HostThrottle is a run's memory of such hosts, seeded from
// the last run's and written back at the end (cooldownfile.go).
//
// Per host it is a small state machine:
//
//   - open: no memory of the host, or it has answered since. Requests go out
//     with the usual retries.
//   - probe: a request ended rate limited (this run), or the last run's
//     cooldown names the host. The next request is the probe: sent once, with
//     no retries. Requests that arrive while it is out wait for its answer.
//   - blocked: the probe was answered 429 again, or a cooldown's backoff
//     window has not passed. No further request is sent to the host this run;
//     each one fails as rate limited at once, and says so.
//
// A 200 from the host reopens it and clears its cooldown. Nothing here touches
// a board's postings: a throttled board fails as it always did, only sooner.
//
// The memory is per host and says nothing about a posting, so a cooldown can
// never age or kill one.

const (
	// MaxCooldownRuns bounds how many consecutive rate-limited runs a host's
	// cooldown counts.
	MaxCooldownRuns = 99
	// maxThrottleHosts bounds the hosts remembered at once, in a run and on disk.
	maxThrottleHosts = 64
	// maxProbeGap is the longest backoff window: a host is probed at least this
	// often, so one that recovers is found again within it, and a daily run
	// always probes.
	maxProbeGap = 6 * time.Hour
	// probeGapBase is the window after a host's second consecutive rate-limited
	// run; it doubles with each further one up to maxProbeGap.
	probeGapBase = 15 * time.Minute
)

// probeGap is how long after its last rate-limited run a host with runs
// consecutive rate-limited runs is left alone. The first rate-limited run
// earns no window, so the next run always sends its one probe.
func probeGap(runs int) time.Duration {
	if runs <= 1 {
		return 0
	}
	if runs > 12 {
		return maxProbeGap
	}
	return min(probeGapBase<<(runs-2), maxProbeGap)
}

// HostCooldown is what one run leaves for the next about a throttled host.
type HostCooldown struct {
	// Runs is how many consecutive runs ended with the host rate limited.
	Runs int
	// Last is when the latest of them last saw a 429.
	Last time.Time
}

type throttleKey struct{}

type hostPhase int

const (
	hostOpen hostPhase = iota
	hostProbe
	hostBlocked
)

type hostEntry struct {
	phase hostPhase
	// prior and last are the cooldown loaded for the host (prior 0: none).
	prior int
	last  time.Time
	// until is the earliest a probe may go out, for a loaded cooldown.
	until time.Time
	// probing is non-nil while the probe is out; it is closed when it is
	// answered or abandoned.
	probing chan struct{}
	// why is what a blocked host's requests fail with.
	why string
	// seen is the latest evidence of this run: 429 (limited) or a 200 (answered).
	seen      evidence
	limitedAt time.Time // when the latest 429 was seen
}

type evidence int

const (
	noEvidence evidence = iota
	sawLimit
	sawAnswer
)

// HostThrottle is the memory of throttled hosts for one run. It is safe for
// concurrent use.
type HostThrottle struct {
	mu    sync.Mutex
	clock func() time.Time
	hosts map[string]*hostEntry
}

// NewHostThrottle returns a throttle seeded with the cooldowns of earlier runs
// (LoadCooldowns). clock reads the time and defaults to time.Now. A cooldown
// stamped in the future (a clock set back) is probed at once, since the window
// it asks for cannot be told.
func NewHostThrottle(prior map[string]HostCooldown, clock func() time.Time) *HostThrottle {
	t := &HostThrottle{clock: clock, hosts: map[string]*hostEntry{}}
	if t.clock == nil {
		t.clock = func() time.Time { return now() }
	}
	at := t.clock()
	for h, c := range prior {
		if c.Runs < 1 || len(t.hosts) >= maxThrottleHosts {
			continue
		}
		e := &hostEntry{phase: hostProbe, prior: min(c.Runs, MaxCooldownRuns), last: c.Last}
		if !c.Last.After(at) {
			e.until = c.Last.Add(probeGap(e.prior))
		}
		t.hosts[h] = e
	}
	return t
}

// WithHostThrottle returns ctx carrying t: requests made with it consult and
// feed t.
func WithHostThrottle(ctx context.Context, t *HostThrottle) context.Context {
	return context.WithValue(ctx, throttleKey{}, t)
}

// Cooldowns is what to leave for the next run: each host whose latest answer
// this run was a 429, counted once more than the run before; hosts the run
// never reached keep the cooldown they had; a host that answered is dropped.
func (t *HostThrottle) Cooldowns() map[string]HostCooldown {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := map[string]HostCooldown{}
	for h, e := range t.hosts {
		switch {
		case e.seen == sawLimit:
			out[h] = HostCooldown{Runs: min(e.prior+1, MaxCooldownRuns), Last: e.limitedAt}
		case e.seen == noEvidence && e.prior > 0:
			out[h] = HostCooldown{Runs: e.prior, Last: e.last}
		}
	}
	return out
}

// blockedWhy is what a blocked host's requests say: the host was probed once.
func (e *hostEntry) blockedWhy() string {
	if e.prior > 0 {
		return fmt.Sprintf("host in cooldown after %d rate-limited runs; probed once", min(e.prior+1, MaxCooldownRuns))
	}
	return "host rate limited earlier this run; probed once"
}

// coolingWhy is what a request says when a loaded cooldown's window has not
// passed.
func (e *hostEntry) coolingWhy() string {
	return fmt.Sprintf("host in cooldown after %d rate-limited runs; not probed until %s", e.prior, e.until.UTC().Format("2006-01-02 15:04 UTC"))
}

// refusal is the error of a request the host's memory refused: none was sent.
func refusal(host, method, path, why string) *RateLimitedError {
	return &RateLimitedError{Host: host, Method: method, Path: path, Why: why, noRequest: true,
		Status: &egress.StatusError{Code: http.StatusTooManyRequests, Method: method, Host: host}}
}

// HostBlocked returns the *RateLimitedError that a request to host would be
// refused with by ctx's HostThrottle, and nil when it would be sent: the host is
// open, a probe is due or out, or ctx carries no throttle. It sends and
// reserves nothing, so a request made outside DoJSON (a robots.txt read) can
// keep the rule that a blocked host is asked for nothing more this run.
func HostBlocked(ctx context.Context, host, method, rawURL string) error {
	t, _ := ctx.Value(throttleKey{}).(*HostThrottle)
	if t == nil {
		return nil
	}
	host = strings.ToLower(host)
	path := rawURL
	if u, err := url.Parse(rawURL); err == nil {
		path = u.Path
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.hosts[host]
	switch {
	case e == nil || e.phase == hostOpen:
		return nil
	case e.phase == hostBlocked:
		return refusal(host, method, path, e.why)
	case e.probing == nil && t.clock().Before(e.until):
		return refusal(host, method, path, e.coolingWhy())
	}
	return nil
}

// admit decides whether a request to host may go out. A non-empty probeWhy
// says it is the host's probe; an error is a *RateLimitedError for a request
// that must not be sent. It waits for a probe already out, ending with ctx.
func (t *HostThrottle) admit(ctx context.Context, host, method, rawURL string) (probeWhy string, err error) {
	path := rawURL
	if u, perr := url.Parse(rawURL); perr == nil {
		path = u.Path
	}
	refuse := func(why string) error { return refusal(host, method, path, why) }
	for {
		t.mu.Lock()
		e := t.hosts[host]
		if e == nil || e.phase == hostOpen {
			t.mu.Unlock()
			return "", nil
		}
		switch {
		case e.phase == hostBlocked:
			why := e.why
			t.mu.Unlock()
			return "", refuse(why)
		case e.probing != nil:
			wait := e.probing
			t.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return "", fmt.Errorf("sources: %s %s: waiting for the probe of %s: %w", method, path, host, ctx.Err())
			}
		case t.clock().Before(e.until):
			e.phase = hostBlocked
			e.why = e.coolingWhy()
			why := e.why
			t.mu.Unlock()
			return "", refuse(why)
		}
		e.probing = make(chan struct{})
		why := e.blockedWhy()
		t.mu.Unlock()
		return why, nil
	}
}

// settle ends the probe of e, waking the requests that waited for it.
func (e *hostEntry) settle() {
	if e.probing != nil {
		close(e.probing)
		e.probing = nil
	}
}

// answered records a 200 from host: it is open again and its cooldown is gone.
func (t *HostThrottle) answered(host string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := t.hosts[host]; e != nil {
		e.settle()
		e.phase, e.seen, e.prior = hostOpen, sawAnswer, 0
	}
}

// limited records a request to host that ended in a 429 after the retries the
// bounds allow. A host with no memory is now in the probe state; a probe that
// was answered 429 blocks the host for the rest of the run.
func (t *HostThrottle) limited(host string, probe bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.hosts[host]
	if e == nil {
		if len(t.hosts) >= maxThrottleHosts {
			return // too many hosts to track: the rest keep the usual retries
		}
		e = &hostEntry{}
		t.hosts[host] = e
	}
	e.seen, e.limitedAt = sawLimit, t.clock()
	if probe {
		e.settle()
		e.phase = hostBlocked
		e.why = e.blockedWhy()
		return
	}
	if e.phase == hostOpen {
		e.phase = hostProbe
		e.until = time.Time{}
	}
}

// inconclusive ends the probe of host, if probe says one was out, without a
// verdict (the request failed, was cancelled, or got a status that says nothing
// of a rate limit): the host stays in the probe state for the next request.
func (t *HostThrottle) inconclusive(host string, probe bool) {
	if !probe {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := t.hosts[host]; e != nil {
		e.settle()
	}
}

// hostLimitedWhy reports whether ctx's throttle has host out of the open state,
// and what a request retrying on it should say.
func hostLimitedWhy(ctx context.Context, host string) (string, bool) {
	t, _ := ctx.Value(throttleKey{}).(*HostThrottle)
	if t == nil {
		return "", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.hosts[strings.ToLower(host)]
	if e == nil || e.phase == hostOpen {
		return "", false
	}
	return "host rate limited earlier this run", true
}
