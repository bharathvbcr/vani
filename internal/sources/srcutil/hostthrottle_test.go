package srcutil

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

var t0 = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeHosts answers every request through h(host, n), n being how many requests
// that host has had including this one, and counts them per host.
type fakeHosts struct {
	*srctest.Doer
	mu sync.Mutex
	n  map[string]int
}

func newFakeHosts(h func(host string, n int) srctest.Reply) *fakeHosts {
	f := &fakeHosts{n: map[string]int{}}
	f.Doer = srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		u, _ := url.Parse(r.URL)
		f.mu.Lock()
		f.n[u.Hostname()]++
		n := f.n[u.Hostname()]
		f.mu.Unlock()
		return h(u.Hostname(), n), true
	})
	return f
}

func (f *fakeHosts) count(host string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n[host]
}

var (
	always429 = func(string, int) srctest.Reply { return srctest.Reply{Status: 429} }
	ok200     = srctest.Reply{Status: 200, Body: []byte(`{"A":1}`)}
)

func hostURL(host string) string { return "https://" + host + "/x" }

func getFrom(ctx context.Context, f *fakeHosts, host string) error {
	var v struct{ A int }
	return GetJSON(ctx, f, hostURL(host), &v)
}

// boardCtx is a board's context: its own retry budget under the run's.
func boardCtx(run context.Context) context.Context { return WithRetryBudget(run, BoardRetryBudget) }

func wantWhy(t *testing.T, err error, sub string) *RateLimitedError {
	t.Helper()
	var rl *RateLimitedError
	if !errors.Is(err, ErrRateLimited) || !errors.As(err, &rl) || !strings.Contains(rl.Why, sub) {
		t.Fatalf("err = %v, want rate limited with %q", err, sub)
	}
	return rl
}

// Gap 1. Many boards on different hosts all throttle: the run as a whole waits
// at most RunRetryBudget, and the boards after that give up without sleeping.
func TestRunRetryBudgetCapsWaitingAcrossBoards(t *testing.T) {
	waits := noWait(t)
	noJitter(t)
	f := newFakeHosts(always429)
	run := WithRunRetryBudget(context.Background(), RunRetryBudget)
	const boards = 40 // each would wait 2+4+8+16 s on its own: 40 x 30 s is 20 min
	var spent, failFast int
	for i := 0; i < boards; i++ {
		before := len(*waits)
		err := getFrom(boardCtx(run), f, fmt.Sprintf("h%d.test", i))
		rl := wantWhy(t, err, "")
		if strings.Contains(rl.Why, "the run's retry budget is spent") {
			failFast++
			if len(*waits) != before {
				t.Errorf("board %d slept after the run's budget was spent", i)
			}
		}
		spent = 0
		for _, w := range *waits {
			spent += int(w)
		}
	}
	if time.Duration(spent) > RunRetryBudget {
		t.Errorf("run waited %v, over its %v budget", time.Duration(spent), RunRetryBudget)
	}
	if failFast == 0 {
		t.Errorf("no board was refused for the run's budget; waits %v", *waits)
	}
	if left, ok := RunRetryBudgetLeft(run); !ok || left < 0 || left > 30*time.Second {
		t.Errorf("run budget left %v (%v), want it nearly all spent", left, ok)
	}
}

// A 503 refused for the run's budget is the plain status, as before: only a 429
// is "rate limited".
func TestRunRetryBudgetRefusesA503AsUnavailable(t *testing.T) {
	waits := noWait(t)
	f := newFakeHosts(func(string, int) srctest.Reply { return srctest.Reply{Status: 503} })
	run := WithRunRetryBudget(context.Background(), 0)
	err := getFrom(boardCtx(run), f, "a.test")
	if err == nil || errors.Is(err, ErrRateLimited) || !strings.Contains(err.Error(), "the run's retry budget is spent") || len(*waits) != 0 || f.count("a.test") != 1 {
		t.Fatalf("err %v waits %v requests %d", err, *waits, f.count("a.test"))
	}
}

