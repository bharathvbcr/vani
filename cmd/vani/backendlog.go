package main

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/decide"
)

// maxLoggedBackendErrors bounds how many distinct backend errors one run prints.
const maxLoggedBackendErrors = 5

// maxRecordedBackendErrors bounds how many distinct backend errors one run
// keeps for the run record; calls failing with a further cause are counted in
// backendErrorReport.Unrecorded, never dropped silently.
const maxRecordedBackendErrors = 20

// maxBackendErrorRunes bounds one error message, printed or recorded.
const maxBackendErrorRunes = 400

// Circuit breaker defaults. A backend whose last breakerTrip calls all errored
// is not called again: its slots read Unavailable at once, and only every
// breakerProbeEvery-th call while it is open goes through to see whether it is
// back. Each run builds its own wrappers, so the breaker starts closed.
const (
	breakerTrip       = 3
	breakerProbeEvery = 10
	// maxBreakerLogs bounds the open/close lines one backend prints in a run,
	// however often it flaps.
	maxBreakerLogs = 10
)

// errBreakerOpen is what a rejected call returns. It is deliberately not
// decide.ErrAuth: the router disables an ErrAuth backend for the rest of the
// process, and a breaker that probes must be allowed to recover.
var errBreakerOpen = errors.New("backend breaker open: recent calls all failed, call skipped")

// errorLogged is the one decorator every decision backend is wrapped in. It:
//
//   - reports each distinct error the backend returns, once per process, and
//     counts every one into the run's backendErrors. The router turns a backend
//     error into an "unavailable" reading and keeps no text, so without this a
//     run where every call is refused (a bad key, an invalid request, no credit)
//     shows only "no decision backend could read the posting" with no cause.
//     Provider errors carry status and API message, never the request body or
//     the key (Manvi redacts secrets);
//   - meters the backend into the same sink: calls, readings by kind, cost and
//     wall time per backend name;
//   - opens a breaker after breakerTrip consecutive errored calls, so a dead
//     backend stops costing a timeout per posting while the run's unread streak
//     is still counting down.
type errorLogged struct {
	decide.Backend
	warnf func(format string, a ...any)
	sink  *backendErrors // nil: print only

	mu   sync.Mutex
	seen map[string]bool

	br breaker
}

// breaker is the consecutive-error circuit breaker of one wrapper. Safe for
// concurrent use: backends are asked from concurrent workers.
type breaker struct {
	trip       int // consecutive errors that open it; <= 0 disables it
	probeEvery int // while open, every probeEvery-th call is a probe

	mu        sync.Mutex
	streak    int
	open      bool
	sinceOpen int  // calls turned away since it opened or last probed
	probing   bool // a probe is in flight; at most one at a time
	logged    int  // open/close lines printed
}

// outcome is how a call that reached the backend ended, for the breaker.
type outcome int

const (
	callOK       outcome = iota // no error: the backend is working
	callErrored                 // an error, a timeout or a panic
	callCanceled                // the caller gave up; says nothing about the backend
)

// admit decides whether a call may reach the backend. probe is true when this
// call is the half-open probe.
func (b *breaker) admit() (allowed, probe bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.open {
		return true, false
	}
	if b.probing {
		return false, false
	}
	b.sinceOpen++
	if b.sinceOpen < b.probeEvery {
		return false, false
	}
	b.sinceOpen, b.probing = 0, true
	return true, true
}

// settle records how an admitted call ended. opened and closed report a state
// change; say is true when that change is to be printed.
func (b *breaker) settle(probe bool, o outcome) (opened, closed, say bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if probe {
		b.probing = false
	}
	switch o {
	case callOK:
		b.streak = 0
		if b.open {
			b.open, b.sinceOpen, closed = false, 0, true
		}
	case callErrored:
		b.streak++
		switch {
		case b.open:
			b.sinceOpen = 0 // a failed probe waits a full interval for the next
		case b.trip > 0 && b.streak >= b.trip:
			b.open, b.sinceOpen, opened = true, 0, true
		}
	case callCanceled:
		if probe { // an abandoned probe does not cost another interval
			b.sinceOpen = b.probeEvery - 1
		}
	}
	if (opened || closed) && b.logged < maxBreakerLogs {
		b.logged++
		say = true
	}
	return opened, closed, say
}

