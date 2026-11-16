package srcutil

import (
	"context"
	"errors"
	"io"
	"net/http"
	"syscall"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
)

// Transient reports whether err is a request failure the next attempt, or the
// next run, may well not see: a timeout (Apple's and JPMorgan's detail pages
// timed out awaiting headers on 2026-11-16 and answered in the runs either
// side), a connection reset or cut short, or a gateway status that outlived
// its retries (retryableStatus, less 429). It is false for anything that is an
// answer: a 500, a 404, a 429 (the host's cooldown owns those), a body over its
// bound, an unexpected layout, an egress policy refusal, and a cancellation.
//
// A timeout here includes a context deadline: a caller that gave a request
// its own deadline (greenhouse pay, ashby form) sees that expiry as a timeout.
// Whether the run itself is over is the caller's question (ctx.Err()), and
// both users of Transient ask it first.
func Transient(err error) bool {
	switch {
	case err == nil,
		errors.Is(err, context.Canceled),
		errors.Is(err, ErrRateLimited),
		errors.Is(err, egress.ErrNotAllowed),
		errors.Is(err, egress.ErrScheme),
		errors.Is(err, egress.ErrBlockedAddress),
		errors.Is(err, egress.ErrTooManyRedirects):
		return false
	}
	var se *egress.StatusError
	if errors.As(err, &se) {
		return se.Code != http.StatusTooManyRequests && retryableStatus(se.Code)
	}
	return timedOut(err) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, io.ErrUnexpectedEOF)
}

// timedOut reports whether any error in err's chain says it timed out. The
// first net.Error in the chain is not enough: a *url.Error asks only its
// direct cause, so a timeout wrapped once more (http2's "timeout awaiting
// response headers") reads as no timeout there.
func timedOut(err error) bool {
	var walk func(error, int) bool
	walk = func(e error, depth int) bool {
		if e == nil || depth > 32 {
			return false
		}
		if t, ok := e.(interface{ Timeout() bool }); ok && t.Timeout() {
			return true
		}
		switch u := e.(type) {
		case interface{ Unwrap() error }:
			return walk(u.Unwrap(), depth+1)
		case interface{ Unwrap() []error }:
			for _, c := range u.Unwrap() {
				if walk(c, depth+1) {
					return true
				}
			}
		}
		return false
	}
	return walk(err, 0)
}

// maxTransportRetries is how many times one request is sent again after a
// transient transport failure (Transient, with no response). One: a timed-out
// attempt has already spent the client's whole timeout, which no retry budget
// sees, so a second retry would double what a dead host costs.
const maxTransportRetries = 1

// retryTransport reports whether a request that failed with err and no
// response is sent again, and waits the backoff first. Only an idempotent
// request with no body (GET, HEAD) is retried: a POST may have been acted on
// before its answer was lost. A throttled host's probe gets its one attempt,
// as does a host that is rate limited (hostLimitedWhy) or that did not answer a
// request and its retry earlier this run (hostSilent): sending to either again
// would cost another timeout for a host that has just shown it will not
// answer. A run that is over, or a budget that cannot pay the wait, ends it.
//
// err is the failure of the attempt that was just made, whether Do's or the
// read of a 200's body; host is the request's lower-cased host.
func retryTransport(ctx context.Context, host, method, probeWhy string, retries int, waited *time.Duration, err error) bool {
	if retries >= maxTransportRetries || probeWhy != "" || ctx.Err() != nil || !Transient(err) {
		return false
	}
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	if _, limited := hostLimitedWhy(ctx, host); limited || hostSilent(ctx, host) {
		return false
	}
	wait := backoff(retries)
	if *waited+wait > maxRequestWait {
		return false
	}
	if ok, _ := spend(ctx, wait); !ok {
		return false
	}
	*waited += wait
	return sleep(ctx, wait) == nil
}

// answeredHTTP reports whether err ended a request the host answered with an
// HTTP status (a 404 as ErrBoardNotFound, any other as *egress.StatusError). A
// rate-limit refusal made from the host's memory sent nothing, so it is not an
// answer.
func answeredHTTP(err error) bool {
	var rl *RateLimitedError
	if errors.As(err, &rl) && rl.noRequest {
		return false
	}
	var se *egress.StatusError
	return errors.Is(err, ErrBoardNotFound) || errors.As(err, &se)
}

// noteTransportGiveUp records, on ctx's HostThrottle, that host did not answer
// a request that has now failed transiently after its transport retry (retries
// is how many were made). A failure that was never retried (a POST, a probe, a
// spent budget) proves nothing about the host and records nothing.
func noteTransportGiveUp(ctx context.Context, host string, retries int, err error) {
	if retries >= maxTransportRetries && ctx.Err() == nil && Transient(err) {
		noteSilent(ctx, host)
	}
}
