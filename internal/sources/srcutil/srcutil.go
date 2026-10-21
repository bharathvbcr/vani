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
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
// waiting, so one board cannot stall a run.
const (
	maxAttempts  = 3
	maxRetryWait = 30 * time.Second
)

// retryBackoff is the wait before retry n when the response names none. A
// variable, with sleep, only so tests need not wait in real time.
var retryBackoff = [maxAttempts - 1]time.Duration{2 * time.Second, 5 * time.Second}

var sleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
// (seconds or an HTTP date) or retryBackoff; a Retry-After over maxRetryWait,
// or the last attempt, returns the status error. The wait ends with ctx.
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
		if attempt+1 >= maxAttempts {
			return nil, fmt.Errorf("sources: %s %s: %w (gave up after %d attempts)", verb, req.URL.Redacted(), se, maxAttempts)
		}
		wait := retryBackoff[attempt]
		if ra, ok := retryAfter(hdr, time.Now()); ok {
			if ra > maxRetryWait {
				return nil, fmt.Errorf("sources: %s %s: %w (asked to retry after %s, over the %s limit)", verb, req.URL.Redacted(), se, ra, maxRetryWait)
			}
			wait = ra
		}
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
