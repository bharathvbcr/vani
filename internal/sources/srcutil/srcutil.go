// Package srcutil holds the pieces every ATS adapter shares: board
// validation, a bounded JSON GET over egress.Doer, and the limits that keep a
// hostile or broken board from exhausting memory.
package srcutil

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

const (
	// MaxPostings is the most postings one board may report. A board above it
	// is an error: silently dropping the tail would hide real postings.
	MaxPostings = 5000
	// MaxDescription is the byte cap on one posting's description text.
	MaxDescription = 64 << 10
	// MaxBody is the byte cap on one API response body.
	MaxBody = 64 << 20
)

// maxBody is MaxBody; a variable only so the test can exercise the cap
// without allocating 64 MiB.
var maxBody int64 = MaxBody

var (
	// ErrBoardNotFound is returned when the ATS answers 404 for the board.
	ErrBoardNotFound = errors.New("sources: board not found")
	// ErrTooManyPostings is returned when a board exceeds MaxPostings.
	ErrTooManyPostings = errors.New("sources: board exceeds posting limit")
	// ErrBodyTooLarge is returned when a response body exceeds the body cap.
	ErrBodyTooLarge = errors.New("sources: response body too large")
)

// CheckBoard refuses a board that belongs to another ATS or has no token.
func CheckBoard(b sources.Board, want posting.ATS) error {
	if b.ATS != want {
		return fmt.Errorf("sources: board ats %q is not %q", b.ATS, want)
	}
	// The same rule as config.ValidBoard (sources.TokenProblem). The key
	// builders refuse ':' too, but only after the board's first fetch.
	if why := sources.TokenProblem(b.Token); why != "" {
		return fmt.Errorf("sources: board token %q %s", b.Token, why)
	}
	if b.BoardName != "" {
		if want != posting.Ashby {
			return fmt.Errorf("sources: board_name applies to ab only, not %s/%s", b.ATS, b.Token)
		}
		if why := sources.BoardNameProblem(b.BoardName); why != "" {
			return fmt.Errorf("sources: board %q board_name %q %s", b.Token, b.BoardName, why)
		}
	}
	return nil
}

// CheckCount returns ErrTooManyPostings when n exceeds MaxPostings.
func CheckCount(n int) error {
	if n > MaxPostings {
		return fmt.Errorf("%w: %d > %d", ErrTooManyPostings, n, MaxPostings)
	}
	return nil
}

// CheckURL accepts an empty string or an absolute http(s) URL. Posting URLs
// come from untrusted payloads and later reach a digest, so other schemes
// (javascript:, file:, data:) are refused here, and so is userinfo:
// "https://jobs.acme.com@evil.test/" reads as acme's but opens evil.test.
func CheckURL(field, raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("sources: %s is not an absolute http(s) url", field)
	}
	if u.User != nil {
		return fmt.Errorf("sources: %s carries userinfo", field)
	}
	return nil
}

// ParseTime parses an RFC 3339 timestamp; an empty string is the zero time.
func ParseTime(field, s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("sources: %s: %w", field, err)
	}
	return t, nil
}

// Retry bounds for a board that asks the caller to slow down. A 429 (or a 503)
// part-way through a large listing would otherwise fail the whole board; a
// Retry-After longer than maxRetryWait is honoured by giving up, not by
// waiting, so one board cannot stall a run. Three things bound the waiting:
// maxRetryWait for any one wait, maxRequestWait for all the waits of one
// request, and the board's budget (WithRetryBudget) for all of its requests.
const (
	maxAttempts    = 5
	maxRetryWait   = 30 * time.Second
	maxRequestWait = time.Minute
	// backoffBase is the wait before the first retry when the response names
	// none; each retry doubles it, up to maxBackoff, before jitter.
	backoffBase = 2 * time.Second
	maxBackoff  = 20 * time.Second

	// BoardRetryBudget is the most one board may spend waiting to retry,
	// across every request it makes (see WithRetryBudget).
	BoardRetryBudget = 2 * time.Minute
)

// The variables below are the injection points for tests: they never change
// in production, and tests replace them so nothing waits in real time.
var (
	sleep = func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	now = time.Now
	// jitter spreads a backoff over [d/2, d] ("equal jitter"), so a daemon's
	// retries do not fall in step with anyone else's and a wait is never zero.
	jitter = func(d time.Duration) time.Duration {
		half := d / 2
		if half <= 0 {
			return d
		}
		return half + rand.N(half+1)
	}
)

