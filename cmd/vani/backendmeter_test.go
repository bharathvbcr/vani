package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/decide"
)

// scripted is a backend whose every call is decided by fn(callNumber), 1-based.
// It counts the calls that reach it.
type scripted struct {
	name  string
	calls atomic.Int64
	fn    func(n int64, q decide.Question) (decide.Answer, error)
}

func (s *scripted) Name() string {
	if s.name == "" {
		return "llm"
	}
	return s.name
}
func (s *scripted) Serves(string) bool { return true }
func (s *scripted) Decide(_ context.Context, q decide.Question) (decide.Answer, error) {
	return s.fn(s.calls.Add(1), q)
}

func slotsQ(names ...string) decide.Question {
	q := decide.Question{Task: "t", Prompt: "p"}
	for _, n := range names {
		q.Slots = append(q.Slots, decide.Slot{Name: n, Options: []string{"a", "b"}})
	}
	return q
}

func answered(q decide.Question, cost float64) decide.Answer {
	a := decide.Answer{Backend: "llm", CostUSD: cost, Slots: map[string]decide.SlotReading{}}
	for _, s := range q.Slots {
		a.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: "a"}
	}
	return a
}

var errDown = errors.New("provider down")

func quiet(string, ...any) {}

// withoutBreaker turns the breaker off, for tests of the error log and the
// error counts, which need a backend that is called however often it fails.
func withoutBreaker(e *errorLogged) *errorLogged {
	e.br.trip = 0
	return e
}

// failing returns a backend that errors for the calls in [from, to] (1-based)
// and answers the others.
func failing(from, to int64) *scripted {
	return &scripted{fn: func(n int64, q decide.Question) (decide.Answer, error) {
		if n >= from && n <= to {
			return decide.Answer{}, errDown
		}
		return answered(q, 0.001), nil
	}}
}

// After breakerTrip consecutive errors the next call must not reach the
// backend, and must read Unavailable on every slot it was asked for.
func TestBreakerOpensAfterConsecutiveErrors(t *testing.T) {
	var lines []string
	warnf := func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	inner := failing(1, 1000)
	b := logErrors(inner, warnf, newBackendErrors())
	q := slotsQ("remote", "seniority")
	for i := 0; i < breakerTrip; i++ {
		if _, err := b.Decide(context.Background(), q); !errors.Is(err, errDown) {
			t.Fatalf("call %d: err %v, want the backend's own error", i+1, err)
		}
	}
	before := inner.calls.Load()
	a, err := b.Decide(context.Background(), q)
	if inner.calls.Load() != before {
		t.Fatal("the breaker is open, yet the inner backend was called")
	}
	if err == nil || !errors.Is(err, errBreakerOpen) {
		t.Fatalf("err = %v, want errBreakerOpen", err)
	}
	if errors.Is(err, decide.ErrAuth) {
		t.Fatal("a breaker rejection must not look like rejected credentials: the router would disable the backend for good")
	}
	if len(a.Slots) != 2 {
		t.Fatalf("slots %v, want one per requested slot", a.Slots)
	}
	for name, rd := range a.Slots {
		if rd.Reading != decide.Unavailable || rd.Detail != "breaker_open" || rd.Value != "" {
			t.Errorf("slot %s = %+v, want Unavailable/breaker_open", name, rd)
		}
	}
	open := 0
	for _, l := range lines {
		if strings.Contains(l, "breaker open") {
			open++
		}
	}
	if open != 1 {
		t.Fatalf("%d breaker-open lines in %v, want exactly 1", open, lines)
	}
}

