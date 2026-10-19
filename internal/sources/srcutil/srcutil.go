// Package srcutil holds the pieces every ATS adapter shares: board
// validation, a bounded JSON GET over egress.Doer, and the limits that keep a
// hostile or broken board from exhausting memory.
package srcutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	if strings.TrimSpace(b.Token) == "" {
		return errors.New("sources: board token is empty")
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
// (javascript:, file:, data:) are refused here.
func CheckURL(field, raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("sources: %s is not an absolute http(s) url", field)
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

// GetJSON issues a GET through d and decodes the JSON body into v. A 404 is
// ErrBoardNotFound, any other non-200 status is an error, and a body above
// MaxBody is ErrBodyTooLarge.
func GetJSON(ctx context.Context, d egress.Doer, rawURL string, v any) error {
	if d == nil {
		return errors.New("sources: no http doer configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("sources: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := d.Do(ctx, req)
	if err != nil {
		return fmt.Errorf("sources: get %s: %w", req.URL.Redacted(), err)
	}
	if resp == nil || resp.Body == nil {
		return fmt.Errorf("sources: get %s: empty response", req.URL.Redacted())
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrBoardNotFound, req.URL.Redacted())
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("sources: get %s: status %d", req.URL.Redacted(), resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return fmt.Errorf("sources: read %s: %w", req.URL.Redacted(), err)
	}
	if int64(len(body)) > maxBody {
		return fmt.Errorf("%w: over %d bytes from %s", ErrBodyTooLarge, maxBody, req.URL.Redacted())
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("sources: decode %s: %w", req.URL.Redacted(), err)
	}
	return nil
}