// backoff is the wait before retry n (0 is the first) when the response names
// none: backoffBase doubling to maxBackoff, jittered.
func backoff(n int) time.Duration {
	d := maxBackoff
	if n < 8 && backoffBase<<n < maxBackoff {
		d = backoffBase << n
	}
	return jitter(d)
}

// ErrRateLimited marks a request still answered 429 after the retries the
// bounds allow. Test with errors.Is; errors.As to *RateLimitedError for the
// detail, or to *egress.StatusError for the status.
var ErrRateLimited = errors.New("sources: rate limited")

// RateLimitedError is a host that kept answering 429. The board is not broken:
// the caller should leave its postings as they are and try again next run.
type RateLimitedError struct {
	Host     string
	Method   string
	Path     string
	Attempts int
	// Why is the bound that ended the retrying.
	Why    string
	Status *egress.StatusError
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("sources: rate limited by %s (HTTP 429), retry next run (%s %s: %s)", e.Host, e.Method, e.Path, e.Why)
}

// Unwrap exposes the 429 as an *egress.StatusError, which callers already read.
func (e *RateLimitedError) Unwrap() error { return e.Status }

// Is makes errors.Is(err, ErrRateLimited) true.
func (e *RateLimitedError) Is(target error) bool { return target == ErrRateLimited }

type budgetKey struct{}

// retryBudget is the wait one board has left to spend on retries.
type retryBudget struct {
	mu   sync.Mutex
	left time.Duration
}

// WithRetryBudget returns ctx carrying a budget of total for the time requests
// spend waiting to retry. A wait the budget cannot cover is not taken: the
// request gives up with the status. One board, one budget: a board that keeps
// being rate limited cannot hold up a run by retrying request after request.
// Without a budget only the per-request bounds apply.
func WithRetryBudget(ctx context.Context, total time.Duration) context.Context {
	return context.WithValue(ctx, budgetKey{}, &retryBudget{left: total})
}

// RetryBudgetLeft is what ctx's retry budget has left, and false when ctx
// carries none.
func RetryBudgetLeft(ctx context.Context) (time.Duration, bool) {
	b, ok := ctx.Value(budgetKey{}).(*retryBudget)
	if !ok {
		return 0, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.left, true
}

// spend takes d from ctx's budget and reports whether it was there to take.
func spend(ctx context.Context, d time.Duration) bool {
	b, ok := ctx.Value(budgetKey{}).(*retryBudget)
	if !ok {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if d > b.left {
		return false
	}
	b.left -= d
	return true
}

// GetJSON issues a GET through d and decodes the JSON body into v. A 404 is
// ErrBoardNotFound, any other non-200 status is an error wrapping
// *egress.StatusError, and a body above MaxBody is ErrBodyTooLarge. A 429 or
// 503 is retried (see DoJSON).
func GetJSON(ctx context.Context, d egress.Doer, rawURL string, v any) error {
	return DoJSON(ctx, d, http.MethodGet, rawURL, nil, nil, v)
}

// GetJSONLimit is GetJSON with a body cap of limit bytes (at most MaxBody),
// for a per-posting request where a whole board's allowance is far too much.
func GetJSONLimit(ctx context.Context, d egress.Doer, rawURL string, limit int64, v any) error {
	if limit <= 0 || limit > maxBody {
		limit = maxBody
	}
	return doJSON(ctx, d, http.MethodGet, rawURL, nil, nil, limit, v)
}

// PostJSON POSTs body, marshalled as JSON, and decodes the reply into v under
// GetJSON's contract. It is for read-only search endpoints (a Workday
// listing): a 429 or 503 resends the same body.
func PostJSON(ctx context.Context, d egress.Doer, rawURL string, body any, header http.Header, v any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("sources: encode request: %w", err)
	}
	h := header.Clone()
	if h == nil {
		h = http.Header{}
	}
	h.Set("Content-Type", "application/json")
	return DoJSON(ctx, d, http.MethodPost, rawURL, payload, h, v)
}

// DoJSON sends one request through d and decodes the JSON reply into v. It is
// the one place adapters' HTTP status, body-cap and retry rules live. A 429 or
// 503 is retried up to maxAttempts in all, after the response's Retry-After
// (seconds or an HTTP date) or an exponential, jittered backoff; a
// Retry-After over maxRetryWait, the last attempt, or a wait the per-request
// or the board's budget (WithRetryBudget) cannot cover, returns the status
// error, for a 429 as a *RateLimitedError. The wait ends with ctx.
func DoJSON(ctx context.Context, d egress.Doer, method, rawURL string, body []byte, header http.Header, v any) error {
	return doJSON(ctx, d, method, rawURL, body, header, maxBody, v)
}

func doJSON(ctx context.Context, d egress.Doer, method, rawURL string, body []byte, header http.Header, limit int64, v any) error {
	raw, err := fetch(ctx, d, method, rawURL, body, header, "application/json", limit)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		u := rawURL
		if p, perr := url.Parse(rawURL); perr == nil {
			u = p.Redacted()
		}
		return fmt.Errorf("sources: decode %s: %w", u, err)
	}
	return nil
}