// Through the real router, with no fallback, an open breaker leaves every slot
// Unavailable, the reading the pipeline reads as "could not read": the posting
// stays Live and the run's unread streak, not a permanent Unsure, follows.
func TestOpenBreakerThroughRouterReadsUnavailable(t *testing.T) {
	inner := failing(1, 1000)
	r := decide.NewRouter(logErrors(inner, quiet, nil))
	q := slotsQ("remote", "seniority")
	var last decide.Answer
	for i := 0; i < breakerTrip+3; i++ {
		var err error
		last, err = r.Decide(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := inner.calls.Load(); got != int64(breakerTrip) {
		t.Fatalf("inner saw %d calls, want %d", got, breakerTrip)
	}
	for name, rd := range last.Slots {
		if rd.Reading != decide.Unavailable {
			t.Errorf("slot %s read %q, want unavailable", name, rd.Reading)
		}
	}
}

// With a fallback behind it, an open breaker hands the slots to the next
// backend rather than to nobody.
func TestOpenBreakerFallsThroughToNextBackend(t *testing.T) {
	bad := failing(1, 1000)
	good := &scripted{name: "lappi", fn: func(_ int64, q decide.Question) (decide.Answer, error) { return answered(q, 0), nil }}
	r := decide.NewRouter(logErrors(bad, quiet, nil), logErrors(good, quiet, nil))
	for i := 0; i < breakerTrip+4; i++ {
		a, err := r.Decide(context.Background(), slotsQ("remote"))
		if err != nil || a.Slots["remote"].Reading != decide.Answered {
			t.Fatalf("call %d: %+v, %v", i+1, a, err)
		}
	}
	if bad.calls.Load() != int64(breakerTrip) {
		t.Fatalf("the failing backend saw %d calls, want %d", bad.calls.Load(), breakerTrip)
	}
}

// The ErrAuth contract: the router disables a backend whose error wraps
// ErrAuth, so the wrapper must hand that error through untouched.
func TestAuthErrorPassesThroughForTheRouter(t *testing.T) {
	inner := &scripted{fn: func(int64, decide.Question) (decide.Answer, error) {
		return decide.Answer{}, fmt.Errorf("anthropic: 401: %w", decide.ErrAuth)
	}}
	r := decide.NewRouter(logErrors(inner, quiet, nil))
	for i := 0; i < 5; i++ {
		if _, err := r.Decide(context.Background(), slotsQ("s")); err != nil {
			t.Fatal(err)
		}
	}
	if inner.calls.Load() != 1 {
		t.Fatalf("inner saw %d calls; the router must stop after the first ErrAuth", inner.calls.Load())
	}
	b := logErrors(&scripted{fn: func(int64, decide.Question) (decide.Answer, error) {
		return decide.Answer{}, fmt.Errorf("x: %w", decide.ErrAuth)
	}}, quiet, nil)
	if _, err := b.Decide(context.Background(), slotsQ("s")); !errors.Is(err, decide.ErrAuth) {
		t.Fatalf("err %v lost ErrAuth", err)
	}
}

// Every breakerProbeEvery-th call while open reaches the backend; a failed
// probe keeps it open, a good one closes it.
func TestBreakerHalfOpenProbe(t *testing.T) {
	var lines []string
	warnf := func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	// Errors on calls 1-3 and on the first probe (call 4), then recovers.
	inner := failing(1, 4)
	b := logErrors(inner, warnf, newBackendErrors())
	q := slotsQ("s")
	ask := func() error { _, err := b.Decide(context.Background(), q); return err }

	for i := 0; i < breakerTrip; i++ {
		ask()
	}
	for i := 1; i < breakerProbeEvery; i++ {
		if err := ask(); !errors.Is(err, errBreakerOpen) {
			t.Fatalf("open call %d: %v", i, err)
		}
	}
	if inner.calls.Load() != int64(breakerTrip) {
		t.Fatalf("inner saw %d calls before the probe", inner.calls.Load())
	}
	if err := ask(); !errors.Is(err, errDown) { // the probe, which fails
		t.Fatalf("probe err = %v, want the backend's error", err)
	}
	if inner.calls.Load() != int64(breakerTrip)+1 {
		t.Fatalf("the probe did not reach the backend: %d calls", inner.calls.Load())
	}
	for i := 1; i < breakerProbeEvery; i++ {
		if err := ask(); !errors.Is(err, errBreakerOpen) {
			t.Fatalf("after a failed probe, call %d: %v (still open expected)", i, err)
		}
	}
	if err := ask(); err != nil { // second probe answers
		t.Fatalf("second probe: %v", err)
	}
	for i := 0; i < 5; i++ { // closed: every call goes through
		if err := ask(); err != nil {
			t.Fatalf("closed call %d: %v", i, err)
		}
	}
	var open, closed int
	for _, l := range lines {
		switch {
		case strings.Contains(l, "breaker open"):
			open++
		case strings.Contains(l, "breaker closed"):
			closed++
		}
	}
	if open != 1 || closed != 1 {
		t.Fatalf("lines %v: want one open and one closed", lines)
	}
}

// One answered call resets the streak: errors that are not consecutive never
// open the breaker.
func TestAnsweredCallResetsTheStreak(t *testing.T) {
	inner := &scripted{fn: func(n int64, q decide.Question) (decide.Answer, error) {
		if n%3 == 0 {
			return answered(q, 0), nil
		}
		return decide.Answer{}, errDown
	}}
	b := logErrors(inner, quiet, nil)
	for i := 0; i < 60; i++ {
		b.Decide(context.Background(), slotsQ("s"))
	}
	if inner.calls.Load() != 60 {
		t.Fatalf("inner saw %d of 60 calls: a streak of 2 opened the breaker", inner.calls.Load())
	}
}

// A cancelled call is the run ending, not the backend failing: it neither
// opens the breaker nor resets a streak. A timeout is the backend being slow
// and does count. The run record's error list keeps recording both, as before.
func TestCancelledCallDoesNotTripOrResetTheBreaker(t *testing.T) {
	cancelled := fmt.Errorf("llmbackend: %w", context.Canceled)
	inner := &scripted{fn: func(n int64, q decide.Question) (decide.Answer, error) {
		if n <= 2 {
			return decide.Answer{}, errDown
		}
		return decide.Answer{}, cancelled
	}}
	sink := newBackendErrors()
	b := logErrors(inner, quiet, sink)
	for i := 0; i < 20; i++ {
		b.Decide(context.Background(), slotsQ("s"))
	}
	if inner.calls.Load() != 20 {
		t.Fatalf("inner saw %d of 20 calls: cancellations opened the breaker", inner.calls.Load())
	}
	st := sink.stats()["llm"]
	if st.Calls != 20 || st.Errors != 2 || st.Canceled != 18 {
		t.Fatalf("stats %+v, want 20 calls, 2 errors, 18 cancelled", st)
	}
	// The third real error finishes the streak the cancellations left at 2.
	inner.fn = func(int64, decide.Question) (decide.Answer, error) { return decide.Answer{}, errDown }
	b.Decide(context.Background(), slotsQ("s"))
	if _, err := b.Decide(context.Background(), slotsQ("s")); !errors.Is(err, errBreakerOpen) {
		t.Fatalf("err %v: the streak across cancellations should have opened it", err)
	}

	timeout := &scripted{fn: func(int64, decide.Question) (decide.Answer, error) {
		return decide.Answer{}, fmt.Errorf("slow: %w", context.DeadlineExceeded)
	}}
	tb := logErrors(timeout, quiet, nil)
	for i := 0; i < breakerTrip+1; i++ {
		tb.Decide(context.Background(), slotsQ("s"))
	}
	if timeout.calls.Load() != int64(breakerTrip) {
		t.Fatalf("timeouts: inner saw %d calls, want %d", timeout.calls.Load(), breakerTrip)
	}
}

// A real shutdown cancels the caller's context and the provider client may
// surface it as its own error that does not wrap context.Canceled. Eight
// workers all failing that way must not open the breaker.
func TestShutdownWithUnwrappedErrorDoesNotTripTheBreaker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sink := newBackendErrors()
	inner := &scripted{fn: func(int64, decide.Question) (decide.Answer, error) {
		return decide.Answer{}, errors.New("conn closed")
	}}
	b := logErrors(inner, quiet, sink)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				b.Decide(ctx, slotsQ("s"))
			}
		}()
	}
	wg.Wait()
	st := sink.stats()["llm"]
	if inner.calls.Load() != 40 || st.Rejected != 0 || st.BreakerOpens != 0 || st.Canceled != 40 || st.Errors != 0 {
		t.Fatalf("calls %d, stats %+v: a shutdown must not look like a dead backend", inner.calls.Load(), st)
	}
}

