package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
)

func la(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	return loc
}

func TestNextRunDST(t *testing.T) {
	loc := la(t)
	at := func(y int, m time.Month, d, h, mi int) time.Time { return time.Date(y, m, d, h, mi, 0, 0, loc) }

	// Ordinary day, and strictly-after semantics.
	got := NextRun(at(2026, 10, 18, 7, 0), 8, 30, loc)
	if !got.Equal(at(2026, 10, 18, 8, 30)) {
		t.Errorf("same-day next: %v", got)
	}
	if got := NextRun(at(2026, 10, 18, 8, 30), 8, 30, loc); !got.Equal(at(2026, 10, 19, 8, 30)) {
		t.Errorf("exact slot must advance a day: %v", got)
	}

	// Spring forward 2026-03-08: 08:30 stays 08:30 wall time, 23h after the previous slot.
	prev := at(2026, 3, 7, 8, 30)
	got = NextRun(prev, 8, 30, loc)
	if !got.Equal(at(2026, 3, 8, 8, 30)) || got.Sub(prev) != 23*time.Hour {
		t.Errorf("spring-forward 08:30: %v (delta %v)", got, got.Sub(prev))
	}
	// 02:30 does not exist on 2026-03-08: runs at the first instant after the gap, once.
	got = NextRun(at(2026, 3, 7, 12, 0), 2, 30, loc)
	if h, m, _ := got.In(loc).Clock(); !got.Equal(at(2026, 3, 8, 3, 0)) || h != 3 || m != 0 {
		t.Errorf("gap slot: %v", got)
	}
	if next := NextRun(got, 2, 30, loc); !next.Equal(at(2026, 3, 9, 2, 30)) {
		t.Errorf("after gap run: %v", next)
	}

	// Fall back 2026-11-01: 08:30 is 25h after the previous slot.
	prev = at(2026, 10, 31, 8, 30)
	got = NextRun(prev, 8, 30, loc)
	if !got.Equal(at(2026, 11, 1, 8, 30)) || got.Sub(prev) != 25*time.Hour {
		t.Errorf("fall-back 08:30: %v (delta %v)", got, got.Sub(prev))
	}
	// 01:30 happens twice on 2026-11-01: exactly one run that day.
	first := NextRun(at(2026, 10, 31, 12, 0), 1, 30, loc)
	if y, m, d := first.In(loc).Date(); y != 2026 || m != time.November || d != 1 {
		t.Fatalf("ambiguous slot on wrong day: %v", first)
	}
	next := NextRun(first, 1, 30, loc)
	if y, m, d := next.In(loc).Date(); y != 2026 || m != time.November || d != 2 {
		t.Errorf("repeated hour fired twice: %v", next)
	}
	// A cursor in the second 01:30 (after the first run) also advances to Nov 2.
	second := first.Add(time.Hour)
	if n := NextRun(second, 1, 30, loc); n.In(loc).Day() != 2 {
		t.Errorf("second 01:30 re-fired: %v", n)
	}
}

func TestPrevRun(t *testing.T) {
	loc := la(t)
	at := func(y int, m time.Month, d, h, mi int) time.Time { return time.Date(y, m, d, h, mi, 0, 0, loc) }
	cases := []struct {
		name       string
		now, want  time.Time
		hour, mins int
	}{
		{"before today's slot", at(2026, 10, 18, 7, 0), at(2026, 10, 17, 8, 30), 8, 30},
		{"after today's slot", at(2026, 10, 18, 9, 0), at(2026, 10, 18, 8, 30), 8, 30},
		{"exactly on the slot counts as passed", at(2026, 10, 18, 8, 30), at(2026, 10, 18, 8, 30), 8, 30},
		{"a minute before the slot", at(2026, 10, 18, 8, 29), at(2026, 10, 17, 8, 30), 8, 30},
		{"spring-forward day, slot in the gap", at(2026, 3, 8, 12, 0), at(2026, 3, 8, 3, 0), 2, 30},
		{"day after the gap", at(2026, 3, 9, 1, 0), at(2026, 3, 8, 3, 0), 2, 30},
		{"fall-back day, second 01:30 is not a slot", at(2026, 11, 1, 1, 30).Add(time.Hour), at(2026, 11, 1, 1, 30), 1, 30},
		{"a month idle", at(2026, 12, 20, 6, 0), at(2026, 12, 19, 8, 30), 8, 30},
	}
	for _, c := range cases {
		if got := PrevRun(c.now, c.hour, c.mins, loc); !got.Equal(c.want) {
			t.Errorf("%s: PrevRun(%v) = %v, want %v", c.name, c.now, got, c.want)
		}
		// The slot found and the next one bracket the instant.
		got := PrevRun(c.now, c.hour, c.mins, loc)
		if got.After(c.now) || !NextRun(got, c.hour, c.mins, loc).After(c.now) {
			t.Errorf("%s: %v does not bracket %v", c.name, got, c.now)
		}
	}
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
	reg     chan struct{}
}

