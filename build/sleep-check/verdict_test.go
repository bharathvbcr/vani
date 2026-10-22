package main

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 22, 14, 30, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return t0.Add(d) }

func TestJudge(t *testing.T) {
	across := []sleepSpan{{Start: at(-90 * time.Second), Wake: at(2 * time.Minute)}}
	cases := []struct {
		name string
		obs  observation
		want outcome
		text string
	}{
		{"pass: slept across, fired right after wake",
			observation{Scheduled: t0, Fired: at(2*time.Minute + 8*time.Second), Spans: across}, outcomePass, "after wake"},
		{"pass: wake exactly at the scheduled instant",
			observation{Scheduled: t0, Fired: at(10 * time.Second), Spans: []sleepSpan{{Start: at(-time.Minute), Wake: t0}}}, outcomePass, ""},
		{"pass: fired before the sampler noticed the wake",
			observation{Scheduled: t0, Fired: at(2*time.Minute - time.Second), Spans: across}, outcomePass, "0s after wake"},
		{"pass: at the limit",
			observation{Scheduled: t0, Fired: at(2*time.Minute + passDeadline), Spans: across}, outcomePass, ""},
		{"fail late: slept, fired too long after wake",
			observation{Scheduled: t0, Fired: at(2*time.Minute + passDeadline + time.Second), Spans: across}, outcomeFail, "over the"},
		{"fail late: awake, fired beyond wakeSlice+5s",
			observation{Scheduled: t0, Fired: at(awakeLimit + time.Second)}, outcomeFail, "awake"},
		{"fail skipped",
			observation{Scheduled: t0, Skipped: true, Spans: across}, outcomeFail, "skipped"},
		{"fail: never ran",
			observation{Scheduled: t0}, outcomeFail, "never ran"},
		{"inconclusive: no sleep, on time",
			observation{Scheduled: t0, Fired: at(300 * time.Millisecond)}, outcomeInconclusive, "did not sleep"},
		{"inconclusive: awake, late but within grace",
			observation{Scheduled: t0, Fired: at(awakeLimit)}, outcomeInconclusive, "did not sleep"},
		{"inconclusive: slept only before the scheduled minute",
			observation{Scheduled: t0, Fired: at(time.Second),
				Spans: []sleepSpan{{Start: at(-5 * time.Minute), Wake: at(-2 * time.Minute)}}}, outcomeInconclusive, ""},
		{"inconclusive: slept only after the scheduled minute",
			observation{Scheduled: t0, Fired: at(time.Second),
				Spans: []sleepSpan{{Start: at(time.Second), Wake: at(time.Minute)}}}, outcomeInconclusive, ""},
	}
	for _, c := range cases {
		got, detail := judge(c.obs)
		if got != c.want {
			t.Errorf("%s: got %s (%s), want %s", c.name, got, detail, c.want)
		}
		if !strings.Contains(detail, c.text) {
			t.Errorf("%s: detail %q lacks %q", c.name, detail, c.text)
		}
	}
}

// A monotonic reading on either side must not change the verdict: after a
// sleep a monotonic Sub excludes the sleep, which is the bug judge avoids.
func TestJudgeIgnoresMonotonicReadings(t *testing.T) {
	now := time.Now() // carries a monotonic reading
	o := observation{
		Scheduled: now.Round(0),
		Fired:     now.Add(10 * time.Second),
		Spans:     []sleepSpan{{Start: now.Add(-time.Minute), Wake: now.Add(time.Second)}},
	}
	if got, detail := judge(o); got != outcomePass {
		t.Fatalf("got %s (%s), want PASS", got, detail)
	}
}