func logErrors(b decide.Backend, warnf func(format string, a ...any), sink *backendErrors) *errorLogged {
	return &errorLogged{
		Backend: b, warnf: warnf, sink: sink, seen: map[string]bool{},
		br: breaker{trip: breakerTrip, probeEvery: breakerProbeEvery},
	}
}

func (e *errorLogged) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	name := e.Backend.Name()
	allowed, probe := e.br.admit()
	if !allowed {
		e.sink.reject(name)
		return breakerAnswer(name, q), errBreakerOpen
	}
	start := time.Now()
	returned := false
	defer func() {
		if returned {
			return
		}
		// The backend panicked (the router contains that as an error): the call
		// failed, and a probe must not stay marked in flight.
		e.sink.record(name, decide.Answer{}, true, false, time.Since(start))
		e.settle(name, probe, callErrored)
	}()
	a, err := e.Backend.Decide(ctx, q)
	returned = true

	o := callOK
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled), errors.Is(ctx.Err(), context.Canceled):
		// The caller's own context is the reliable signal: a provider client may
		// surface a cancelled request as its own "connection closed" error that
		// does not wrap context.Canceled. A timeout (DeadlineExceeded) is the
		// backend being slow, and still counts.
		o = callCanceled
	default:
		o = callErrored
	}
	e.sink.record(name, a, err != nil, o == callCanceled, time.Since(start))
	if err != nil {
		msg := truncateRunes(err.Error(), maxBackendErrorRunes)
		key := pipeline.ErrorKey(msg)
		e.sink.add(name, key, msg)
		e.mu.Lock()
		first := !e.seen[key] && len(e.seen) < maxLoggedBackendErrors
		if first {
			e.seen[key] = true
		}
		e.mu.Unlock()
		if first {
			e.warnf("%s backend error: %s", name, msg)
		}
	}
	e.settle(name, probe, o)
	return a, err
}

// settle feeds the breaker and prints and counts its state changes.
func (e *errorLogged) settle(name string, probe bool, o outcome) {
	opened, closed, say := e.br.settle(probe, o)
	if opened {
		e.sink.opened(name)
	}
	if !say {
		return
	}
	if opened {
		e.warnf("%s backend breaker open after %d consecutive errors; skipping its calls, probing every %d",
			name, e.br.trip, e.br.probeEvery)
	} else if closed {
		e.warnf("%s backend breaker closed: the backend answered", name)
	}
}

