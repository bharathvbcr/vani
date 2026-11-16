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
//   - blocked: the probe of a host that was already in cooldown was answered
//     429 again, a cooldown's backoff window has not passed, or the host kept
//     answering 429 through the in-run probes below. No further request is sent
//     to the host this run; each one fails as rate limited at once, and says so.
//
// A host that is first rate limited in this run is not blocked for the rest of
// it, which would leave every later board on a shared host (a Greenhouse or
// Lever API serves hundreds) unread because of one 429. It cools: the probe
// waits out the server's Retry-After when the 429 gave one (a request is
// retried for at most maxRetryWait, so it is bounded by maxInRunCool), else
// inRunDefaultCool, charged to the board's and the run's retry budgets. A probe
// that is answered 429 again lengthens the wait (doubling, up to maxInRunCool)
// and, after maxInRunProbeFailures of them, blocks the host for the run. A host
// that asks for more than maxInRunCool is blocked at once. The probe is always
// one attempt, so a host that keeps refusing is asked at most
// maxInRunProbeFailures+1 more times, each after a longer wait.
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

	// inRunDefaultCool is how long a host first rate limited in this run
	// cools when its 429 named no Retry-After; inRunMinCool is the shortest a
	// Retry-After is waited out (Retry-After: 0 is no reason to ask again at
	// once).
	inRunDefaultCool = 30 * time.Second
	inRunMinCool     = 5 * time.Second
	// maxInRunCool bounds one in-run cooldown. A host that asks for longer is
	// blocked for the run.
	maxInRunCool = 2 * time.Minute
	// maxInRunProbeFailures is how many probes of one in-run cooldown may be
	// answered 429 before the host is blocked for the run.
	maxInRunProbeFailures = 3
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
	// until is the earliest a probe may go out: for a loaded cooldown, the end
	// of its backoff window; for an in-run one (cooling), the end of the wait.
	until time.Time
	// cooling is set when until is an in-run cooldown, which a request waits
	// out (within its budgets) instead of being refused. A loaded cooldown's
	// window is hours long and is never waited on.
	cooling bool
	// fails counts the in-run probes answered 429.
	fails int
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
	// silent is the hosts whose request failed transiently again after its
	// transport retry (Transient, no answer twice): later requests to them are
	// sent once, with no transport retry, until one is answered. It says nothing
	// of a rate limit, so it never refuses or delays a request. Bounded by
	// maxThrottleHosts; past it a host keeps the usual retry.
	silent map[string]struct{}
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

// coolFor is the in-run cooldown after the host's fails-th consecutive 429
// (0 for the first), and false when the host asked for more than a run waits.
// rl is the 429 that ended the request, nil when none is known.
func coolFor(rl *RateLimitedError, fails int) (time.Duration, bool) {
	base := inRunDefaultCool
	if rl != nil && rl.hasRA {
		base = max(rl.ra, inRunMinCool)
	}
	if base > maxInRunCool {
		return 0, false
	}
	return min(base<<min(fails, 8), maxInRunCool), true
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
// open, a probe is due or out, or ctx carries no throttle. A host cooling in
// this run is waited for first (coolWait), so the caller then sends. It sends
// and reserves nothing, so a request made outside DoJSON (a robots.txt read)
// can keep the rule that a blocked host is asked for nothing more this run.
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
	for {
		t.mu.Lock()
		e := t.hosts[host]
		switch {
		case e == nil || e.phase == hostOpen:
			t.mu.Unlock()
			return nil
		case e.phase == hostBlocked:
			why := e.why
			t.mu.Unlock()
			return refusal(host, method, path, why)
		case e.probing == nil && e.cooling && t.clock().Before(e.until):
			// A host cooling in this run is waited for, as a request through
			// admit waits: a board that reads robots.txt first would otherwise
			// be refused here, never send the probe, and leave the host cooling
			// for every board after it. The next request through is the probe.
			wait, deadline := e.until.Sub(t.clock()), e.until
			t.mu.Unlock()
			if err := t.coolWait(ctx, host, method, path, e, wait, deadline); err != nil {
				return err
			}
			continue
		case e.probing == nil && t.clock().Before(e.until):
			why := e.coolingWhy()
			t.mu.Unlock()
			return refusal(host, method, path, why)
		}
		t.mu.Unlock()
		return nil
	}
}

