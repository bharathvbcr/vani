package page

import (
	"bytes"
	"regexp"
	"strings"
)

// The two functions below are the robots.txt group state machines exactly as
// they were before the shared group parser (parseRobotsGroups) replaced the
// copy each held. They are the reference oracle: TestRobotsParsersMatchLegacy
// runs old and new over the same inputs. Only the names differ.

func legacyParseCrawlDelay(body []byte, agent string) (float64, error) {
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

func legacyParseAllowed(body []byte, agent, path string) bool {
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