func newClock(now time.Time) *fakeClock {
	return &fakeClock{now: now, reg: make(chan struct{}, 1000)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, waiter{c.now.Add(d), ch})
	c.mu.Unlock()
	c.reg <- struct{}{}
	return ch
}

func (c *fakeClock) Advance(to time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = to
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(to) {
			w.ch <- to
		} else {
			kept = append(kept, w)
		}
	}
	c.waiters = kept
}

func (c *fakeClock) waitReg(t *testing.T) {
	t.Helper()
	select {
	case <-c.reg:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler never waited on the clock")
	}
}

type logSink struct {
	mu    sync.Mutex
	lines []string
}

func captureLogs(t *testing.T) *logSink {
	s := &logSink{}
	old := Logf
	Logf = func(f string, a ...any) {
		s.mu.Lock()
		s.lines = append(s.lines, fmt.Sprintf(f, a...))
		s.mu.Unlock()
	}
	t.Cleanup(func() { Logf = old })
	return s
}

func (s *logSink) has(sub string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

var sched = config.Schedule{DailyAt: "08:30", Timezone: "America/Los_Angeles"}

type harness struct {
	t      *testing.T
	clock  *fakeClock
	cancel context.CancelFunc
	done   chan error
	runs   atomic.Int32
	ran    chan struct{}
}

func start(t *testing.T, now time.Time, job func(ctx context.Context, h *harness) error) *harness {
	t.Helper()
	h := &harness{t: t, clock: newClock(now), done: make(chan error, 1), ran: make(chan struct{}, 100)}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	t.Cleanup(cancel)
	go func() {
		h.done <- Run(ctx, sched, func(ctx context.Context) error {
			h.runs.Add(1)
			defer func() { h.ran <- struct{}{} }()
			if job != nil {
				return job(ctx, h)
			}
			return nil
		}, h.clock)
	}()
	h.clock.waitReg(t)
	return h
}

func (h *harness) waitRan() {
	h.t.Helper()
	select {
	case <-h.ran:
	case <-time.After(5 * time.Second):
		h.t.Fatal("job did not run")
	}
}

func (h *harness) stop() {
	h.t.Helper()
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			h.t.Errorf("Run returned %v on cancel", err)
		}
	case <-time.After(5 * time.Second):
		h.t.Fatal("Run did not exit on cancel")
	}
}

func TestFiresOncePerDayAcrossDST(t *testing.T) {
	loc := la(t)
	h := start(t, time.Date(2026, 3, 7, 12, 0, 0, 0, loc), nil)
	// Advancing to just before the slot must not fire the job.
	h.clock.Advance(time.Date(2026, 3, 8, 8, 29, 0, 0, loc))
	if h.runs.Load() != 0 {
		t.Fatal("fired early")
	}
	// Spring-forward day: slot is 08:30 PDT.
	h.clock.Advance(time.Date(2026, 3, 8, 8, 30, 0, 0, loc))
	h.waitRan()
	h.clock.waitReg(t)
	// Exactly one run for the day; the next slot is the following morning.
	h.clock.Advance(time.Date(2026, 3, 9, 8, 30, 0, 0, loc))
	h.waitRan()
	h.clock.waitReg(t)
	if got := h.runs.Load(); got != 2 {
		t.Errorf("runs = %d, want 2", got)
	}
	h.stop()
}

