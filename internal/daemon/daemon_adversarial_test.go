package daemon

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A deadline handed to waitUntil must carry no Go monotonic reading: with one,
// t.Sub(clock.Now()) would use the monotonic clock (which stalls in system
// sleep on darwin) and the wall-clock slicing would be void on real hardware.
func TestNextRunCarriesNoMonotonicReading(t *testing.T) {
	loc := la(t)
	real := time.Now() // carries a monotonic reading
	if !strings.Contains(real.String(), "m=") {
		t.Skip("time.Now() has no monotonic reading on this platform")
	}
	cursors := []time.Time{real, real.In(loc), real.In(time.UTC), real.Add(36 * time.Hour)}
	for _, c := range cursors {
		for _, hm := range [][2]int{{8, 30}, {2, 30}, {0, 0}, {23, 59}} {
			next := NextRun(c, hm[0], hm[1], loc)
			if strings.Contains(next.String(), "m=") || next != next.Round(0) {
				t.Errorf("NextRun(%v, %v) carries a monotonic reading: %s", c, hm, next)
			}
			now := time.Now()
			// Sub with a mono-less operand must use the wall clock.
			if got, want := next.Sub(now), next.Round(0).Sub(now.Round(0)); got != want {
				t.Errorf("Sub used monotonic time: %v vs wall %v", got, want)
			}
		}
	}
	// DST-gap path (spring forward 2026-03-08, 02:30 does not exist in LA).
	gap := NextRun(time.Date(2026, 3, 7, 12, 0, 0, 0, loc).Add(0), 2, 30, loc)
	if strings.Contains(gap.String(), "m=") {
		t.Errorf("DST-gap wallTime carries monotonic reading: %s", gap)
	}
	// Cursor derived from a mono-carrying now, after a skipped run (cursor = now).
	skip := NextRun(time.Now(), 8, 30, loc)
	if strings.Contains(skip.String(), "m=") {
		t.Errorf("post-skip NextRun carries monotonic reading: %s", skip)
	}
}

// Real SystemClock Now against a NextRun deadline: Sub must equal the wall difference.
func TestWaitUntilDeadlineSubIsWallWithSystemClock(t *testing.T) {
	loc := la(t)
	var clk SystemClock
	next := NextRun(clk.Now(), 8, 30, loc)
	now := clk.Now()
	if next.Sub(now) != next.Round(0).Sub(now.Round(0)) {
		t.Fatal("deadline.Sub(SystemClock.Now()) used the monotonic clock")
	}
	if now.Sub(next) != now.Round(0).Sub(next.Round(0)) {
		t.Fatal("late := now.Sub(next) used the monotonic clock")
	}
}

func TestWaitUntilCtxCancelledAtEntryWithFutureDeadline(t *testing.T) {
	now := time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)
	mc := newMonoClock(now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitUntil(ctx, mc, now.Add(time.Hour)) {
		t.Error("cancelled ctx with future deadline reported success")
	}
}

func TestWaitUntilOneNanosecondDeadline(t *testing.T) {
	now := time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)
	mc := newMonoClock(now)
	done := make(chan bool, 1)
	go func() { done <- waitUntil(context.Background(), mc, now.Add(time.Nanosecond)) }()
	mc.waitReg(t)
	mc.Tick(time.Nanosecond)
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("returned false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stuck")
	}
	if d := mc.afterDurations(); len(d) != 1 || d[0] != time.Nanosecond {
		t.Errorf("durations = %v", d)
	}
}

func TestWaitUntilHugeDeadlineIsSlicedAndSaturationSafe(t *testing.T) {
	now := time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)
	mc := newMonoClock(now)
	// Beyond the int64-nanosecond range (~292y): Sub saturates to maxDuration.
	far := now.AddDate(400, 0, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- waitUntil(ctx, mc, far) }()
	for i := 0; i < 50; i++ {
		mc.waitReg(t)
		mc.Tick(wakeSlice)
	}
	cancel()
	if ok := <-done; ok {
		t.Fatal("huge deadline returned true")
	}
	for _, d := range mc.afterDurations() {
		if d <= 0 || d > wakeSlice {
			t.Fatalf("After(%v) outside (0, %v]", d, wakeSlice)
		}
	}
}

// Repeated 1-day backwards wall jumps: every slice stays in (0, wakeSlice], one
// After call per slice (no spin), and the wait is still live afterwards.
func TestWaitUntilRepeatedBackwardsJumpsNoSpin(t *testing.T) {
	now := time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)
	mc := newMonoClock(now)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- waitUntil(ctx, mc, now.Add(time.Hour)) }()
	const rounds = 100
	for i := 0; i < rounds; i++ {
		mc.waitReg(t)
		mc.Jump(-24 * time.Hour)
		mc.Tick(wakeSlice)
	}
	mc.waitReg(t)
	time.Sleep(20 * time.Millisecond) // a spin would register many more Afters now
	n := len(mc.afterDurations())
	if n != rounds+1 {
		t.Errorf("After calls = %d, want %d (one per slice)", n, rounds+1)
	}
	for _, d := range mc.afterDurations() {
		if d <= 0 || d > wakeSlice {
			t.Fatalf("After(%v) outside (0, %v]", d, wakeSlice)
		}
	}
	cancel()
	if ok := <-done; ok {
		t.Fatal("returned true after cancel")
	}
}

// Wall clock jumps forward past the deadline mid-run while After never fires:
// the next slice boundary must return, not wait the stale interval.
func TestWaitUntilExactSliceBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)
	for _, extra := range []time.Duration{0, 1, -1} {
		mc := newMonoClock(now)
		dl := now.Add(2*wakeSlice + extra)
		done := make(chan bool, 1)
		go func() { done <- waitUntil(context.Background(), mc, dl) }()
		for fin := false; !fin; {
			select {
			case <-mc.reg:
				mc.Tick(wakeSlice)
			case ok := <-done:
				if !ok {
					t.Fatalf("extra=%v: false", extra)
				}
				fin = true
			case <-time.After(5 * time.Second):
				t.Fatalf("extra=%v: stuck", extra)
			}
		}
		if n := len(mc.afterDurations()); n > 3 {
			t.Errorf("extra=%v: %d After calls", extra, n)
		}
	}
}