// Calls, reading kinds, cost and wall time are kept per backend name.
func TestStatsCountCallsReadingsCostAndWallTime(t *testing.T) {
	sink := newBackendErrors()
	if sink.stats() != nil {
		t.Fatal("a run with no call reports stats")
	}
	mixed := &scripted{fn: func(n int64, q decide.Question) (decide.Answer, error) {
		time.Sleep(2 * time.Millisecond)
		a := decide.Answer{CostUSD: 0.25, Slots: map[string]decide.SlotReading{}}
		kinds := []decide.Reading{decide.Answered, decide.Abstained, decide.Unavailable, decide.Failed, decide.NotAsked, decide.Refused}
		for i, s := range q.Slots {
			a.Slots[s.Name] = decide.SlotReading{Reading: kinds[i%len(kinds)]}
		}
		return a, nil
	}}
	other := &scripted{name: "lappi", fn: func(_ int64, q decide.Question) (decide.Answer, error) { return answered(q, 0), nil }}
	m := logErrors(mixed, quiet, sink)
	o := logErrors(other, quiet, sink)
	for i := 0; i < 4; i++ {
		m.Decide(context.Background(), slotsQ("a", "b", "c", "d", "e", "f"))
	}
	o.Decide(context.Background(), slotsQ("a"))
	st := sink.stats()
	l := st["llm"]
	if l.Calls != 4 || l.Errors != 0 {
		t.Fatalf("llm stats %+v", l)
	}
	for _, k := range []decide.Reading{decide.Answered, decide.Abstained, decide.Unavailable, decide.Failed, decide.NotAsked, decide.Refused} {
		if l.Readings[string(k)] != 4 {
			t.Errorf("readings[%s] = %d, want 4 (%v)", k, l.Readings[string(k)], l.Readings)
		}
	}
	if math.Abs(l.CostUSD-1.0) > 1e-9 {
		t.Errorf("cost %v, want 1.0", l.CostUSD)
	}
	if l.WallMS < 8 {
		t.Errorf("wall %d ms, want at least 8 (4 calls of 2ms)", l.WallMS)
	}
	if st["lappi"].Calls != 1 || st["lappi"].Readings["answered"] != 1 {
		t.Errorf("lappi stats %+v", st["lappi"])
	}
}