// A wait is charged to the board and the run together or to neither: a board
// that cannot cover it does not draw down the run.
func TestRetryBudgetsChargeAllLevelsOrNone(t *testing.T) {
	run := WithRunRetryBudget(context.Background(), time.Minute)
	board := WithRetryBudget(run, 5*time.Second)
	if ok, _ := spend(board, 3*time.Second); !ok {
		t.Fatal("3s refused")
	}
	if ok, by := spend(board, 3*time.Second); ok || by != "board" {
		t.Fatalf("second 3s: ok %v by %q, want refused by the board", ok, by)
	}
	if left, _ := RunRetryBudgetLeft(run); left != 57*time.Second {
		t.Errorf("run left %v, want 57s: the refused wait was charged", left)
	}
	if left, _ := RetryBudgetLeft(board); left != 2*time.Second {
		t.Errorf("board left %v, want 2s", left)
	}
	tight := WithRunRetryBudget(context.Background(), time.Second)
	if ok, by := spend(WithRetryBudget(tight, time.Hour), 2*time.Second); ok || by != "run" {
		t.Errorf("ok %v by %q, want refused by the run", ok, by)
	}
	if _, ok := RunRetryBudgetLeft(context.Background()); ok {
		t.Error("a context with no run budget reported one")
	}
}