func TestNeverOverlapsAndOverrunDelays(t *testing.T) {
	loc := la(t)
	logs := captureLogs(t)
	var active, maxActive atomic.Int32
	release := make(chan struct{})
	h := start(t, time.Date(2026, 10, 18, 9, 0, 0, 0, loc), func(ctx context.Context, h *harness) error {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			m := maxActive.Load()
			if n <= m || maxActive.CompareAndSwap(m, n) {
				break
			}
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	h.clock.Advance(time.Date(2026, 10, 19, 8, 30, 0, 0, loc))
	for h.runs.Load() < 1 {
		time.Sleep(time.Millisecond)
	}
	// The job overruns past the next day's slot (2h late, within the window).
	h.clock.Advance(time.Date(2026, 10, 20, 10, 30, 0, 0, loc))
	time.Sleep(20 * time.Millisecond)
	if h.runs.Load() != 1 {
		t.Fatalf("second job started while first still running: runs=%d", h.runs.Load())
	}
	release <- struct{}{}
	h.waitRan()
	// The delayed (Oct 20 08:30) slot now runs once, after the first finished.
	for h.runs.Load() < 2 {
		time.Sleep(time.Millisecond)
	}
	release <- struct{}{}
	h.waitRan()
	h.clock.waitReg(t)
	if maxActive.Load() != 1 {
		t.Errorf("max concurrent jobs = %d", maxActive.Load())
	}
	if got := h.runs.Load(); got != 2 {
		t.Errorf("stacked runs: %d", got)
	}
	if !logs.has("missed run") {
		t.Error("delayed run not logged")
	}
	h.stop()
}

func TestMissedRunWithinWindowRunsOnce(t *testing.T) {
	loc := la(t)
	logs := captureLogs(t)
	h := start(t, time.Date(2026, 10, 18, 7, 0, 0, 0, loc), nil)
	// Wake at 11:00: 2.5h late.
	h.clock.Advance(time.Date(2026, 10, 18, 11, 0, 0, 0, loc))
	h.waitRan()
	h.clock.waitReg(t)
	if got := h.runs.Load(); got != 1 {
		t.Errorf("runs = %d, want 1", got)
	}
	if !logs.has("missed run") || !logs.has("running once now") {
		t.Errorf("missed run not logged: %v", logs.lines)
	}
	h.stop()
}

func TestMissedRunBeyondWindowSkips(t *testing.T) {
	loc := la(t)
	logs := captureLogs(t)
	h := start(t, time.Date(2026, 10, 18, 7, 0, 0, 0, loc), nil)
	// Wake at 15:00: 6.5h late -> skipped.
	h.clock.Advance(time.Date(2026, 10, 18, 15, 0, 0, 0, loc))
	h.clock.waitReg(t)
	if h.runs.Load() != 0 {
		t.Fatal("run executed beyond the 6h window")
	}
	if !logs.has("skipping run") {
		t.Errorf("skip not logged: %v", logs.lines)
	}
	// The next day's slot still fires.
	h.clock.Advance(time.Date(2026, 10, 19, 8, 30, 0, 0, loc))
	h.waitRan()
	h.clock.waitReg(t)
	h.stop()
}

func TestWindowBoundaryExactly6hRuns(t *testing.T) {
	loc := la(t)
	h := start(t, time.Date(2026, 10, 18, 7, 0, 0, 0, loc), nil)
	h.clock.Advance(time.Date(2026, 10, 18, 14, 30, 0, 0, loc))
	h.waitRan()
	h.clock.waitReg(t)
	h.stop()
}

func TestCancelWhileWaitingAndWhileRunning(t *testing.T) {
	loc := la(t)
	h := start(t, time.Date(2026, 10, 18, 7, 0, 0, 0, loc), nil)
	h.stop()

	started := make(chan struct{})
	h = start(t, time.Date(2026, 10, 18, 7, 0, 0, 0, loc), func(ctx context.Context, _ *harness) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	h.clock.Advance(time.Date(2026, 10, 18, 8, 30, 0, 0, loc))
	<-started
	h.stop()
}

func TestJobErrorAndPanicKeepSchedule(t *testing.T) {
	loc := la(t)
	logs := captureLogs(t)
	var n atomic.Int32
	h := start(t, time.Date(2026, 10, 18, 7, 0, 0, 0, loc), func(context.Context, *harness) error {
		if n.Add(1) == 1 {
			return errors.New("boom")
		}
		panic("kaboom")
	})
	h.clock.Advance(time.Date(2026, 10, 18, 8, 30, 0, 0, loc))
	h.waitRan()
	h.clock.waitReg(t)
	h.clock.Advance(time.Date(2026, 10, 19, 8, 30, 0, 0, loc))
	h.waitRan()
	h.clock.waitReg(t)
	if !logs.has("job failed: boom") || !logs.has("job panicked: kaboom") {
		t.Errorf("failures not logged: %v", logs.lines)
	}
	h.stop()
}

// A job error wrapping ErrFatal ends Run with that error, logged as an exit;
// the daemon does not wait for another slot to fail the same way.
func TestFatalJobErrorEndsRun(t *testing.T) {
	loc := la(t)
	logs := captureLogs(t)
	cause := errors.New("ledger poisoned")
	h := start(t, time.Date(2026, 10, 18, 7, 0, 0, 0, loc), func(context.Context, *harness) error {
		return fmt.Errorf("%w: %w", ErrFatal, cause)
	})
	h.clock.Advance(time.Date(2026, 10, 18, 8, 30, 0, 0, loc))
	h.waitRan()
	select {
	case err := <-h.done:
		if !errors.Is(err, ErrFatal) || !errors.Is(err, cause) {
			t.Fatalf("Run returned %v, want the fatal job error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept scheduling after a fatal job error")
	}
	if !logs.has("job failed, exiting: daemon: fatal job error: ledger poisoned") {
		t.Errorf("exit not logged: %v", logs.lines)
	}
	if n := h.runs.Load(); n != 1 {
		t.Errorf("%d runs, want 1", n)
	}
}

func TestRunRejectsBadSchedule(t *testing.T) {
	c := newClock(time.Now())
	job := func(context.Context) error { return nil }
	for _, s := range []config.Schedule{
		{DailyAt: "25:00", Timezone: "America/Los_Angeles"},
		{DailyAt: "08:30", Timezone: "Nowhere/Land"},
		{},
	} {
		if err := Run(context.Background(), s, job, c); err == nil {
			t.Errorf("accepted schedule %+v", s)
		}
	}
	if err := Run(context.Background(), sched, nil, c); err == nil {
		t.Error("accepted nil job")
	}
}

func TestPlistXMLEscaping(t *testing.T) {
	out := string(PlistXML(`com.vani"<&>`, `/Applications/V&V/vani`, []string{"daemon", `--x=<a href="y">&'`}, `/cfg/a&b/<c>.json`, "/st/<d>"))
	for _, bad := range []string{`<a href`, `"<&>`, "V&V", "/st/<d>", "a&b", "<c>"} {
		if strings.Contains(out, bad) {
			t.Errorf("unescaped %q in:\n%s", bad, out)
		}
	}
	for _, want := range []string{
		"<string>com.vani&#34;&lt;&amp;&gt;</string>",
		"/Applications/V&amp;V/vani",
		"&lt;a href=&#34;y&#34;&gt;&amp;&#39;",
		"/st/&lt;d&gt;/logs/vani.out.log",
		"/st/&lt;d&gt;/logs/vani.err.log",
		"<key>VANI_CONFIG</key>\n\t\t<string>/cfg/a&amp;b/&lt;c&gt;.json</string>",
		"<key>VANI_STATE_DIR</key>\n\t\t<string>/st/&lt;d&gt;</string>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>ProgramArguments</key>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if err := xmlWellFormed(out); err != nil {
		t.Errorf("not well-formed XML: %v", err)
	}
}

// monoClock models darwin: After timers run on a monotonic clock that does not
// advance while the machine sleeps, while Now is wall time. Tick advances both
// (awake time); Jump advances only the wall clock (sleep, or a clock step).
type monoClock struct {
	mu      sync.Mutex
	wall    time.Time
	mono    time.Duration
	waiters []monoWaiter
	durs    []time.Duration
	reg     chan struct{}
}

type monoWaiter struct {
	at time.Duration
	ch chan time.Time
}

func newMonoClock(wall time.Time) *monoClock {
	return &monoClock{wall: wall, reg: make(chan struct{}, 100000)}
}

func (c *monoClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wall
}

func (c *monoClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, monoWaiter{c.mono + d, ch})
	c.durs = append(c.durs, d)
	c.mu.Unlock()
	c.reg <- struct{}{}
	return ch
}

func (c *monoClock) Tick(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mono += d
	c.wall = c.wall.Add(d)
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if w.at <= c.mono {
			w.ch <- c.wall
		} else {
			kept = append(kept, w)
		}
	}
	c.waiters = kept
}

func (c *monoClock) Jump(d time.Duration) {
	c.mu.Lock()
	c.wall = c.wall.Add(d)
	c.mu.Unlock()
}

func (c *monoClock) afterDurations() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.durs...)
}

