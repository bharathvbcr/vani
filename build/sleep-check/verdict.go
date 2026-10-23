package main

import (
	"fmt"
	"time"
)

// The daemon's own constants are unexported, so the check restates them. If
// they change, this check must change with them (the verdict text says which
// values it used).
const (
	wakeSlice    = 30 * time.Second // daemon.wakeSlice
	onTimeSlack  = time.Minute      // daemon.onTimeSlack
	awakeGrace   = 5 * time.Second  // allowed beyond wakeSlice when awake
	passDeadline = wakeSlice + onTimeSlack
	awakeLimit   = wakeSlice + awakeGrace
)

// sleepSpan is one observed machine sleep: the last wall-clock sample before
// it and the first one after it.
type sleepSpan struct {
	Start time.Time
	Wake  time.Time
}

// observation is everything the verdict is a function of.
type observation struct {
	Scheduled time.Time
	Fired     time.Time // zero when the job never ran
	Skipped   bool      // the daemon logged that it skipped the run as missed
	Spans     []sleepSpan
}

type outcome string

const (
	outcomePass         outcome = "PASS"
	outcomeFail         outcome = "FAIL"
	outcomeInconclusive outcome = "INCONCLUSIVE"
)

// judge turns an observation into a verdict. Every time is compared as wall
// time (monotonic readings are stripped), because a monotonic delta does not
// include the time the machine spent asleep.
//
//   - skipped, or never fired: FAIL.
//   - the machine slept across the scheduled instant: lateness is measured
//     from the wake, and PASS needs it within wakeSlice+onTimeSlack.
//   - otherwise the machine was awake at the deadline: FAIL if the run came
//     more than wakeSlice+5s after it, else INCONCLUSIVE, because nothing
//     about sleep was exercised.
func judge(o observation) (outcome, string) {
	sched := o.Scheduled.Round(0)
	if o.Skipped {
		return outcomeFail, "the daemon skipped the run as missed instead of running it after wake"
	}
	if o.Fired.IsZero() {
		return outcomeFail, "the job never ran"
	}
	fired := o.Fired.Round(0)

	if span, ok := spanAcross(o.Spans, sched); ok {
		wake := span.Wake.Round(0)
		late := fired.Sub(wake)
		if late < 0 {
			late = 0 // fired before the sampler noticed the wake
		}
		if late > passDeadline {
			return outcomeFail, fmt.Sprintf("fired %s after wake, over the %s limit (wakeSlice %s + onTimeSlack %s)",
				late.Round(time.Second), passDeadline, wakeSlice, onTimeSlack)
		}
		return outcomePass, fmt.Sprintf("slept across the schedule; fired %s after wake, within %s (wakeSlice %s + onTimeSlack %s)",
			late.Round(time.Second), passDeadline, wakeSlice, onTimeSlack)
	}

	late := fired.Sub(sched)
	if late > awakeLimit {
		return outcomeFail, fmt.Sprintf("awake at the deadline but fired %s late, over %s (wakeSlice %s + %s)",
			late.Round(time.Second), awakeLimit, wakeSlice, awakeGrace)
	}
	return outcomeInconclusive, "the Mac did not sleep across the scheduled minute (no wall-clock jump over it); the run was on time but sleep was not exercised"
}

// spanAcross returns the sleep that covered instant t: it began before t and
// the machine was back at or after it.
func spanAcross(spans []sleepSpan, t time.Time) (sleepSpan, bool) {
	for _, s := range spans {
		if s.Start.Round(0).Before(t) && !s.Wake.Round(0).Before(t) {
			return s, true
		}
	}
	return sleepSpan{}, false
}
