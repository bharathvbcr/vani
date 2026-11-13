package srcutil

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// clockedWait replaces the retry wait with one that moves clk, so a wait takes
// exactly as long as it says and the throttle's own clock agrees. It returns
// the waits taken.
func clockedWait(t *testing.T, clk *fakeClock) *[]time.Duration {
	t.Helper()
	old := sleep
	var mu sync.Mutex
	var waits []time.Duration
	sleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		clk.advance(d)
		return nil
	}
	t.Cleanup(func() { sleep = old })
	return &waits
}

func retryAfterReply(v string) srctest.Reply {
	return srctest.Reply{Status: 429, Header: http.Header{"Retry-After": {v}}}
}

// coolRun is a run context with the budgets a run has and a throttle on clk.
func coolRun(clk *fakeClock) (context.Context, *HostThrottle) {
	th := NewHostThrottle(nil, clk.now)
	return WithHostThrottle(WithRunRetryBudget(context.Background(), RunRetryBudget), th), th
}

// A host that sent Retry-After is waited for exactly that long (not for the
// default), by the next board, which is then the probe; its 200 reopens the
// host for everyone after it.
func TestInRunCooldownHonoursRetryAfter(t *testing.T) {
	clk := &fakeClock{t: t0}
	waits := clockedWait(t, clk)
	noJitter(t)
	f := newFakeHosts(func(_ string, n int) srctest.Reply {
		if n == 1 { // asks for more than a request waits (30s): it gives up at once
			return retryAfterReply("40")
		}
		return ok200
	})
	run, th := coolRun(clk)
	wantWhy(t, getFrom(boardCtx(run), f, "gh.test"), "")
	before := len(*waits)
	if err := getFrom(boardCtx(run), f, "gh.test"); err != nil {
		t.Fatalf("the probe after the cooldown: %v", err)
	}
	got := *waits
	if len(got) != before+1 || got[before] != 40*time.Second {
		t.Fatalf("waits %v; want the second board to wait the 40s the host asked for", got)
	}
	if f.count("gh.test") != 2 {
		t.Errorf("%d requests, want the 429 and the probe", f.count("gh.test"))
	}
	if err := getFrom(boardCtx(run), f, "gh.test"); err != nil || len(*waits) != before+1 {
		t.Errorf("a reopened host: err %v, waits %v; want no more waiting", err, *waits)
	}
	if len(th.Cooldowns()) != 0 {
		t.Errorf("cooldowns %+v after the host answered", th.Cooldowns())
	}
}

// With no Retry-After the cooldown is the short default, then doubles with
// each probe answered 429 up to the cap, and the third failed probe blocks the
// host for the run. Every probe is a single attempt.
func TestInRunCooldownEscalatesThenBlocks(t *testing.T) {
	clk := &fakeClock{t: t0}
	waits := clockedWait(t, clk)
	noJitter(t)
	f := newFakeHosts(always429)
	run, th := coolRun(clk)
	wantWhy(t, getFrom(boardCtx(run), f, "gh.test"), "")
	first := len(*waits)
	for i, want := range []time.Duration{inRunDefaultCool, 2 * inRunDefaultCool, maxInRunCool} {
		wantWhy(t, getFrom(boardCtx(run), f, "gh.test"), "probed once")
		if got := *waits; len(got) != first+i+1 || got[first+i] != want {
			t.Fatalf("probe %d: waits %v; want %v", i+1, got[first:], want)
		}
	}
	// The board's budget pays the sixth attempt: six, plus three single probes.
	if f.count("gh.test") != 6+3 {
		t.Fatalf("%d requests, want the first board's six and three single probes", f.count("gh.test"))
	}
	n := f.count("gh.test")
	rl := wantWhy(t, getFrom(boardCtx(run), f, "gh.test"), "probed once")
	if !rl.noRequest || f.count("gh.test") != n {
		t.Errorf("after three failed probes the host is blocked for the run: noRequest %v, %d requests", rl.noRequest, f.count("gh.test"))
	}
	if c := th.Cooldowns()["gh.test"]; c.Runs != 1 {
		t.Errorf("cooldowns %+v, want one rate-limited run", th.Cooldowns())
	}
}