// breakerAnswer is the reading a turned-away call gets: Unavailable on every
// slot asked, never NotAsked, so a posting nothing else could read stays Live
// and is read again on a later run instead of being recorded as unsure.
func breakerAnswer(name string, q decide.Question) decide.Answer {
	a := decide.Answer{Backend: name, Slots: make(map[string]decide.SlotReading, len(q.Slots))}
	for _, s := range q.Slots {
		a.Slots[s.Name] = decide.SlotReading{Reading: decide.Unavailable, Detail: "breaker_open"}
	}
	return a
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// backendErrors collects one run's decision-backend errors for the run record:
// every failed call is counted, by backend and cause. Safe for concurrent use;
// a nil *backendErrors ignores everything.
type backendErrors struct {
	mu         sync.Mutex
	causes     map[backendCause]*backendErrorCause
	order      []backendCause
	unrecorded map[string]int
	meter      map[string]*backendStat
}

// maxStatBackends bounds how many backend names one run keeps stats for; calls
// of any further name are folded into otherBackend, never dropped.
const maxStatBackends = 16

const otherBackend = "(other)"

// backendStat is what one backend did in one run, as the run record shows it.
type backendStat struct {
	// Calls reached the backend. Errors failed; Canceled were abandoned by the
	// caller (not errors, and not counted by the breaker).
	Calls    int `json:"calls"`
	Errors   int `json:"errors,omitempty"`
	Canceled int `json:"canceled,omitempty"`
	// Rejected calls were turned away by the open breaker without reaching the
	// backend; BreakerOpens is how many times it opened.
	Rejected     int `json:"breaker_rejected,omitempty"`
	BreakerOpens int `json:"breaker_opens,omitempty"`
	// Readings counts slot readings by kind (decide.Reading), from calls that
	// returned an answer.
	Readings map[string]int `json:"readings,omitempty"`
	// CostUSD sums the backend's reported cost; a negative, NaN or infinite
	// figure counts as zero.
	CostUSD float64 `json:"cost_usd"`
	WallMS  int64   `json:"wall_ms"`

	wall time.Duration
}

type backendCause struct{ backend, key string }

// backendErrorCause is one distinct error as the run record shows it.
type backendErrorCause struct {
	Backend string `json:"backend"`
	// Count is how many calls failed this way in the run.
	Count int `json:"count"`
	// Example is the first such error, request id included.
	Example string `json:"example"`
}

// backendErrorReport is a backend's failures in one run.
type backendErrorReport struct {
	Causes []backendErrorCause `json:"causes"`
	// Unrecorded counts failed calls whose cause is not in Causes because the
	// run already held maxRecordedBackendErrors distinct causes.
	Unrecorded int `json:"unrecorded,omitempty"`
}

func newBackendErrors() *backendErrors {
	return &backendErrors{causes: map[backendCause]*backendErrorCause{}, unrecorded: map[string]int{}, meter: map[string]*backendStat{}}
}

// stat returns the backend's counters, creating them; the caller holds b.mu.
func (b *backendErrors) stat(backend string) *backendStat {
	if b.meter == nil {
		b.meter = map[string]*backendStat{}
	}
	if st, ok := b.meter[backend]; ok {
		return st
	}
	if len(b.meter) >= maxStatBackends {
		backend = otherBackend
		if st, ok := b.meter[backend]; ok {
			return st
		}
	}
	st := &backendStat{}
	b.meter[backend] = st
	return st
}

// readingKind maps a reading to a bounded set of keys: a backend that returns
// a kind the contract does not define cannot grow the record.
func readingKind(r decide.Reading) string {
	switch r {
	case decide.Answered, decide.Abstained, decide.Refused, decide.Failed, decide.Unavailable, decide.NotAsked:
		return string(r)
	}
	return "unknown"
}

// record meters one call that reached the backend. The answer's cost counts
// even when the call errored (the router does the same); its readings count
// only when it did not, since a failed call's readings are discarded.
func (b *backendErrors) record(backend string, a decide.Answer, errored, canceled bool, wall time.Duration) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.stat(backend)
	st.Calls++
	switch {
	case canceled:
		st.Canceled++
	case errored:
		st.Errors++
	}
	st.wall += wall
	if c := a.CostUSD; !math.IsNaN(c) && !math.IsInf(c, 0) && c > 0 {
		if sum := st.CostUSD + c; !math.IsInf(sum, 0) {
			st.CostUSD = sum
		}
	}
	if errored {
		return
	}
	for _, rd := range a.Slots {
		if st.Readings == nil {
			st.Readings = map[string]int{}
		}
		st.Readings[readingKind(rd.Reading)]++
	}
}

// reject counts a call the open breaker turned away.
func (b *backendErrors) reject(backend string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.stat(backend).Rejected++
	b.mu.Unlock()
}

// opened counts one opening of the backend's breaker.
func (b *backendErrors) opened(backend string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.stat(backend).BreakerOpens++
	b.mu.Unlock()
}

// stats returns a copy of the run's per-backend meter, or nil when no backend
// was called.
func (b *backendErrors) stats() map[string]backendStat {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.meter) == 0 {
		return nil
	}
	out := make(map[string]backendStat, len(b.meter))
	for name, st := range b.meter {
		c := *st
		c.WallMS = st.wall.Milliseconds()
		if st.Readings != nil {
			c.Readings = make(map[string]int, len(st.Readings))
			for k, v := range st.Readings {
				c.Readings[k] = v
			}
		}
		out[name] = c
	}
	return out
}

func (b *backendErrors) add(backend, key, msg string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	c := backendCause{backend, key}
	if got, ok := b.causes[c]; ok {
		got.Count++
		return
	}
	if len(b.causes) >= maxRecordedBackendErrors {
		b.unrecorded[backend]++
		return
	}
	b.causes[c] = &backendErrorCause{Backend: backend, Count: 1, Example: msg}
	b.order = append(b.order, c)
}

// report returns the run's errors, most frequent first, or nil when no call
// failed.
func (b *backendErrors) report() *backendErrorReport {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.order) == 0 && len(b.unrecorded) == 0 {
		return nil
	}
	r := &backendErrorReport{Causes: make([]backendErrorCause, 0, len(b.order))}
	for _, c := range b.order {
		r.Causes = append(r.Causes, *b.causes[c])
	}
	sort.SliceStable(r.Causes, func(i, j int) bool { return r.Causes[i].Count > r.Causes[j].Count })
	for _, n := range b.unrecorded {
		r.Unrecorded += n
	}
	return r
}
