package page

import (
	"bytes"
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
	body = bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))
	type rule struct {
		allow bool
		re    *regexp.Regexp
		n     int // pattern length: the specificity
	}
	var named, star []rule
	var sawNamed bool
	inAgents := false
	curNamed, curStar := false, false
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
				curNamed, sawNamed = true, true
			case val == "*":
				curStar = true
			}
			continue
		}
		if key != "allow" && key != "disallow" {
			if key == "crawl-delay" {
				inAgents = false
			}
			continue
		}
		inAgents = false
		if !curNamed && !curStar {
			continue
		}
		if val == "" {
			continue // "Disallow:" with no path allows everything; an empty Allow adds nothing
		}
		re, err := ruleRegexp(val)
		if err != nil {
			continue
		}
		r := rule{allow: key == "allow", re: re, n: len(val)}
		if curNamed {
			named = append(named, r)
		}
		if curStar {
			star = append(star, r)
		}
	}
	rules := star
	if sawNamed {
		rules = named
	}
	best, allowed := -1, true
	for _, r := range rules {
		if !r.re.MatchString(path) {
			continue
		}
		if r.n > best || (r.n == best && r.allow) {
			best, allowed = r.n, r.allow
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