func (c *monoClock) waitReg(t *testing.T) {
	t.Helper()
	select {
	case <-c.reg:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler never waited on the clock")
	}
}

func startMono(t *testing.T, now time.Time) (*monoClock, *harness) {
	t.Helper()
	mc := newMonoClock(now)
	h := &harness{t: t, done: make(chan error, 1), ran: make(chan struct{}, 100)}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	t.Cleanup(cancel)
	go func() {
		h.done <- Run(ctx, sched, func(context.Context) error {
			h.runs.Add(1)
			h.ran <- struct{}{}
			return nil
		}, mc)
	}()
	mc.waitReg(t)
	return mc, h
}

// The machine sleeps overnight: wall time reaches the slot while the monotonic
// timer for the original interval has barely moved. The run must start within
// one slice of the wall deadline and not be reported late.
func TestWakeFromSleepRunsOnTime(t *testing.T) {
	loc := la(t)
	logs := captureLogs(t)
	mc, h := startMono(t, time.Date(2026, 10, 20, 22, 0, 0, 0, loc))
	mc.Jump(10*time.Hour + 29*time.Minute + 50*time.Second) // wall 08:29:50, mono unchanged
	mc.Tick(wakeSlice)
	h.waitRan()
	if logs.has("missed run") || logs.has("skipping run") {
		t.Errorf("run reported late: %v", logs.lines)
	}
	h.stop()
}