// coolWait waits out the in-run cooldown e had (until deadline, wait from now
// when the caller looked, with t.mu released), charged to ctx's retry budgets,
// and then opens the host to a probe: the next request through admit is it. A
// wait the budgets cannot cover is refused as rate limited, and the host stays
// cooling; ctx ending ends the wait with its error.
func (t *HostThrottle) coolWait(ctx context.Context, host, method, path string, e *hostEntry, wait time.Duration, deadline time.Time) error {
	if ok, spent := spend(ctx, wait); !ok {
		return refusal(host, method, path, fmt.Sprintf("host rate limited earlier this run; cooling for %s more, and the %s's retry budget is spent", wait.Round(time.Second), spent))
	}
	if err := sleep(ctx, wait); err != nil {
		return fmt.Errorf("sources: %s %s: waiting for %s to cool: %w", method, path, host, err)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if cur := t.hosts[host]; cur == e && e.until.Equal(deadline) {
		e.until = time.Time{} // waited: the next request through is the probe
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
		case e.cooling && t.clock().Before(e.until):
			// An in-run cooldown is short: wait it out, charged to the
			// retry budgets, and let this request be the probe. A request the
			// budgets cannot cover is refused and the host stays cooling.
			wait, deadline := e.until.Sub(t.clock()), e.until
			t.mu.Unlock()
			if err := t.coolWait(ctx, host, method, path, e, wait, deadline); err != nil {
				return "", err
			}
			continue
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
	delete(t.silent, host)
	if e := t.hosts[host]; e != nil {
		e.settle()
		e.phase, e.seen, e.prior = hostOpen, sawAnswer, 0
		e.cooling, e.fails, e.until = false, 0, time.Time{}
	}
}

// markSilent records that host did not answer a request or its transport retry.
// heard records an HTTP answer from host that was not a 200 (a 404, a 500, a
// 429): the host is answering, so it is no longer silent. Unlike answered it
// leaves a rate-limit memory as it is.
func (t *HostThrottle) heard(host string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.silent, host)
}

func (t *HostThrottle) markSilent(host string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.silent[host]; ok || len(t.silent) >= maxThrottleHosts {
		return
	}
	if t.silent == nil {
		t.silent = map[string]struct{}{}
	}
	t.silent[host] = struct{}{}
}

// hostSilent reports whether ctx's throttle has host as one that did not answer
// a request and its retry earlier this run.
func hostSilent(ctx context.Context, host string) bool {
	t, _ := ctx.Value(throttleKey{}).(*HostThrottle)
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.silent[host]
	return ok
}

// noteSilent is markSilent on ctx's throttle, if it carries one.
func noteSilent(ctx context.Context, host string) {
	if t, _ := ctx.Value(throttleKey{}).(*HostThrottle); t != nil {
		t.markSilent(host)
	}
}

// limited records a request to host that ended in a 429 after the retries the
// bounds allow. A host with no memory is now in the probe state; a probe that
// was answered 429 blocks the host for the rest of the run.
func (t *HostThrottle) limited(host string, probe bool) { t.limitedBy(host, probe, nil) }

// limitedBy is limited with the 429 that ended the request, whose Retry-After
// sets the host's in-run cooldown (coolFor). A host that was already in
// cooldown when this run began is not given one: it has been rate limited run
// after run, so a probe answered 429 blocks it for the run.
func (t *HostThrottle) limitedBy(host string, probe bool, rl *RateLimitedError) {
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
	at := t.clock()
	e.seen, e.limitedAt = sawLimit, at
	if probe {
		e.settle()
		e.cool(at, rl)
		return
	}
	if e.phase == hostOpen {
		e.phase = hostProbe
		e.until, e.cooling, e.fails = time.Time{}, false, 0
		e.cool(at, rl)
	}
}

// cool sets the host's cooldown after a 429 at at: an in-run one, or the
// block for the rest of the run when the host was in cooldown before the run,
// asked for more than maxInRunCool, or has answered maxInRunProbeFailures
// probes with 429.
func (e *hostEntry) cool(at time.Time, rl *RateLimitedError) {
	d, ok := coolFor(rl, e.fails)
	switch {
	case e.prior > 0 || !ok || e.fails >= maxInRunProbeFailures:
		e.phase, e.cooling = hostBlocked, false
		e.why = e.blockedWhy()
		if e.prior == 0 && !ok {
			e.why = fmt.Sprintf("host asked to retry after %s, over the %s a run waits; probed once", rl.ra.Round(time.Second), maxInRunCool)
		}
	default:
		e.phase, e.cooling = hostProbe, true
		e.until = at.Add(d)
		e.fails++
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