// Retry-After: 0 is no reason to ask again at once: the cooldown has a floor.
func TestInRunCooldownHasAFloor(t *testing.T) {
	clk := &fakeClock{t: t0}
	waits := clockedWait(t, clk)
	noJitter(t)
	f := newFakeHosts(func(_ string, n int) srctest.Reply {
		if n <= 8 {
			return retryAfterReply("0")
		}
		return ok200
	})
	run, _ := coolRun(clk)
	wantWhy(t, getFrom(boardCtx(run), f, "gh.test"), "")
	before := len(*waits)
	_ = getFrom(boardCtx(run), f, "gh.test") // the probe: answered 429 again
	if got := *waits; len(got) <= before || got[before] != inRunMinCool {
		t.Fatalf("waits %v; want the floor of %v before the probe", got, inRunMinCool)
	}
}

// A host that asks for longer than a run waits is blocked at once, and a board
// that arrives is refused without a request or a wait.
func TestInRunCooldownOverTheCapBlocksAtOnce(t *testing.T) {
	clk := &fakeClock{t: t0}
	waits := clockedWait(t, clk)
	f := newFakeHosts(func(string, int) srctest.Reply { return retryAfterReply("600") })
	run, _ := coolRun(clk)
	wantWhy(t, getFrom(boardCtx(run), f, "gh.test"), "asked to retry after")
	before, n := len(*waits), f.count("gh.test")
	rl := wantWhy(t, getFrom(boardCtx(run), f, "gh.test"), "asked to retry after 10m0s, over the 2m0s a run waits")
	if !rl.noRequest || f.count("gh.test") != n || len(*waits) != before {
		t.Errorf("noRequest %v, %d requests (was %d), waits %v", rl.noRequest, f.count("gh.test"), n, *waits)
	}
}

// A Retry-After between a request's own cap and the run's is waited out by the
// next board, not by the first one's retries.
func TestInRunCooldownWaitsAHostAskingBeyondARequestsCap(t *testing.T) {
	clk := &fakeClock{t: t0}
	waits := clockedWait(t, clk)
	f := newFakeHosts(func(_ string, n int) srctest.Reply {
		if n == 1 {
			return retryAfterReply("90")
		}
		return ok200
	})
	run, _ := coolRun(clk)
	wantWhy(t, getFrom(boardCtx(run), f, "gh.test"), "asked to retry after 1m30s")
	if len(*waits) != 0 || f.count("gh.test") != 1 {
		t.Fatalf("the first board waited %v over %d requests; a request does not wait past its cap", *waits, f.count("gh.test"))
	}
	if err := getFrom(boardCtx(run), f, "gh.test"); err != nil || len(*waits) != 1 || (*waits)[0] != 90*time.Second {
		t.Fatalf("second board: err %v waits %v; want one wait of 90s then the probe answered", err, *waits)
	}
}

// The wait is charged to the board's and the run's retry budgets: a board that
// cannot pay for it is refused without sleeping, and the host stays cooling for
// the board that can.
func TestInRunCooldownIsChargedToTheBudgets(t *testing.T) {
	clk := &fakeClock{t: t0}
	waits := clockedWait(t, clk)
	noJitter(t)
	f := newFakeHosts(func(_ string, n int) srctest.Reply {
		if n <= 6 {
			return srctest.Reply{Status: 429}
		}
		return ok200
	})
	run, _ := coolRun(clk)
	wantWhy(t, getFrom(boardCtx(run), f, "gh.test"), "")
	before, n := len(*waits), f.count("gh.test")
	poor := WithRetryBudget(run, 10*time.Second)
	rl := wantWhy(t, getFrom(poor, f, "gh.test"), "the board's retry budget is spent")
	if !rl.noRequest || f.count("gh.test") != n || len(*waits) != before {
		t.Fatalf("a board that cannot pay: noRequest %v, %d requests, waits %v", rl.noRequest, f.count("gh.test"), *waits)
	}
	if left, _ := RetryBudgetLeft(poor); left != 10*time.Second {
		t.Errorf("a refused wait was charged: %v left", left)
	}
	before = len(*waits)
	if err := getFrom(boardCtx(run), f, "gh.test"); err != nil || len(*waits) != before+1 {
		t.Errorf("a board that can pay: err %v waits %v", err, *waits)
	}

	// And the run's budget: spent, nothing waits.
	clk2 := &fakeClock{t: t0}
	waits2 := clockedWait(t, clk2)
	f2 := newFakeHosts(always429)
	th := NewHostThrottle(nil, clk2.now)
	tight := WithHostThrottle(WithRunRetryBudget(context.Background(), 70*time.Second), th)
	wantWhy(t, getFrom(boardCtx(tight), f2, "gh.test"), "") // spends 62s of the 70s
	n2, w2 := f2.count("gh.test"), len(*waits2)
	rl = wantWhy(t, getFrom(boardCtx(tight), f2, "gh.test"), "the run's retry budget is spent")
	if !rl.noRequest || f2.count("gh.test") != n2 || len(*waits2) != w2 {
		t.Errorf("run budget spent: noRequest %v, %d requests, waits %v", rl.noRequest, f2.count("gh.test"), *waits2)
	}
}