// GetBody GETs rawURL and returns the body under DoJSON's status, body-cap
// and retry rules, for sources that read an HTML page rather than an API.
func GetBody(ctx context.Context, d egress.Doer, rawURL string) ([]byte, error) {
	return fetch(ctx, d, http.MethodGet, rawURL, nil, nil, "text/html,application/xhtml+xml", maxBody)
}

// fetch is DoJSON's request loop; it returns the 200 body.
func fetch(ctx context.Context, d egress.Doer, method, rawURL string, body []byte, header http.Header, accept string, limit int64) ([]byte, error) {
	if d == nil {
		return nil, errors.New("sources: no http doer configured")
	}
	verb := strings.ToLower(method)
	var waited time.Duration // spent waiting to retry this request
	for attempt := 0; ; attempt++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
		if err != nil {
			return nil, fmt.Errorf("sources: build request: %w", err)
		}
		for k, vs := range header {
			req.Header[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
		}
		req.Header.Set("Accept", accept)
		resp, err := d.Do(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("sources: %s %s: %w", verb, req.URL.Redacted(), err)
		}
		if resp == nil || resp.Body == nil {
			return nil, fmt.Errorf("sources: %s %s: empty response", verb, req.URL.Redacted())
		}
		if resp.StatusCode == http.StatusOK {
			return readBody(resp.Body, req, limit)
		}
		code, hdr := resp.StatusCode, resp.Header
		// Drain a little so the connection can be reused, then let it go.
		_, _ = io.CopyN(io.Discard, resp.Body, 64<<10)
		resp.Body.Close()
		if code == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", ErrBoardNotFound, req.URL.Redacted())
		}
		se := &egress.StatusError{Code: code, Method: method, Host: req.URL.Hostname()}
		if code != http.StatusTooManyRequests && code != http.StatusServiceUnavailable {
			return nil, fmt.Errorf("sources: %s %s: %w", verb, req.URL.Redacted(), se)
		}
		// giveUp ends the retrying: a 429 as a *RateLimitedError, so the board is
		// told apart from one that is broken, a 503 as the plain status.
		giveUp := func(why string) error {
			if code == http.StatusTooManyRequests {
				return &RateLimitedError{Host: se.Host, Method: method, Path: req.URL.Path, Attempts: attempt + 1, Why: why, Status: se}
			}
			return fmt.Errorf("sources: %s %s: %w (%s)", verb, req.URL.Redacted(), se, why)
		}
		if attempt+1 >= maxAttempts {
			return nil, giveUp(fmt.Sprintf("gave up after %d attempts", maxAttempts))
		}
		wait := backoff(attempt)
		if ra, ok := retryAfter(hdr, now()); ok {
			if ra > maxRetryWait {
				return nil, giveUp(fmt.Sprintf("asked to retry after %s, over the %s limit", ra, maxRetryWait))
			}
			wait = ra
		}
		if waited+wait > maxRequestWait {
			return nil, giveUp(fmt.Sprintf("gave up after %d attempts: waiting %s more would pass the %s limit for one request", attempt+1, wait, maxRequestWait))
		}
		if !spend(ctx, wait) {
			return nil, giveUp(fmt.Sprintf("gave up after %d attempts: the board's retry budget is spent", attempt+1))
		}
		waited += wait
		if err := sleep(ctx, wait); err != nil {
			return nil, fmt.Errorf("sources: %s %s: waiting to retry %w: %w", verb, req.URL.Redacted(), se, err)
		}
	}
}

func readBody(rc io.ReadCloser, req *http.Request, limit int64) ([]byte, error) {
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("sources: read %s: %w", req.URL.Redacted(), err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%w: over %d bytes from %s", ErrBodyTooLarge, limit, req.URL.Redacted())
	}
	return body, nil
}

// retryAfter reads a Retry-After header: delay-seconds or an HTTP date. ok is
// false when it is absent or malformed; a date in the past is a zero wait.
func retryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 0 {
			return 0, false
		}
		if n > int64(time.Hour/time.Second) { // keep the multiplication in range
			n = int64(time.Hour / time.Second)
		}
		return time.Duration(n) * time.Second, true
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return 0, false
	}
	if d := t.Sub(now); d > 0 {
		return d, true
	}
	return 0, true
}