// Boards spending at once cannot overdraw the run: every grant is paid for.
func TestRunRetryBudgetConcurrentSpend(t *testing.T) {
	run := WithRunRetryBudget(context.Background(), time.Second)
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := spend(WithRetryBudget(run, time.Minute), 100*time.Millisecond); ok {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if left, _ := RunRetryBudgetLeft(run); granted != 10 || left != 0 {
		t.Errorf("granted %d, left %v; want 10 grants of 100ms from 1s and nothing left", granted, left)
	}
}

// Gap 2a. A host that ended rate limited is asked once more by the next board,
// without retries, and not at all after that.
func TestHostThrottleSecondBoardProbesOnceThenBlocks(t *testing.T) {
	waits := noWait(t)
	noJitter(t)
	f := newFakeHosts(always429)
	th := NewHostThrottle(nil, nil)
	run := WithHostThrottle(WithRunRetryBudget(context.Background(), RunRetryBudget), th)

	wantWhy(t, getFrom(boardCtx(run), f, "ms.test"), "gave up after 5 attempts")
	if f.count("ms.test") != 5 || len(*waits) != 4 {
		t.Fatalf("first board: %d requests, waits %v", f.count("ms.test"), *waits)
	}
	spentBefore := len(*waits)

	wantWhy(t, getFrom(boardCtx(run), f, "ms.test"), "host rate limited earlier this run; probed once")
	if f.count("ms.test") != 6 || len(*waits) != spentBefore {
		t.Errorf("second board: %d requests in all, waits %v; want one probe and no waiting", f.count("ms.test"), *waits)
	}
	rl := wantWhy(t, getFrom(boardCtx(run), f, "ms.test"), "host rate limited earlier this run; probed once")
	if f.count("ms.test") != 6 || len(*waits) != spentBefore || !rl.noRequest {
		t.Errorf("third board: %d requests in all; want none sent", f.count("ms.test"))
	}
	// Other hosts are unaffected.
	if err := getFrom(boardCtx(run), newFakeHosts(func(string, int) srctest.Reply { return ok200 }), "other.test"); err != nil {
		t.Errorf("other host: %v", err)
	}
	if c := th.Cooldowns()["ms.test"]; c.Runs != 1 {
		t.Errorf("cooldowns %+v, want one run for ms.test", th.Cooldowns())
	}
}

// The probe's 200 reopens the host: the board after it retries as usual, and
// the host leaves the cooldowns.
func TestHostThrottleRecoveryClearsAndRestoresRetries(t *testing.T) {
	waits := noWait(t)
	noJitter(t)
	f := newFakeHosts(func(_ string, n int) srctest.Reply {
		if n <= 5 || n == 7 { // down for the first board, a 429 then 200 for the third
			return srctest.Reply{Status: 429}
		}
		return ok200
	})
	th := NewHostThrottle(nil, nil)
	run := WithHostThrottle(context.Background(), th)
	wantWhy(t, getFrom(run, f, "ms.test"), "")
	if err := getFrom(run, f, "ms.test"); err != nil { // request 6: the probe, answered
		t.Fatalf("probe: %v", err)
	}
	if len(th.Cooldowns()) != 0 {
		t.Errorf("cooldowns after recovery: %+v", th.Cooldowns())
	}
	before := len(*waits)
	if err := getFrom(run, f, "ms.test"); err != nil { // request 7 is 429: retried, then 8 is 200
		t.Fatalf("after recovery: %v", err)
	}
	if len(*waits) != before+1 || f.count("ms.test") != 8 {
		t.Errorf("a reopened host was not retried: waits %v, %d requests", *waits, f.count("ms.test"))
	}
}

// A cooldown from earlier runs: the first request is one probe with no retries;
// its 429 ends the host for the run, with the cooldown message, and the count
// goes up by one. Nothing waits.
func TestHostThrottleCooldownProbesOnceAcrossRuns(t *testing.T) {
	waits := noWait(t)
	clk := &fakeClock{t: t0}
	f := newFakeHosts(always429)
	th := NewHostThrottle(map[string]HostCooldown{"ms.test": {Runs: 2, Last: t0.Add(-24 * time.Hour)}}, clk.now)
	run := WithHostThrottle(context.Background(), th)

	wantWhy(t, getFrom(boardCtx(run), f, "ms.test"), "host in cooldown after 3 rate-limited runs; probed once")
	wantWhy(t, getFrom(boardCtx(run), f, "ms.test"), "host in cooldown after 3 rate-limited runs; probed once")
	if f.count("ms.test") != 1 || len(*waits) != 0 {
		t.Fatalf("%d requests, waits %v; want one probe and no waiting", f.count("ms.test"), *waits)
	}
	if c := th.Cooldowns()["ms.test"]; c.Runs != 3 || !c.Last.Equal(t0) {
		t.Errorf("cooldown %+v, want 3 runs at %v", c, t0)
	}
}

// A cooldown whose probe is answered is cleared; the next requests retry as usual.
func TestHostThrottleCooldownClearedBySuccess(t *testing.T) {
	waits := noWait(t)
	f := newFakeHosts(func(_ string, n int) srctest.Reply {
		if n == 2 {
			return srctest.Reply{Status: 429}
		}
		return ok200
	})
	th := NewHostThrottle(map[string]HostCooldown{"ms.test": {Runs: 4, Last: t0.Add(-time.Hour * 24)}}, (&fakeClock{t: t0}).now)
	run := WithHostThrottle(context.Background(), th)
	if err := getFrom(run, f, "ms.test"); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(th.Cooldowns()) != 0 {
		t.Errorf("cooldown kept after a success: %+v", th.Cooldowns())
	}
	// Request 2 is a 429: a host that has answered is a bursty one, not a
	// blocked one, so it gets the usual retry (request 3 is a 200).
	if err := getFrom(run, f, "ms.test"); err != nil || len(*waits) != 1 {
		t.Errorf("after success: err %v waits %v, want one retry wait", err, *waits)
	}
}

// The backoff window: a host whose cooldown was just refreshed is left alone
// (no request), one whose window has passed is probed. The window grows with
// the count, is bounded, and the first rate-limited run earns none.
func TestHostThrottleBackoffWindow(t *testing.T) {
	noWait(t)
	for runs, want := range map[int]time.Duration{1: 0, 2: 15 * time.Minute, 3: 30 * time.Minute, 4: time.Hour, 5: 2 * time.Hour,
		6: 4 * time.Hour, 7: maxProbeGap, 8: maxProbeGap, 99: maxProbeGap} {
		if got := probeGap(runs); got != want {
			t.Errorf("probeGap(%d) = %v, want %v", runs, got, want)
		}
	}
	clk := &fakeClock{t: t0}
	f := newFakeHosts(always429)
	prior := map[string]HostCooldown{"ms.test": {Runs: 3, Last: t0.Add(-10 * time.Minute)}} // window 30m
	th := NewHostThrottle(prior, clk.now)
	run := WithHostThrottle(context.Background(), th)
	rl := wantWhy(t, getFrom(run, f, "ms.test"), "host in cooldown after 3 rate-limited runs; not probed until 2026-10-22 12:20 UTC")
	if f.count("ms.test") != 0 || !rl.noRequest {
		t.Errorf("a host inside its window was asked: %d requests", f.count("ms.test"))
	}
	// A skipped host learned nothing: its cooldown is exactly as it was.
	if got := th.Cooldowns()["ms.test"]; got != prior["ms.test"] {
		t.Errorf("cooldown changed without a request: %+v, was %+v", got, prior["ms.test"])
	}
	// Past the window it is probed.
	th = NewHostThrottle(prior, (&fakeClock{t: t0.Add(21 * time.Minute)}).now)
	wantWhy(t, getFrom(WithHostThrottle(context.Background(), th), f, "ms.test"), "probed once")
	if f.count("ms.test") != 1 {
		t.Errorf("past its window: %d requests, want one probe", f.count("ms.test"))
	}
}

// A cooldown stamped in the future (the clock was set back) cannot say how long
// ago it was: it is probed rather than waited out, however many runs it counts.
func TestHostThrottleFutureStampIsProbed(t *testing.T) {
	noWait(t)
	f := newFakeHosts(always429)
	th := NewHostThrottle(map[string]HostCooldown{"ms.test": {Runs: 9, Last: t0.Add(72 * time.Hour)}}, (&fakeClock{t: t0}).now)
	wantWhy(t, getFrom(WithHostThrottle(context.Background(), th), f, "ms.test"), "host in cooldown after 10 rate-limited runs; probed once")
	if f.count("ms.test") != 1 {
		t.Errorf("%d requests, want one probe", f.count("ms.test"))
	}
}

// A request that fails or is cancelled during the probe says nothing about the
// host: it stays a probe, and the next request is the one that is sent.
func TestHostThrottleInconclusiveProbeIsRetried(t *testing.T) {
	noWait(t)
	f := newFakeHosts(func(_ string, n int) srctest.Reply {
		if n == 1 {
			return srctest.Reply{Status: 503}
		}
		return ok200
	})
	th := NewHostThrottle(map[string]HostCooldown{"ms.test": {Runs: 1, Last: t0.Add(-time.Hour)}}, (&fakeClock{t: t0}).now)
	run := WithHostThrottle(context.Background(), th)
	err := getFrom(run, f, "ms.test")
	if err == nil || errors.Is(err, ErrRateLimited) || f.count("ms.test") != 1 || !strings.Contains(err.Error(), "one attempt, no retries") {
		t.Fatalf("503 probe: err %v requests %d; want one attempt, a plain 503", err, f.count("ms.test"))
	}
	if err := getFrom(run, f, "ms.test"); err != nil {
		t.Errorf("next request after an inconclusive probe: %v", err)
	}
	if len(th.Cooldowns()) != 0 {
		t.Errorf("cooldowns %+v after the host answered", th.Cooldowns())
	}
}

// A request waiting for the probe ends with its own context, and the probe
// is not disturbed; cancelling the probe itself wakes the waiter, which then
// becomes the probe.
func TestHostThrottleCancellationWhileProbing(t *testing.T) {
	noWait(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	var mu sync.Mutex
	calls := 0
	d := srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		entered <- struct{}{}
		if n == 1 {
			<-release
		}
		return ok200, true
	})
	prior := map[string]HostCooldown{"ms.test": {Runs: 1, Last: t0.Add(-time.Hour)}}
	th := NewHostThrottle(prior, (&fakeClock{t: t0}).now)
	run := WithHostThrottle(context.Background(), th)
	var v struct{ A int }

	probeDone := make(chan error, 1)
	go func() { probeDone <- GetJSON(run, d, hostURL("ms.test"), &v) }()
	<-entered // the probe is out

	waiterCtx, cancel := context.WithCancel(run)
	waiterDone := make(chan error, 1)
	go func() { waiterDone <- GetJSON(waiterCtx, d, hostURL("ms.test"), &v) }()
	cancel()
	if err := <-waiterDone; !errors.Is(err, context.Canceled) || errors.Is(err, ErrRateLimited) {
		t.Fatalf("waiter: %v, want the context's cancellation only", err)
	}
	mu.Lock()
	n := calls
	mu.Unlock()
	if n != 1 {
		t.Fatalf("%d requests while the probe was out, want 1", n)
	}
	close(release)
	if err := <-probeDone; err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(th.Cooldowns()) != 0 {
		t.Errorf("cooldowns %+v after the probe was answered", th.Cooldowns())
	}
}