// A negative, NaN or infinite cost from a backend must not poison the sum; a
// reading kind the contract does not define must not grow the map.
func TestStatsRefuseBadCostAndUnknownKinds(t *testing.T) {
	sink := newBackendErrors()
	costs := []float64{0.5, math.NaN(), -3, math.Inf(1), math.Inf(-1), 0.25}
	inner := &scripted{fn: func(n int64, q decide.Question) (decide.Answer, error) {
		a := answered(q, costs[n-1])
		a.Slots["s"] = decide.SlotReading{Reading: decide.Reading(fmt.Sprintf("weird-%d", n))}
		return a, nil
	}}
	b := logErrors(inner, quiet, sink)
	for range costs {
		b.Decide(context.Background(), slotsQ("s"))
	}
	st := sink.stats()["llm"]
	if st.CostUSD != 0.75 {
		t.Fatalf("cost %v, want 0.75", st.CostUSD)
	}
	if len(st.Readings) != 1 || st.Readings["unknown"] != 6 {
		t.Fatalf("readings %v, want only unknown:6", st.Readings)
	}
	if _, err := json.Marshal(sink.stats()); err != nil {
		t.Fatalf("stats do not marshal: %v", err)
	}
}

// A failed call still costs what the backend says it cost (the router counts
// it the same way) and still takes wall time.
func TestStatsCountAFailedCallsCost(t *testing.T) {
	sink := newBackendErrors()
	inner := &scripted{fn: func(int64, decide.Question) (decide.Answer, error) {
		return decide.Answer{CostUSD: 0.1}, errDown
	}}
	logErrors(inner, quiet, sink).Decide(context.Background(), slotsQ("s"))
	if st := sink.stats()["llm"]; st.Calls != 1 || st.Errors != 1 || st.CostUSD != 0.1 || len(st.Readings) != 0 {
		t.Fatalf("stats %+v", st)
	}
}

