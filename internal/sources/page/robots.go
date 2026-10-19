package page

import (
	"bytes"
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
)

const (
	// robotsAgent is the product token whose robots.txt group Vani obeys. It is
	// the product token of egress's default User-Agent ("Vani/0.1 ..."), which
	// the robots.txt request itself carries.
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
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	u := (&url.URL{Scheme: "https", Host: host, Path: "/robots.txt"}).String()
	fail := func(err error) (float64, error) {
		if ctx.Err() == nil && errors.Is(rctx.Err(), context.DeadlineExceeded) {
			return 0, fmt.Errorf("page: robots.txt for %s: timed out after %s: %w", host, timeout, context.DeadlineExceeded)
		}
		return 0, fmt.Errorf("page: robots.txt for %s: %w", host, err)
	}
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u, nil)
	if err != nil {
		return fail(err)
	}
	resp, err := d.Do(rctx, req)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()
	switch c := resp.StatusCode; {
	case c >= 200 && c < 300:
	case c >= 400 && c < 500:
		return 0, nil
	default:
		return fail(fmt.Errorf("status %d, so its Crawl-delay is unknown", c))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRobotsBytes+1))
	if err != nil {
		return fail(err)
	}
	if len(body) > maxRobotsBytes {
		return fail(ErrRobotsTooLarge)
	}
	secs, err := parseCrawlDelay(body, robotsAgent)
	if err != nil {
		return fail(err)
	}
	return secs, nil
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
	body = bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))
	type bucket struct {
		seen  bool // some group applies
		delay float64
		err   error // the first malformed Crawl-delay
	}
	var named, star bucket
	inAgents := false                 // the previous directive was a user-agent line
	curNamed, curStar := false, false // the current group names agent / names "*"
	for line := range bytes.SplitSeq(body, []byte("\n")) {
		if i := bytes.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		k, v, ok := strings.Cut(string(line), ":")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.TrimSpace(v)
		if key == "user-agent" {
			if !inAgents {
				curNamed, curStar = false, false
			}
			inAgents = true
			switch {
			case productToken(val) == agent:
				curNamed, named.seen = true, true
			case val == "*":
				curStar, star.seen = true, true
			}
			continue
		}
		switch key {
		case "allow", "disallow", "crawl-delay":
			inAgents = false // a rule: the next user-agent line starts a new group
		default:
			continue // a record outside groups (Sitemap) or unknown: skipped, the group goes on
		}
		if key != "crawl-delay" || (!curNamed && !curStar) {
			continue
		}
		secs, err := parseDelayValue(val)
		add := func(b *bucket) {
			switch {
			case err == nil:
				b.delay = max(b.delay, secs)
			case b.err == nil:
				b.err = err
			}
		}
		if curNamed {
			add(&named)
		}
		if curStar {
			add(&star)
		}
	}
	b := star
	if named.seen {
		b = named
	}
	if b.err != nil {
		return 0, b.err
	}
	return b.delay, nil
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
