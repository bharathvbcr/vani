package page

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	// UserAgent is what every careers-page request carries: lightpanda's
	// (--user-agent) and Vani's own robots.txt read. One identity means the
	// robots.txt group lightpanda obeys for Allow/Disallow is the group whose
	// Crawl-delay Paced honours. lightpanda refuses any value containing
	// "Mozilla".
	UserAgent = "Vani/0.1 (+personal job-listing reader)"
	// robotsAgent is UserAgent's product token, lower-cased: the robots.txt
	// group Vani obeys.
	robotsAgent = "vani"
	// maxRobotsBytes bounds a robots.txt body: RFC 9309's 500 KiB parsing
	// minimum, rounded up. A longer file is an error rather than read in part:
	// the cut could fall before the Crawl-delay that mattered.
	maxRobotsBytes = 512 << 10
	// robotsTimeout bounds one robots.txt request, connection through body.
	robotsTimeout = 10 * time.Second
	// maxDelayText is the longest Crawl-delay value parsed, and how much of a
	// malformed one an error quotes.
	maxDelayText = 32
)

var (
	// ErrCrawlDelayTooLong is a host whose Crawl-delay is above MaxInterval.
	// Its pages are not fetched: pacing them at the cap would go faster than
	// the site asked.
	ErrCrawlDelayTooLong = errors.New("page: robots.txt Crawl-delay exceeds the per-host wait cap")
	// ErrRobotsTooLarge is a robots.txt body over maxRobotsBytes.
	ErrRobotsTooLarge = errors.New("page: robots.txt exceeds the size limit")
)

// delayRE is the only Crawl-delay spelling accepted: decimal seconds.
var delayRE = regexp.MustCompile(`^(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)$`)

// crawlDelay fetches https://host/robots.txt through d and returns the
// Crawl-delay it sets for Vani, in seconds (0 when it sets none).
//
// Following RFC 9309 §2.3.1: a 4xx means there is no robots.txt, so no delay.
// Anything else that does not yield a parsed file (a 5xx, a refused or failed
// request, a timeout, an oversized body) is an error: the delay is unknown,
// and fetching as if it were zero would be a check that could not run passing
// as one that did.
func crawlDelay(ctx context.Context, d egress.Doer, host string, timeout time.Duration) (float64, error) {
	body, found, err := readRobots(ctx, d, host, timeout, "its Crawl-delay is unknown")
	if err != nil || !found {
		return 0, err
	}
	secs, err := parseCrawlDelay(body, robotsAgent)
	if err != nil {
		return 0, fmt.Errorf("page: robots.txt for %s: %w", host, err)
	}
	return secs, nil
}

// readRobots fetches https://host/robots.txt through d. found is false for a
// 4xx (RFC 9309 §2.3.1: there is no robots.txt). A 5xx, a refused or failed
// request, a timeout or a body over maxRobotsBytes is an error; unknown names
// what the caller could not learn, for the 5xx message.
func readRobots(ctx context.Context, d egress.Doer, host string, timeout time.Duration, unknown string) (body []byte, found bool, err error) {
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	u := (&url.URL{Scheme: "https", Host: host, Path: "/robots.txt"}).String()
	fail := func(err error) ([]byte, bool, error) {
		if ctx.Err() == nil && errors.Is(rctx.Err(), context.DeadlineExceeded) {
			return nil, false, fmt.Errorf("page: robots.txt for %s: timed out after %s: %w", host, timeout, context.DeadlineExceeded)
		}
		return nil, false, fmt.Errorf("page: robots.txt for %s: %w", host, err)
	}
	// A host the run's throttle has blocked is asked for nothing more, this
	// read included (srcutil.HostBlocked): a refused read is an error, never
	// "no robots.txt".
	if err := srcutil.HostBlocked(ctx, host, http.MethodGet, u); err != nil {
		return fail(err)
	}
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u, nil)
	if err != nil {
		return fail(err)
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := d.Do(rctx, req)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()
	switch c := resp.StatusCode; {
	case c >= 200 && c < 300:
	case c >= 400 && c < 500:
		return nil, false, nil
	default:
		return fail(fmt.Errorf("status %d, so %s", c, unknown))
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxRobotsBytes+1))
	if err != nil {
		return fail(err)
	}
	if len(body) > maxRobotsBytes {
		return fail(ErrRobotsTooLarge)
	}
	return body, true, nil
}

// parseCrawlDelay returns the Crawl-delay, in seconds, of the robots.txt
// groups that apply to agent: those naming it if any do, else those for "*".
// Several applicable groups or lines combine to the longest delay. No
// applicable Crawl-delay is 0.
//
// The content is untrusted. Lines that are not "key: value" and unknown keys
// are skipped, as RFC 9309 asks; a Crawl-delay in an applicable group that is
// not decimal seconds is an error, because guessing it as zero would pace
// faster than the site asked. An error quotes at most maxDelayText bytes of
// the value, escaped.
func parseCrawlDelay(body []byte, agent string) (float64, error) {
	var delay float64
	var first error // the first malformed Crawl-delay
	for _, r := range parseRobotsGroups(body, agent).applicable() {
		if r.key != "crawl-delay" {
			continue
		}
		secs, err := parseDelayValue(r.val)
		switch {
		case err == nil:
			delay = max(delay, secs)
		case first == nil:
			first = err
		}
	}
	if first != nil {
		return 0, first
	}
	return delay, nil
}

// parseDelayValue reads one Crawl-delay value as decimal seconds.
func parseDelayValue(val string) (float64, error) {
	if len(val) <= maxDelayText && delayRE.MatchString(val) {
		if secs, err := strconv.ParseFloat(val, 64); err == nil {
			return secs, nil
		}
	}
	if len(val) > maxDelayText {
		val = val[:maxDelayText]
	}
	return 0, fmt.Errorf("its Crawl-delay %q is not a number of seconds", val)
}

// productToken is the lower-cased leading product token of a user-agent
// value: "Vani/0.1 (+x)" is "vani".
func productToken(val string) string {
	end := strings.IndexFunc(val, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' || r == '-')
	})
	if end >= 0 {
		val = val[:end]
	}
	return strings.ToLower(val)
}

// delayDuration turns a Crawl-delay in seconds into a wait, rounded up to the
// millisecond. secs must already be within MaxInterval.
func delayDuration(secs float64) time.Duration {
	ms := int64(secs * 1000)
	if float64(ms) < secs*1000 {
		ms++
	}
	return time.Duration(ms) * time.Millisecond
}
