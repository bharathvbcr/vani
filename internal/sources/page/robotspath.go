package page

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
)

// RobotsAllows reports whether the robots.txt of host permits Vani to fetch
// path (an absolute path without a query string). It reads
// https://host/robots.txt through d under the same bounds as the Crawl-delay
// read: a 4xx is no robots.txt, which allows; a 5xx, a failed or timed-out
// request or an oversized file is an error, never "allowed", because a check
// that could not run must not pass as one that did.
func RobotsAllows(ctx context.Context, d egress.Doer, host, path string) (bool, error) {
	return robotsAllows(ctx, d, host, path, robotsTimeout)
}

func robotsAllows(ctx context.Context, d egress.Doer, host, path string, timeout time.Duration) (bool, error) {
	body, found, err := readRobots(ctx, d, host, timeout, "whether it permits this path is unknown")
	if err != nil {
		return false, err
	}
	if !found {
		return true, nil
	}
	return parseAllowed(body, robotsAgent, path), nil
}

// parseAllowed applies RFC 9309 §2.2 to the robots.txt groups that apply to
// agent (those naming it if any do, else those for "*"): the longest matching
// Allow or Disallow rule wins, Allow on a tie, and no matching rule allows.
// An empty Disallow matches nothing. The content is untrusted: unknown lines
// are skipped, and a pattern that does not compile matches nothing.
func parseAllowed(body []byte, agent, path string) bool {
	best, allowed := -1, true
	for _, r := range parseRobotsGroups(body, agent).applicable() {
		if r.key == "crawl-delay" || r.val == "" {
			continue // "Disallow:" with no path allows everything; an empty Allow adds nothing
		}
		re, err := ruleRegexp(r.val)
		if err != nil || !re.MatchString(path) {
			continue
		}
		if n := len(r.val); n > best || (n == best && r.key == "allow") {
			best, allowed = n, r.key == "allow"
		}
	}
	return allowed
}

// ruleRegexp compiles a robots.txt path pattern: a prefix match in which "*"
// is any run of characters and a trailing "$" anchors the end.
func ruleRegexp(pat string) (*regexp.Regexp, error) {
	anchored := strings.HasSuffix(pat, "$")
	pat = strings.TrimSuffix(pat, "$")
	parts := strings.Split(pat, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	expr := "^" + strings.Join(parts, ".*")
	if anchored {
		expr += "$"
	}
	return regexp.Compile(expr)
}
