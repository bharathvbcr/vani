// Package daemon runs Vani's daily job on a wall-clock schedule and renders
// the launchd agent definition that starts it.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
)

const (
	// MissedRunWindow is how late a run may start (after sleep/wake or after
	// a long previous job) and still run once; later than this it is skipped.
	MissedRunWindow = 6 * time.Hour
	// onTimeSlack is the lateness below which a run is reported as on time.
	onTimeSlack = time.Minute
	// wakeSlice caps how long waitUntil sleeps on one timer. On darwin Go
	// timers use a monotonic clock that does not advance while the machine
	// sleeps, but the schedule is wall time: one long timer would fire hours
	// after the slot on wake. Re-reading the wall clock every slice bounds that
	// lateness to one slice. It stays well under onTimeSlack, so a wake is
	// never reported as a missed run, and 30 s keeps the idle wakeups trivial.
	wakeSlice = 30 * time.Second
)

// Logf receives the scheduler's decisions. Tests replace it.
var Logf = log.Printf

// ErrFatal marks a job error the process cannot outlive, such as a ledger
// left unusable by a failed write: Run returns it, and the daemon exits
// non-zero, instead of logging it and failing every later run the same way.
var ErrFatal = errors.New("daemon: fatal job error")

// Job is the unit of work run once per day.
type Job func(ctx context.Context) error

// Clock abstracts time so the scheduler is testable.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// SystemClock is the real clock.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now() }

// After implements Clock.
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// NextRun returns the first scheduled instant strictly after `after`, where
// the schedule is hour:minute on the wall clock of loc. A wall time that does
// not exist on a spring-forward day runs at the first instant after the gap.
func NextRun(after time.Time, hour, minute int, loc *time.Location) time.Time {
	local := after.In(loc)
	y, m, d := local.Date()
	for i := 0; i < 3; i++ {
		if c := wallTime(y, m, d+i, hour, minute, loc); c.After(after) {
			return c
		}
	}
	// Unreachable for real zones: three consecutive days always contain a
	// slot after `after`.
	return wallTime(y, m, d+3, hour, minute, loc)
}

// PrevRun returns the latest scheduled instant at or before `at`, on the same
// schedule NextRun reads: hour:minute on the wall clock of loc. It starts two
// days back, which always holds a slot, and steps forward to the last one not
// after `at`.
func PrevRun(at time.Time, hour, minute int, loc *time.Location) time.Time {
	prev := NextRun(at.Add(-48*time.Hour), hour, minute, loc)
	for {
		next := NextRun(prev, hour, minute, loc)
		if next.After(at) {
			return prev
		}
		prev = next
	}
}

// wallTime is the instant at which loc's wall clock first reads hour:minute on
// the given date, or the first instant after the gap if it never does.
func wallTime(y int, m time.Month, d, hour, minute int, loc *time.Location) time.Time {
	t := time.Date(y, m, d, hour, minute, 0, 0, loc)
	if t.Hour() == hour && t.Minute() == minute {
		return t
	}
	// The wall time falls in a DST gap. Walk forward from local midnight to
	// the first minute whose wall clock has reached the target.
	t = time.Date(y, m, d, 0, 0, 0, 0, loc)
	target := hour*60 + minute
	for i := 0; i < 26*60; i++ {
		if t.Hour()*60+t.Minute() >= target && sameDay(t, y, m, d) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Date(y, m, d, hour, minute, 0, 0, loc)
}

func sameDay(t time.Time, y int, m time.Month, d int) bool {
	ty, tm, td := t.Date()
	return time.Date(ty, tm, td, 0, 0, 0, 0, time.UTC).Equal(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
}

// Run fires job once per day at sched.DailyAt in sched.Timezone until ctx is
// cancelled, then returns nil. Jobs run on this goroutine, so two never
// overlap: a job that overruns delays the next run and runs are not stacked.
// A run that starts late (sleep/wake, or after an overrun) runs once if it is
// within MissedRunWindow of its slot and is skipped, with a log line, if not.
// A job error is logged and the schedule continues, except one wrapping
// ErrFatal, which Run returns after logging it.
func Run(ctx context.Context, sched config.Schedule, job Job, clock Clock) error {
	hour, minute, loc, err := sched.Parsed()
	if err != nil {
		return fmt.Errorf("daemon: schedule: %w", err)
	}
	if job == nil || clock == nil {
		return fmt.Errorf("daemon: job and clock are required")
	}
	cursor := clock.Now()
	for {
		next := NextRun(cursor, hour, minute, loc)
		Logf("daemon: next run at %s", next.In(loc).Format(time.RFC3339))
		if !waitUntil(ctx, clock, next) {
			return nil
		}
		now := clock.Now()
		late := now.Sub(next)
		switch {
		case late > MissedRunWindow:
			Logf("daemon: skipping run scheduled %s: %s late exceeds %s window",
				next.In(loc).Format(time.RFC3339), late.Round(time.Second), MissedRunWindow)
			cursor = now
			continue
		case late > onTimeSlack:
			Logf("daemon: missed run scheduled %s, running once now (%s late)",
				next.In(loc).Format(time.RFC3339), late.Round(time.Second))
		}
		if err := runJob(ctx, job); errors.Is(err, ErrFatal) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		// Advance from the slot just served, so a delayed run does not also
		// consume the next day's slot and a fall-back repeat hour cannot fire twice.
		cursor = next
	}
}

// waitUntil blocks until clock.Now() reaches t. It returns false if ctx ends
// first. It sleeps at most wakeSlice per clock.After and re-reads the wall
// clock each time, so a sleep that stalls the timer clock (or a wall-clock
// step in either direction) delays the return by at most one slice. Every
// iteration calls After with a positive duration, so it cannot busy-spin.
func waitUntil(ctx context.Context, clock Clock, t time.Time) bool {
	for {
		d := t.Sub(clock.Now())
		if d <= 0 {
			return ctx.Err() == nil
		}
		d = min(d, wakeSlice)
		timer := clock.After(d)
		// Re-check after arming: time that passed between the read above and
		// After (a clock jump, a stalled goroutine) would otherwise be added to
		// the wait, since After measures from its own call.
		if !clock.Now().Before(t) {
			return ctx.Err() == nil
		}
		select {
		case <-ctx.Done():
			return false
		case <-timer:
		}
	}
}

func runJob(ctx context.Context, job Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			Logf("daemon: job panicked: %v", r)
		}
	}()
	if err = job(ctx); err != nil {
		if errors.Is(err, ErrFatal) {
			Logf("daemon: job failed, exiting: %v", err)
		} else {
			Logf("daemon: job failed: %v", err)
		}
	}
	return err
}
