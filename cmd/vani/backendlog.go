package main

import (
	"context"
	"regexp"
	"sort"
	"sync"

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

// errorLogged reports each distinct error a decision backend returns, once per
// process, and counts every one into the run's backendErrors. The router turns
// a backend error into an "unavailable" reading and keeps no text, so without
// this a run where every call is refused (a bad key, an invalid request, no
// credit) shows only "no decision backend could read the posting" with no
// cause. Provider errors carry status and API message, never the request body
// or the key (Manvi redacts secrets).
type errorLogged struct {
	decide.Backend
	warnf func(format string, a ...any)
	sink  *backendErrors // nil: print only

	mu   sync.Mutex
	seen map[string]bool
}

// requestID matches a provider request id (Anthropic "req_…", OpenRouter
// "gen-…"), which differs on every call.
var requestID = regexp.MustCompile(`\b(req|gen)[_-][A-Za-z0-9_-]+`)

// errorKey is the identity of a backend error for de-duplication: the same
// refusal with a new request id is the same error. The first message is still
// printed whole, request id included.
func errorKey(msg string) string { return requestID.ReplaceAllString(msg, "$1_*") }

func logErrors(b decide.Backend, warnf func(format string, a ...any), sink *backendErrors) *errorLogged {
	return &errorLogged{Backend: b, warnf: warnf, sink: sink, seen: map[string]bool{}}
}

func (e *errorLogged) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	a, err := e.Backend.Decide(ctx, q)
	if err != nil {
		msg := truncateRunes(err.Error(), maxBackendErrorRunes)
		key := errorKey(msg)
		e.sink.add(e.Backend.Name(), key, msg)
		e.mu.Lock()
		first := !e.seen[key] && len(e.seen) < maxLoggedBackendErrors
		if first {
			e.seen[key] = true
		}
		e.mu.Unlock()
		if first {
			e.warnf("%s backend error: %s", e.Backend.Name(), msg)
		}
	}
	return a, err
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
	return &backendErrors{causes: map[backendCause]*backendErrorCause{}, unrecorded: map[string]int{}}
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
