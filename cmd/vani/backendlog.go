package main

import (
	"context"
	"regexp"
	"sync"

	"github.com/bharathvbcr/decide"
)

// maxLoggedBackendErrors bounds how many distinct backend errors one run prints.
const maxLoggedBackendErrors = 5

// errorLogged reports each distinct error a decision backend returns, once per
// process. The router turns a backend error into an "unavailable" reading and
// keeps no text, so without this a run where every call is refused (a bad key,
// an invalid request, no credit) shows only "no decision backend could read the
// posting" with no cause. Provider errors carry status and API message, never
// the request body or the key (Manvi redacts secrets).
type errorLogged struct {
	decide.Backend
	warnf func(format string, a ...any)

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

func logErrors(b decide.Backend, warnf func(format string, a ...any)) *errorLogged {
	return &errorLogged{Backend: b, warnf: warnf, seen: map[string]bool{}}
}

func (e *errorLogged) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	a, err := e.Backend.Decide(ctx, q)
	if err != nil {
		msg := err.Error()
		if len(msg) > 400 {
			msg = msg[:400]
		}
		key := errorKey(msg)
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