// Rejected calls are counted apart from calls, so the record shows what the
// breaker saved.
func TestStatsCountBreakerRejections(t *testing.T) {
	sink := newBackendErrors()
	b := logErrors(failing(1, 1000), quiet, sink)
	for i := 0; i < breakerTrip+5; i++ {
		b.Decide(context.Background(), slotsQ("s"))
	}
	st := sink.stats()["llm"]
	if st.Calls != breakerTrip || st.Errors != breakerTrip || st.Rejected != 5 || st.BreakerOpens != 1 {
		t.Fatalf("stats %+v", st)
	}
	// Rejections are not backend errors: the cause list still shows only the
	// real one.
	r := sink.report()
	if len(r.Causes) != 1 || r.Causes[0].Count != breakerTrip {
		t.Fatalf("causes %+v", r.Causes)
	}
}

// Concurrent workers: every call is counted exactly once, and the breaker
// admits no more than one probe at a time.
func TestConcurrentCallsKeepExactCounts(t *testing.T) {
	sink := newBackendErrors()
	inner := &scripted{fn: func(_ int64, q decide.Question) (decide.Answer, error) { return answered(q, 0.5), nil }}
	b := logErrors(inner, quiet, sink)
	const workers, each = 16, 200
	var wg sync.WaitGroup
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				b.Decide(context.Background(), slotsQ("a", "b"))
			}
		}()
	}
	wg.Wait()
	st := sink.stats()["llm"]
	if st.Calls != workers*each || st.Readings["answered"] != 2*workers*each || st.CostUSD != 0.5*workers*each {
		t.Fatalf("stats %+v, want %d calls", st, workers*each)
	}
}