func TestHostThrottleCancelledProbeWakesWaiter(t *testing.T) {
	noWait(t)
	probeCtx, cancelProbe := context.WithCancel(context.Background())
	entered := make(chan struct{}, 4)
	var mu sync.Mutex
	calls := 0
	d := srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		entered <- struct{}{}
		if n == 1 {
			<-probeCtx.Done()
			return srctest.Reply{}, false // the transport fails: the probe is cancelled
		}
		return ok200, true
	})
	th := NewHostThrottle(map[string]HostCooldown{"ms.test": {Runs: 1, Last: t0.Add(-time.Hour)}}, (&fakeClock{t: t0}).now)
	var v struct{ A int }
	probeDone := make(chan error, 1)
	go func() {
		probeDone <- GetJSON(WithHostThrottle(probeCtx, th), d, hostURL("ms.test"), &v)
	}()
	<-entered
	waiterDone := make(chan error, 1)
	go func() { waiterDone <- GetJSON(WithHostThrottle(context.Background(), th), d, hostURL("ms.test"), &v) }()
	cancelProbe()
	if err := <-probeDone; err == nil {
		t.Fatal("cancelled probe succeeded")
	}
	select {
	case err := <-waiterDone:
		if err != nil {
			t.Errorf("waiter after a cancelled probe: %v, want it sent and answered", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter was never woken by the cancelled probe")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("%d requests, want the cancelled probe and the waiter's", calls)
	}
}

// Many requests at once on a throttled host send one probe in all.
func TestHostThrottleConcurrentRequestsShareOneProbe(t *testing.T) {
	noWait(t)
	f := newFakeHosts(always429)
	th := NewHostThrottle(map[string]HostCooldown{"ms.test": {Runs: 1, Last: t0.Add(-time.Hour)}}, (&fakeClock{t: t0}).now)
	run := WithHostThrottle(context.Background(), th)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := getFrom(run, f, "ms.test"); !errors.Is(err, ErrRateLimited) {
				t.Errorf("err = %v, want rate limited", err)
			}
		}()
	}
	wg.Wait()
	if f.count("ms.test") != 1 {
		t.Errorf("%d requests from 32 callers, want one probe", f.count("ms.test"))
	}
}

