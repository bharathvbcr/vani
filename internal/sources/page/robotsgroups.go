package page

import (
	"bytes"
	"strings"
)

// robotsRule is one rule line of a robots.txt group: key is "allow",
// "disallow" or "crawl-delay" (lower-cased), val the trimmed text after the
// colon, comment removed.
type robotsRule struct{ key, val string }

// robotsGroups is the rule lines of the robots.txt groups that name one agent
// and of those that name "*".
type robotsGroups struct {
	named, star []robotsRule
	// sawNamed is true when some group names the agent, even one with no rules:
	// RFC 9309 §2.2.1 then ignores the "*" groups altogether.
	sawNamed bool
}

// applicable is the rules that govern the agent: those of the groups naming it
// if any group does, else those of the "*" groups, each in file order.
func (g robotsGroups) applicable() []robotsRule {
	if g.sawNamed {
		return g.named
	}
	return g.star
}

// parseRobotsGroups is the one RFC 9309 §2.2.1 group reader behind the
// Crawl-delay and the Allow/Disallow checks. A group is one or more
// user-agent lines followed by its rules; a user-agent line after a rule
// starts a new group. A group that names both the agent and "*" adds its
// rules to both lists. Lines that are not "key: value", records outside
// groups (Sitemap) and unknown keys are skipped and leave the current group as
// it was. agent is the lower-cased product token. The content is untrusted:
// nothing here interprets a rule's value.
func parseRobotsGroups(body []byte, agent string) robotsGroups {
	body = bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))
	var g robotsGroups
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
				curNamed, g.sawNamed = true, true
			case val == "*":
				curStar = true
			}
			continue
		}
		switch key {
		case "allow", "disallow", "crawl-delay":
			inAgents = false // a rule: the next user-agent line starts a new group
		default:
			continue
		}
		r := robotsRule{key: key, val: val}
		if curNamed {
			g.named = append(g.named, r)
		}
		if curStar {
			g.star = append(g.star, r)
		}
	}
	return g
}