func TestConcurrentFailuresOpenOnceAndProbeOneAtATime(t *testing.T) {
	var inFlight, maxInFlight atomic.Int64
	gate := make(chan struct{})
	inner := &scripted{fn: func(n int64, _ decide.Question) (decide.Answer, error) {
		if n > int64(breakerTrip) { // probes
			cur := inFlight.Add(1)
			for {
				m := maxInFlight.Load()
				if cur <= m || maxInFlight.CompareAndSwap(m, cur) {
					break
				}
			}
			<-gate
			inFlight.Add(-1)
		}
		return decide.Answer{}, errDown
	}}
	var opens atomic.Int64
	b := logErrors(inner, func(f string, a ...any) {
		if strings.Contains(fmt.Sprintf(f, a...), "breaker open") {
			opens.Add(1)
		}
	}, newBackendErrors())
	for i := 0; i < breakerTrip; i++ {
		b.Decide(context.Background(), slotsQ("s"))
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5*breakerProbeEvery; i++ {
				b.Decide(context.Background(), slotsQ("s"))
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	if maxInFlight.Load() > 1 {
		t.Fatalf("%d probes in flight at once", maxInFlight.Load())
	}
	if opens.Load() != 1 {
		t.Fatalf("%d open lines, want 1 (a failed probe is not a new opening)", opens.Load())
	}
}

// Zero slots requested: nothing to read, and nothing breaks.
func TestZeroSlotsAreSafe(t *testing.T) {
	sink := newBackendErrors()
	inner := failing(1, 1000)
	b := logErrors(inner, quiet, sink)
	for i := 0; i < breakerTrip+1; i++ {
		a, _ := b.Decide(context.Background(), decide.Question{Task: "t"})
		if len(a.Slots) != 0 {
			t.Fatalf("slots %v from a question with none", a.Slots)
		}
	}
	if st := sink.stats()["llm"]; st.Calls != breakerTrip || st.Rejected != 1 {
		t.Fatalf("stats %+v", st)
	}
}

// The router recovers a backend that panics (decide.call). The wrapper must not
// swallow that, and must not leave a half-open probe marked in flight: a panic
// is a failed call.
func TestPanickingBackendCountsAsFailureAndLeavesNoStuckProbe(t *testing.T) {
	inner := &scripted{fn: func(int64, decide.Question) (decide.Answer, error) { panic("boom") }}
	b := logErrors(inner, quiet, newBackendErrors())
	call := func() (panicked bool) {
		defer func() { panicked = recover() != nil }()
		b.Decide(context.Background(), slotsQ("s"))
		return false
	}
	for i := 0; i < breakerTrip; i++ {
		if !call() {
			t.Fatalf("call %d: the panic was swallowed", i+1)
		}
	}
	for i := 1; i < breakerProbeEvery; i++ {
		if call() {
			t.Fatalf("open call %d reached the inner backend", i)
		}
	}
	if !call() { // the probe panics...
		t.Fatal("the probe did not reach the backend")
	}
	for i := 1; i < breakerProbeEvery; i++ {
		call()
	}
	if !call() { // ...and the next probe is still admitted
		t.Fatal("a panicking probe left the breaker unable to probe again")
	}
}

// The new field is additive: the run record keeps the old fields as they were
// and carries the stats under their own key.
func TestRunDetailCarriesBackendStats(t *testing.T) {
	sink := newBackendErrors()
	b := logErrors(failing(1, 3), quiet, sink)
	for i := 0; i < 6; i++ {
		b.Decide(context.Background(), slotsQ("s"))
	}
	d := runDetail{BackendErrors: sink.report(), BackendStats: sink.stats()}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["backend_errors"]; !ok {
		t.Error("backend_errors disappeared from the record")
	}
	var stats map[string]struct {
		Calls        int            `json:"calls"`
		Errors       int            `json:"errors"`
		Rejected     int            `json:"breaker_rejected"`
		BreakerOpens int            `json:"breaker_opens"`
		Readings     map[string]int `json:"readings"`
		CostUSD      float64        `json:"cost_usd"`
		WallMS       int64          `json:"wall_ms"`
	}
	if err := json.Unmarshal(got["backend_stats"], &stats); err != nil {
		t.Fatalf("backend_stats missing or malformed in %s: %v", raw, err)
	}
	s := stats["llm"]
	if s.Calls != 3 || s.Errors != 3 || s.Rejected != 3 || s.BreakerOpens != 1 {
		t.Fatalf("stats %+v", s)
	}
	if empty, _ := json.Marshal(runDetail{}); string(empty) != "{}" {
		t.Fatalf("an empty detail marshals as %s; the new field must be omitted when unset", empty)
	}
}

// A nil sink (commands outside a run) still gets the breaker.
func TestNilSinkStillBreaks(t *testing.T) {
	inner := failing(1, 1000)
	b := logErrors(inner, quiet, nil)
	for i := 0; i < breakerTrip+3; i++ {
		b.Decide(context.Background(), slotsQ("s"))
	}
	if inner.calls.Load() != int64(breakerTrip) {
		t.Fatalf("inner saw %d calls", inner.calls.Load())
	}
	var none *backendErrors
	if none.stats() != nil {
		t.Fatal("a nil sink reports stats")
	}
}

// The number of backends one run keeps stats for is bounded.
func TestStatsAreBoundedByBackendName(t *testing.T) {
	sink := newBackendErrors()
	for i := 0; i < maxStatBackends+10; i++ {
		inner := &scripted{name: fmt.Sprintf("b%d", i), fn: func(_ int64, q decide.Question) (decide.Answer, error) { return answered(q, 0), nil }}
		logErrors(inner, quiet, sink).Decide(context.Background(), slotsQ("s"))
	}
	st := sink.stats()
	if len(st) != maxStatBackends+1 || st[otherBackend].Calls != 10 {
		t.Fatalf("%d entries, other = %+v", len(st), st[otherBackend])
	}
}