// A request already retrying when another request finds the host throttled
// stops retrying instead of spending its budget on a host known to be blocked.
func TestHostThrottleStopsRetriesOnceHostIsKnownLimited(t *testing.T) {
	waits := noWait(t)
	th := NewHostThrottle(nil, nil)
	run := WithHostThrottle(context.Background(), th)
	old := sleep
	t.Cleanup(func() { sleep = old })
	sleep = func(ctx context.Context, d time.Duration) error {
		*waits = append(*waits, d)
		th.limited("ms.test", false) // another request's retries ran out meanwhile
		return ctx.Err()
	}
	f := newFakeHosts(always429)
	wantWhy(t, getFrom(run, f, "ms.test"), "host rate limited earlier this run")
	if f.count("ms.test") != 2 || len(*waits) != 1 {
		t.Errorf("%d requests, waits %v; want to stop after the first wait", f.count("ms.test"), *waits)
	}
}

// More throttled hosts than the throttle tracks: the cooldowns stay bounded and
// the hosts beyond it keep the usual retries.
func TestHostThrottleManyHostsStayBounded(t *testing.T) {
	waits := noWait(t)
	f := newFakeHosts(always429)
	th := NewHostThrottle(nil, nil)
	run := WithHostThrottle(context.Background(), th)
	for i := 0; i < 3*maxThrottleHosts; i++ {
		wantWhy(t, getFrom(run, f, fmt.Sprintf("h%d.test", i)), "")
	}
	if got := len(th.Cooldowns()); got != maxThrottleHosts {
		t.Errorf("%d cooldowns, want the %d tracked", got, maxThrottleHosts)
	}
	if want := 3 * maxThrottleHosts * 4; len(*waits) != want {
		t.Errorf("%d waits, want %d: an untracked host must still be retried", len(*waits), want)
	}
	// Seeding with more than the bound keeps the bound.
	big := map[string]HostCooldown{}
	for i := 0; i < 500; i++ {
		big[fmt.Sprintf("s%d.test", i)] = HostCooldown{Runs: 1, Last: t0}
	}
	if got := len(NewHostThrottle(big, (&fakeClock{t: t0}).now).Cooldowns()); got != maxThrottleHosts {
		t.Errorf("seeded with 500: %d cooldowns, want %d", got, maxThrottleHosts)
	}
}

