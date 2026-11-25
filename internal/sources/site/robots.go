package site

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/egress"
)

const robotsProductAgent = "vani"

type compiledRule struct {
	allow bool
	raw   string
	re    *regexp.Regexp
}

type robotsPolicy struct {
	fetched  bool
	rules    []compiledRule
	sitemaps []string
}

// fetchRobots reads https://<domain>/robots.txt once under MaxRobotsBytes.
// 4xx means no robots.txt (allowed); 5xx, non-2xx/4xx status, timeout,
// network error, or oversized body fails closed.
func fetchRobots(
	ctx context.Context,
	d egress.Doer,
	domain string,
	timeout time.Duration,
) (robotsPolicy, []string, error) {
	robotsURL := (&url.URL{Scheme: "https", Host: domain, Path: "/robots.txt"}).String()
	resp, err := fetchSameHost(
		ctx,
		d,
		robotsURL,
		domain,
		"text/plain; charset=utf-8, */*;q=0.1",
		timeout,
		MaxRobotsBytes,
	)
	if err != nil {
		return robotsPolicy{}, nil, fmt.Errorf("site: robots.txt for %s: %w", domain, err)
	}

	switch {
	case resp.status >= 200 && resp.status < 300:
		policy, notes := parseRobots(resp.body, domain)
		policy.fetched = true
		return policy, notes, nil
	case resp.status >= 400 && resp.status < 500:
		note := fmt.Sprintf("robots.txt for %s returned HTTP %d (treating as absent)", domain, resp.status)
		return robotsPolicy{fetched: false}, []string{note}, nil
	default:
		return robotsPolicy{}, nil, fmt.Errorf("site: robots.txt for %s returned status %d", domain, resp.status)
	}
}

type rawRobotsRule struct {
	key string
	val string
}

// parseRobots implements RFC 9309 group selection ("vani" falling back to "*")
// and extracts clean same-domain https Sitemap directives.
func parseRobots(body []byte, domain string) (robotsPolicy, []string) {
	body = bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))
	var (
		named, star []rawRobotsRule
		sawNamed    bool
		inAgents    bool
		curNamed    bool
		curStar     bool
		sitemaps    []string
		seenSM      = map[string]bool{}
		notes       []string
	)

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

		switch key {
		case "user-agent":
			if !inAgents {
				curNamed, curStar = false, false
			}
			inAgents = true
			switch {
			case productToken(val) == robotsProductAgent:
				curNamed, sawNamed = true, true
			case val == "*":
				curStar = true
			}
		case "sitemap":
			// RFC 9309 §2.2.3: Sitemap is outside groups and does not end or start one.
			if clean, valid := cleanSameDomainSitemapURL(val, domain); valid {
				if !seenSM[clean] {
					seenSM[clean] = true
					sitemaps = append(sitemaps, clean)
				}
			} else if val != "" {
				notes = append(notes, fmt.Sprintf("ignored robots.txt sitemap directive %q", truncNote(val)))
			}
		case "allow", "disallow", "crawl-delay":
			inAgents = false
			if key == "crawl-delay" {
				continue
			}
			r := rawRobotsRule{key: key, val: val}
			if curNamed {
				named = append(named, r)
			}
			if curStar {
				star = append(star, r)
			}
		}
	}

	applicable := star
	if sawNamed {
		applicable = named
	}
	rules := make([]compiledRule, 0, len(applicable))
	for _, r := range applicable {
		if r.val == "" {
			continue
		}
		re, err := compileRobotsPattern(r.val)
		if err != nil {
			continue
		}
		rules = append(rules, compiledRule{
			allow: r.key == "allow",
			raw:   r.val,
			re:    re,
		})
	}

	return robotsPolicy{
		rules:    rules,
		sitemaps: sitemaps,
	}, notes
}

func (p robotsPolicy) allowed(rawPath string) bool {
	if !p.fetched || len(p.rules) == 0 {
		return true
	}
	if rawPath == "" {
		rawPath = "/"
	}
	best := -1
	allowed := true
	for _, r := range p.rules {
		if !r.re.MatchString(rawPath) {
			continue
		}
		n := len(r.raw)
		if n > best || (n == best && r.allow) {
			best = n
			allowed = r.allow
		}
	}
	return allowed
}

func productToken(val string) string {
	end := strings.IndexFunc(val, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' || r == '-')
	})
	if end >= 0 {
		val = val[:end]
	}
	return strings.ToLower(val)
}

func compileRobotsPattern(pat string) (*regexp.Regexp, error) {
	anchored := strings.HasSuffix(pat, "$")
	pat = strings.TrimSuffix(pat, "$")
	parts := strings.Split(pat, "*")
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	expr := "^" + strings.Join(parts, ".*")
	if anchored {
		expr += "$"
	}
	return regexp.Compile(expr)
}

// cleanSameDomainSitemapURL accepts only clean https://<domain>/... URLs with
// no userinfo, port, fragment, whitespace, or control characters.
func cleanSameDomainSitemapURL(raw, domain string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxURLLen || !utf8.ValidString(raw) {
		return "", false
	}
	if strings.ContainsAny(raw, " \t\r\n#") || strings.ContainsFunc(raw, unicode.IsControl) {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Opaque != "" {
		return "", false
	}
	if u.User != nil || u.Port() != "" || u.Fragment != "" || u.RawFragment != "" {
		return "", false
	}
	if !strings.EqualFold(u.Hostname(), domain) {
		return "", false
	}
	if u.Path == "" || !strings.HasPrefix(u.Path, "/") || hasDotDotSegment(u.Path) {
		return "", false
	}
	u.Scheme = "https"
	u.Host = domain
	return u.String(), true
}

func hasDotDotSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

func truncNote(s string) string {
	s = cleanLine(s, 120)
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}