// Cancelling while a request waits out the cooldown ends it with the context's
// error, not a rate limit, and the host is still cooling for the next request.
func TestInRunCooldownWaitEndsWithTheContext(t *testing.T) {
	clk := &fakeClock{t: t0}
	clockedWait(t, clk)
	noJitter(t)
	f := newFakeHosts(always429)
	run, th := coolRun(clk)
	wantWhy(t, getFrom(boardCtx(run), f, "gh.test"), "")
	n := f.count("gh.test")
	ctx, cancel := context.WithCancel(boardCtx(run))
	cancel()
	err := getFrom(ctx, f, "gh.test")
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrRateLimited) || f.count("gh.test") != n {
		t.Fatalf("err %v, %d requests (was %d); want the context's cancellation and nothing sent", err, f.count("gh.test"), n)
	}
	if e := th.hosts["gh.test"]; e == nil || !e.cooling || !clk.now().Before(e.until) {
		t.Errorf("the cooldown was lost to a cancelled request: %+v", e)
	}
}

// Many requests arriving while the host cools wait together and send one
// probe in all.
func TestInRunCooldownConcurrentRequestsShareOneProbe(t *testing.T) {
	clk := &fakeClock{t: t0}
	clockedWait(t, clk)
	noJitter(t)
	f := newFakeHosts(func(_ string, n int) srctest.Reply {
		if n <= 6 {
			return srctest.Reply{Status: 429}
		}
		return ok200
	})
	run, _ := coolRun(clk)
	wantWhy(t, getFrom(boardCtx(run), f, "gh.test"), "")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := getFrom(boardCtx(run), f, "gh.test"); err != nil {
				t.Errorf("a request that waited for the probe: %v", err)
			}
		}()
	}
	wg.Wait()
	// One probe (request 7), then the other fifteen go out as an open host's.
	if n := f.count("gh.test"); n != 6+16 {
		t.Errorf("%d requests, want the first board's six and one each for the 16 callers", n)
	}
}