// Host names differ in case only: they are one host.
func TestHostThrottleHostCaseFolds(t *testing.T) {
	noWait(t)
	f := newFakeHosts(always429)
	th := NewHostThrottle(nil, nil)
	run := WithHostThrottle(context.Background(), th)
	wantWhy(t, getFrom(run, f, "MS.test"), "")
	wantWhy(t, getFrom(run, f, "ms.TEST"), "probed once")
	wantWhy(t, getFrom(run, f, "Ms.Test"), "probed once")
	if n := f.count("MS.test") + f.count("ms.TEST") + f.count("Ms.Test"); n != 6 {
		t.Errorf("%d requests, want 5 and 1 probe", n)
	}
}

// Without a throttle nothing changes: every request retries on its own.
func TestNoHostThrottleLeavesRetriesAlone(t *testing.T) {
	waits := noWait(t)
	f := newFakeHosts(always429)
	for i := 0; i < 2; i++ {
		wantWhy(t, getFrom(context.Background(), f, "ms.test"), "gave up after 5 attempts")
	}
	if f.count("ms.test") != 10 || len(*waits) != 8 {
		t.Errorf("%d requests, waits %v", f.count("ms.test"), *waits)
	}
}

// HostBlocked is the throttle's answer for a request made outside DoJSON (a
// robots.txt read): refused for a host that is blocked or inside its backoff
// window, sent for one that is open or due its probe, and it reserves nothing.
func TestHostBlocked(t *testing.T) {
	noWait(t)
	const url = "https://ms.test/robots.txt"
	// No throttle on the context, and no memory of the host.
	if err := HostBlocked(context.Background(), "ms.test", "GET", url); err != nil {
		t.Errorf("no throttle: %v", err)
	}
	clk := &fakeClock{t: t0}
	th := NewHostThrottle(nil, clk.now)
	run := WithHostThrottle(context.Background(), th)
	if err := HostBlocked(run, "ms.test", "GET", url); err != nil {
		t.Errorf("open host: %v", err)
	}

	// Inside a cooldown's window: refused, with the window in the message, no
	// request sent, and the host's memory untouched.
	prior := map[string]HostCooldown{"ms.test": {Runs: 3, Last: t0.Add(-10 * time.Minute)}}
	th = NewHostThrottle(prior, clk.now)
	run = WithHostThrottle(context.Background(), th)
	err := HostBlocked(run, "MS.test", "GET", url)
	var rl *RateLimitedError
	if !errors.Is(err, ErrRateLimited) || !errors.As(err, &rl) || !rl.noRequest || rl.Path != "/robots.txt" ||
		!strings.Contains(rl.Why, "not probed until 2026-10-22 12:20 UTC") {
		t.Errorf("inside the window: %v", err)
	}
	if got := th.Cooldowns()["ms.test"]; got != prior["ms.test"] {
		t.Errorf("cooldown changed by a check: %+v", got)
	}

	// Past the window the host is due its probe: the check lets a request go and
	// takes nothing from it, so the next request is still the probe.
	late := &fakeClock{t: t0.Add(21 * time.Minute)}
	th = NewHostThrottle(prior, late.now)
	run = WithHostThrottle(context.Background(), th)
	if err := HostBlocked(run, "ms.test", "GET", url); err != nil {
		t.Errorf("due its probe: %v", err)
	}
	f := newFakeHosts(always429)
	wantWhy(t, getFrom(run, f, "ms.test"), "probed once")
	if f.count("ms.test") != 1 {
		t.Errorf("%d requests after the check, want the one probe", f.count("ms.test"))
	}
	// The probe's 429 blocked the host: now the check refuses.
	if err := HostBlocked(run, "ms.test", "GET", url); !errors.Is(err, ErrRateLimited) || !strings.Contains(err.Error(), "probed once") {
		t.Errorf("blocked host: %v", err)
	}
	// Other hosts are unaffected.
	if err := HostBlocked(run, "other.test", "GET", "https://other.test/robots.txt"); err != nil {
		t.Errorf("other host: %v", err)
	}
}