func TestWaitSlicesAreBoundedAndNeverSpin(t *testing.T) {
	loc := la(t)
	start := time.Date(2026, 10, 20, 7, 0, 0, 0, loc)
	mc := newMonoClock(start)
	done := make(chan bool, 1)
	go func() { done <- waitUntil(context.Background(), mc, start.Add(time.Hour)) }()
	for finished := false; !finished; {
		select {
		case <-mc.reg:
			mc.Tick(wakeSlice)
		case ok := <-done:
			if !ok {
				t.Fatal("waitUntil returned false without cancel")
			}
			finished = true
		case <-time.After(5 * time.Second):
			t.Fatal("waitUntil stuck")
		}
	}
	durs := mc.afterDurations()
	if want := int(time.Hour / wakeSlice); len(durs) > want+1 {
		t.Errorf("After called %d times for 1h, want <= %d", len(durs), want+1)
	}
	for _, d := range durs {
		if d <= 0 || d > wakeSlice {
			t.Errorf("After(%v) outside (0, %v]", d, wakeSlice)
		}
	}
}

func TestWaitUntilWaitsOnlyRemainingWhenShorterThanSlice(t *testing.T) {
	now := time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)
	mc := newMonoClock(now)
	done := make(chan bool, 1)
	go func() { done <- waitUntil(context.Background(), mc, now.Add(5*time.Second)) }()
	mc.waitReg(t)
	mc.Tick(5 * time.Second)
	if !<-done {
		t.Fatal("waitUntil returned false")
	}
	if durs := mc.afterDurations(); len(durs) != 1 || durs[0] != 5*time.Second {
		t.Errorf("durations = %v, want [5s]", durs)
	}
}

func TestWaitUntilDeadlinePassedDoesNotWait(t *testing.T) {
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	mc := newMonoClock(now)
	if !waitUntil(context.Background(), mc, now.Add(-time.Hour)) {
		t.Error("past deadline returned false")
	}
	if !waitUntil(context.Background(), mc, now) {
		t.Error("deadline == now returned false")
	}
	if n := len(mc.afterDurations()); n != 0 {
		t.Errorf("After called %d times for a past deadline", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitUntil(ctx, mc, now.Add(-time.Hour)) {
		t.Error("cancelled ctx reported success")
	}
}

// A backwards wall step must not fire the run early, spin, or lose the wait.
func TestWallClockJumpsBackwardsKeepsWaiting(t *testing.T) {
	loc := la(t)
	mc, h := startMono(t, time.Date(2026, 10, 20, 7, 0, 0, 0, loc))
	mc.Jump(-3 * time.Hour) // wall 04:00
	mc.Tick(wakeSlice)
	mc.waitReg(t) // the loop re-armed instead of firing or spinning
	if h.runs.Load() != 0 {
		t.Fatal("fired after backwards jump")
	}
	for _, d := range mc.afterDurations() {
		if d <= 0 || d > wakeSlice {
			t.Errorf("After(%v) outside (0, %v]", d, wakeSlice)
		}
	}
	if n := len(mc.afterDurations()); n != 2 {
		t.Errorf("After calls = %d, want 2", n)
	}
	// Wall catches up to the slot: the run fires.
	mc.Jump(4*time.Hour + 30*time.Minute)
	mc.Tick(wakeSlice)
	h.waitRan()
	h.stop()
}

func TestCancelMidSlice(t *testing.T) {
	loc := la(t)
	mc, h := startMono(t, time.Date(2026, 10, 20, 7, 0, 0, 0, loc))
	mc.Tick(wakeSlice / 2) // part-way through a slice
	h.stop()
	if h.runs.Load() != 0 {
		t.Error("job ran after cancel")
	}
}