// A robots.txt read (outside the retry loop) waits out the cooldown as a
// request does, so the board that reads it first still reaches the probe: it
// is let through after the wait, the wait is charged to the budgets, and the
// next request through is the probe.
func TestHostBlockedWaitsForACoolingHost(t *testing.T) {
	clk := &fakeClock{t: t0}
	waits := clockedWait(t, clk)
	noJitter(t)
	f := newFakeHosts(func(_ string, n int) srctest.Reply {
		if n <= 6 {
			return srctest.Reply{Status: 429}
		}
		return ok200
	})
	run, th := coolRun(clk)
	wantWhy(t, getFrom(boardCtx(run), f, "gh.test"), "")
	before := len(*waits)
	board := boardCtx(run)
	if err := HostBlocked(board, "gh.test", "GET", "https://gh.test/robots.txt"); err != nil {
		t.Fatalf("HostBlocked while cooling: %v, want it to wait and let the read through", err)
	}
	if got := *waits; len(got) != before+1 || got[before] != inRunDefaultCool {
		t.Fatalf("waits %v, want one of %v", got, inRunDefaultCool)
	}
	if left, _ := RetryBudgetLeft(board); left != BoardRetryBudget-inRunDefaultCool {
		t.Errorf("board budget left %v; the wait was not charged", left)
	}
	// Nothing was reserved: the host is due its probe, and the request that
	// follows the robots.txt read is it, sent once with no retries.
	if e := th.hosts["gh.test"]; e == nil || e.probing != nil || e.phase != hostProbe {
		t.Fatalf("host state after the wait: %+v", e)
	}
	if err := getFrom(board, f, "gh.test"); err != nil || f.count("gh.test") != 7 || len(*waits) != before+1 {
		t.Errorf("the probe after the robots.txt read: err %v, %d requests, waits %v", err, f.count("gh.test"), *waits)
	}
	// A board that cannot pay for the wait is refused without waiting, and the
	// host stays cooling for one that can.
	wantWhy(t, getFrom(boardCtx(run), newFakeHosts(always429), "other.test"), "") // another host cooling
	err := HostBlocked(WithRetryBudget(run, time.Second), "other.test", "GET", "https://other.test/robots.txt")
	var rl *RateLimitedError
	if !errors.As(err, &rl) || !strings.Contains(rl.Why, "retry budget is spent") || !rl.noRequest {
		t.Errorf("a board that cannot pay: %v", err)
	}
	if err := HostBlocked(WithRetryBudget(run, time.Minute), "other.test", "GET", "https://other.test/robots.txt"); err != nil {
		t.Errorf("a board that can pay: %v", err)
	}
	// Cancelled while waiting: the context's error, not a rate limit.
	wantWhy(t, getFrom(boardCtx(run), newFakeHosts(always429), "third.test"), "")
	ctx, cancel := context.WithCancel(boardCtx(run))
	cancel()
	if err := HostBlocked(ctx, "third.test", "GET", "https://third.test/robots.txt"); !errors.Is(err, context.Canceled) || errors.Is(err, ErrRateLimited) {
		t.Errorf("cancelled while waiting: %v", err)
	}
}

// A host already in cooldown when the run began is not given in-run cooldowns:
// it has been limited run after run, so its failed probe blocks it at once.
func TestInRunCooldownNeverAppliesToAHostAlreadyInCooldown(t *testing.T) {
	clk := &fakeClock{t: t0}
	waits := clockedWait(t, clk)
	f := newFakeHosts(always429)
	th := NewHostThrottle(map[string]HostCooldown{"gh.test": {Runs: 2, Last: t0.Add(-24 * time.Hour)}}, clk.now)
	run := WithHostThrottle(WithRunRetryBudget(context.Background(), RunRetryBudget), th)
	wantWhy(t, getFrom(boardCtx(run), f, "gh.test"), "host in cooldown after 3 rate-limited runs; probed once")
	rl := wantWhy(t, getFrom(boardCtx(run), f, "gh.test"), "probed once")
	if !rl.noRequest || f.count("gh.test") != 1 || len(*waits) != 0 {
		t.Errorf("noRequest %v, %d requests, waits %v", rl.noRequest, f.count("gh.test"), *waits)
	}
}

// coolFor: the Retry-After wins over the default, has a floor, doubles with
// each failure up to the cap, and refuses what is over the cap.
func TestCoolFor(t *testing.T) {
	ra := func(d time.Duration) *RateLimitedError { return &RateLimitedError{ra: d, hasRA: true} }
	for name, tc := range map[string]struct {
		rl    *RateLimitedError
		fails int
		want  time.Duration
		ok    bool
	}{
		"no 429 known":    {nil, 0, inRunDefaultCool, true},
		"no retry-after":  {&RateLimitedError{}, 0, inRunDefaultCool, true},
		"retry-after":     {ra(10 * time.Second), 0, 10 * time.Second, true},
		"zero":            {ra(0), 0, inRunMinCool, true},
		"doubles":         {ra(10 * time.Second), 2, 40 * time.Second, true},
		"default doubles": {nil, 1, 2 * inRunDefaultCool, true},
		"capped":          {nil, 7, maxInRunCool, true},
		"at the cap":      {ra(maxInRunCool), 0, maxInRunCool, true},
		"over the cap":    {ra(maxInRunCool + time.Second), 0, 0, false},
		"an hour":         {ra(time.Hour), 0, 0, false},
		"huge fail count": {nil, 1 << 20, maxInRunCool, true},
	} {
		got, ok := coolFor(tc.rl, tc.fails)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: coolFor = %v, %v; want %v, %v", name, got, ok, tc.want, tc.ok)
		}
	}
}
