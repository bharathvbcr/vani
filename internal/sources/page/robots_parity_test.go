package page

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// parityBodies is robots.txt content that exercises the group state machine:
// grouping, the named-over-star rule, comments, a BOM, CRLF, unknown records,
// rules before any user-agent line, and malformed delays.
var parityBodies = []string{
	"",
	"\n\n",
	"User-agent: *\nDisallow: /a\nCrawl-delay: 3\n",
	"User-agent: vani\nCrawl-delay: 4\nDisallow: /p\nUser-agent: *\nCrawl-delay: 9\nDisallow: /\n",
	"User-agent: *\nCrawl-delay: 9\nUser-agent: Vani/0.1 (+x)\nCrawl-delay: 2\n",
	"User-agent: vani\nUser-agent: *\nCrawl-delay: 5\nDisallow: /x\nAllow: /x/y\n",
	"User-agent: other\nUser-agent: vani\nDisallow: /named\n\nUser-agent: *\nDisallow: /\n",
	"User-agent: vani\n\nUser-agent: *\nDisallow: /\n", // a named group with no rules still wins
	"Disallow: /orphan\nCrawl-delay: 7\nUser-agent: *\nDisallow: /b\n",
	"User-agent: *\nSitemap: https://e.test/s.xml\nDisallow: /c\nunknown: thing\nCrawl-delay: 1.5\n",
	"User-agent: *\nCrawl-delay: abc\nCrawl-delay: 2\n",
	"User-agent: *\nCrawl-delay: 2\nCrawl-delay: abc\nCrawl-delay: 7\n",
	"User-agent: vani\nCrawl-delay: -1\nUser-agent: *\nCrawl-delay: 1\n",
	"User-agent: *\r\nDisallow: /crlf\r\nCrawl-delay: 6\r\n",
	"\xef\xbb\xbfUser-agent: *\nDisallow: /bom\n",
	"User-agent: * # all\nDisallow: /d # why\nAllow: /d/ok$\n",
	"USER-AGENT: *\nDISALLOW: /upper\nCRAWL-DELAY: 3\n",
	"User-agent: *\nDisallow:\nAllow:\nCrawl-delay:\n",
	"User-agent: *\nDisallow: /*.pdf$\nAllow: /docs/*.pdf$\nDisallow: /[bad\n",
	"User-agent: *\nAllow: /a\nDisallow: /a\n", // a tie goes to Allow
	"User-agent: *\nDisallow: /a\nAllow: /a\n",
	"no colon here\nUser-agent *\nDisallow /nocolon\n",
	"User-agent: *\nDisallow: /a\nUser-agent: vani\nDisallow: /named-late\n",
	"User-agent: vani\nDisallow: /v\nUser-agent: vani\nCrawl-delay: 11\n", // a second vani group
	"User-agent: *\nCrawl-delay: " + strings.Repeat("9", 100) + "\n",
	"User-agent: *\nCrawl-delay: \x1b[31m\n",
	"User-agent: *\nDisallow: /" + strings.Repeat("a", 5000) + "\n",
}

var parityPaths = []string{"/", "/a", "/a/b", "/b", "/c", "/d/ok", "/d/x", "/x/y", "/p", "/named", "/docs/a.pdf", "/z.pdf", "/crlf", "/bom", "/upper", "/orphan", "/v"}

// parityLine pieces a generated body is drawn from: every key the parser
// distinguishes plus noise, with agents that do and do not match.
var parityLines = []string{
	"User-agent: *", "User-agent: vani", "User-agent: Vani/9", "User-agent: googlebot", "User-agent:",
	"Disallow: /", "Disallow: /a", "Disallow:", "Allow: /a/b", "Allow: /a/b$", "Disallow: /*b$", "Disallow: /[x",
	"Crawl-delay: 1", "Crawl-delay: 2.5", "Crawl-delay: x", "Crawl-delay:", "Crawl-delay: .5",
	"Sitemap: https://e.test/s", "Host: e.test", "# comment", "garbage", "", "  Disallow  :  /a  # t",
}

// The shared group parser replaced the state machine each of parseCrawlDelay
// and parseAllowed carried; both must answer exactly as the copies they
// replaced did (robots_oracle_test.go), on hand-written and on generated files.
func TestRobotsParsersMatchLegacy(t *testing.T) {
	bodies := append([]string(nil), parityBodies...)
	rng := rand.New(rand.NewPCG(7, 11))
	for range 3000 {
		n := rng.IntN(14)
		var sb strings.Builder
		for range n {
			sb.WriteString(parityLines[rng.IntN(len(parityLines))])
			if rng.IntN(5) == 0 {
				sb.WriteString("\r")
			}
			sb.WriteString("\n")
		}
		bodies = append(bodies, sb.String())
	}
	for i, body := range bodies {
		b := []byte(body)
		if got, want := parityOutcome(b, robotsAgent, parseCrawlDelay), parityOutcome(b, robotsAgent, legacyParseCrawlDelay); got != want {
			t.Fatalf("body %d %q: parseCrawlDelay = %s, legacy %s", i, body, got, want)
		}
		for _, p := range parityPaths {
			if got, want := parseAllowed(b, robotsAgent, p), legacyParseAllowed(b, robotsAgent, p); got != want {
				t.Fatalf("body %d %q path %s: parseAllowed = %v, legacy %v", i, body, p, got, want)
			}
		}
		// A second agent exercises the named/star choice the other way round.
		if got, want := parseAllowed(b, "googlebot", "/a"), legacyParseAllowed(b, "googlebot", "/a"); got != want {
			t.Fatalf("body %d %q googlebot: parseAllowed = %v, legacy %v", i, body, got, want)
		}
		if got, want := parityOutcome(b, "googlebot", parseCrawlDelay), parityOutcome(b, "googlebot", legacyParseCrawlDelay); got != want {
			t.Fatalf("body %d %q googlebot: parseCrawlDelay = %s, legacy %s", i, body, got, want)
		}
	}
	if len(bodies) < 3000 {
		t.Fatalf("only %d bodies compared", len(bodies))
	}
}

func parityOutcome(body []byte, agent string, parse func([]byte, string) (float64, error)) string {
	secs, err := parse(body, agent)
	return fmt.Sprintf("%v|%v", secs, err)
}

// Both consumers read the same groups: a body's applicable rules are the
// named group's when any group names the agent, else the "*" groups'.
func TestParseRobotsGroups(t *testing.T) {
	g := parseRobotsGroups([]byte("User-agent: vani\nUser-agent: *\nAllow: /a\nSitemap: s\nCrawl-delay: 2\nUser-agent: *\nDisallow: /b\n"), "vani")
	if !g.sawNamed || len(g.named) != 2 || len(g.star) != 3 {
		t.Fatalf("named %v star %v sawNamed %v", g.named, g.star, g.sawNamed)
	}
	if got := g.applicable(); len(got) != 2 || got[0].key != "allow" || got[1].key != "crawl-delay" {
		t.Errorf("applicable = %v, want the named group's allow and crawl-delay", got)
	}
	if got := parseRobotsGroups([]byte("User-agent: *\nDisallow: /b\n"), "vani").applicable(); len(got) != 1 || got[0].val != "/b" {
		t.Errorf("star fallback = %v", got)
	}
	if got := parseRobotsGroups(nil, "vani").applicable(); len(got) != 0 {
		t.Errorf("empty body has rules: %v", got)
	}
}
