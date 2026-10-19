// Package page reads postings from a company's own careers page with a
// headless browser (Lightpanda) that is forced through the egress proxy.
//
// The listing page is fetched, its <a href> links are filtered by the board's
// operator-written LinkPattern, and each matching posting page is fetched and
// read from its schema.org JobPosting JSON-LD. Page content is untrusted: it
// only ever contributes URLs that passed validation as process arguments.
package page

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	// MaxLinks is the most posting pages one board may list. More is an error
	// asking for a narrower pattern: truncating would hide postings and let the
	// reconciler mark them dead.
	MaxLinks = 50

	httpTimeoutMs = 20000
	// boardTimeout bounds one whole List call.
	boardTimeout = 15 * time.Minute

	maxPatternLen = 512
	proxyUser     = "vani"
)

// tokenRE is what a proxy token may contain; it keeps the proxy URL and the
// argument list free of anything that needs escaping.
var tokenRE = regexp.MustCompile(`^[A-Za-z0-9._~-]{16,128}$`)

// ProxyInfo is the egress proxy every lightpanda run is pointed at.
type ProxyInfo struct {
	// Addr is the proxy's loopback "ip:port" (egress.Proxy.Start's addr).
	Addr string
	// Token is the proxy's bearer token (egress.Proxy.Start's token).
	Token string
}

type source struct {
	run   Runner
	proxy ProxyInfo
	now   func() time.Time
}

// New returns the careers-page source. run executes lightpanda; proxy names the
// egress proxy that must carry all of its traffic.
func New(run Runner, proxy ProxyInfo) sources.Source {
	return &source{run: run, proxy: proxy, now: time.Now}
}

func (*source) ATS() posting.ATS { return posting.Page }

// List reads every currently listed posting on the board. Any page that cannot
// be loaded fails the whole board: a partial listing would make the
// reconciler mark healthy postings dead. For the same reason a listing with no
// matching links is an error, not an empty board.
func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := srcutil.CheckBoard(b, posting.Page); err != nil {
		return nil, err
	}
	if strings.Contains(b.Token, ":") { // PageKey would refuse it only after the first fetch
		return nil, fmt.Errorf("page: board token %q contains ':'", b.Token)
	}
	if s.run == nil {
		return nil, errors.New("page: no runner configured")
	}
	listURL, err := fetchableURL("board url", b.URL)
	if err != nil {
		return nil, err
	}
	pattern, err := compilePattern(b.LinkPattern)
	if err != nil {
		return nil, err
	}
	proxyArgs, err := s.proxyArgs()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, boardTimeout)
	defer cancel()

	doc, err := s.fetch(ctx, proxyArgs, listURL)
	if err != nil {
		return nil, err
	}
	urls, err := postingURLs(scanHTML(doc).links, listURL, pattern)
	if err != nil {
		return nil, err
	}

	prefix := b.KeyPrefix()
	seenAt := s.now()
	out := make([]posting.Posting, 0, len(urls))
	for _, u := range urls {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		doc, err := s.fetch(ctx, proxyArgs, u.canon)
		if err != nil {
			return nil, err
		}
		p, keep := s.build(b, u, doc, seenAt)
		if !keep {
			continue
		}
		key, err := posting.PageKey(b.Token, u.canon)
		if err != nil {
			return nil, fmt.Errorf("page: key for %s: %w", u.canon, err)
		}
		if !strings.HasPrefix(string(key), prefix) {
			return nil, fmt.Errorf("page: key %q does not start with board prefix %q", key, prefix)
		}
		p.Key = key
		out = append(out, p)
	}
	return out, nil
}

// build turns one posting page into a Posting; keep is false for an expired
// posting. A page with no JobPosting still yields a posting from its <title>
// and body text, so it is listed rather than lost.
func (s *source) build(b sources.Board, u target, doc string, seenAt time.Time) (posting.Posting, bool) {
	sc := scanHTML(doc)
	p := posting.Posting{
		ATS:     posting.Page,
		Company: b.Company,
		URL:     u.canon,
		SeenAt:  seenAt,
	}
	if jp, ok := findJobPosting(sc.ldJSON); ok {
		if jp.expired(seenAt) {
			return p, false
		}
		p.Title, p.Location, p.Remote, p.PostedAt = jp.title, jp.location, jp.remote, jp.postedAt
		p.Description = jp.description
		if jp.company != "" {
			p.Company = jp.company
		}
	} else {
		body := doc
		if sc.bodyStart > 0 {
			body = doc[sc.bodyStart:]
		}
		p.Description = htmltext.Text(body, srcutil.MaxDescription)
	}
	if p.Title == "" {
		p.Title = firstNonEmpty(sc.title, u.text, u.canon)
	}
	return p, true
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// fetch runs `lightpanda fetch --dump html` on rawURL, which the caller has
// already validated.
//
// --user-agent UserAgent makes lightpanda identify as Vani, so --obey-robots
// applies the same robots.txt group whose Crawl-delay Paced honours:
// lightpanda picks the group by its own User-Agent's product token, and by
// default that is "Lightpanda" (measured 2026-10-19, lightpanda
// 1.1.0-nightly.10455).
//
// --http-max-host-open 1 makes one run open one connection, so one proxy
// CONNECT, per host. The egress proxy paces CONNECTs per host; with
// lightpanda's default of 6 parallel connections, one page with same-host
// scripts would queue several paced CONNECTs inside a single run, and any
// that outlast lightpanda's connect timeout are dropped without failing the
// fetch (measured 2026-10-18, lightpanda 1.1.0-nightly).
func (s *source) fetch(ctx context.Context, proxyArgs []string, rawURL string) (string, error) {
	args := []string{"fetch", "--dump", "html", "--obey-robots", "--block-private-networks", "--fail-on-http-error",
		"--http-timeout", strconv.Itoa(httpTimeoutMs), "--http-max-host-open", "1", "--user-agent", UserAgent}
	args = append(args, proxyArgs...)
	args = append(args, rawURL)
	out, err := s.run.Run(ctx, args)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return "", fmt.Errorf("page: fetching %s: %w", rawURL, cerr)
		}
		return "", fmt.Errorf("page: fetching %s: %s", rawURL, s.redact(err.Error()))
	}
	if len(out) > MaxStdout {
		return "", fmt.Errorf("page: fetching %s: %w", rawURL, ErrOutputTooLarge)
	}
	doc := string(out)
	if !hasElement(doc) {
		return "", fmt.Errorf("page: fetching %s: %w", rawURL, ErrEmptyDocument)
	}
	return doc, nil
}

// ErrEmptyDocument is a fetch that exited 0 but dumped no element. Lightpanda
// does this when the page's first response has not arrived by its --wait-ms
// (5 s by default): it prints a bare "<!DOCTYPE html>" and succeeds. Accepting
// that would turn a posting into an empty one, so it fails the board.
var ErrEmptyDocument = errors.New("page: lightpanda dumped an empty document (the page did not load in time)")

// hasElement reports whether doc holds any markup past a leading doctype.
func hasElement(doc string) bool {
	rest := strings.TrimSpace(doc)
	if len(rest) >= 9 && strings.EqualFold(rest[:9], "<!doctype") {
		end := strings.IndexByte(rest, '>')
		if end < 0 {
			return false
		}
		rest = rest[end+1:]
	}
	return strings.Contains(rest, "<")
}

func (s *source) redact(msg string) string {
	if s.proxy.Token == "" {
		return msg
	}
	return strings.ReplaceAll(msg, s.proxy.Token, "[redacted]")
}

// proxyArgs validates the proxy and returns the flags that force traffic
// through it. Lightpanda sends --proxy-bearer-token on plain-HTTP proxying
// only, not on CONNECT (docs/PHASE0-lightpanda.md), so the token also rides as
// the proxy URL's Basic password, which the egress proxy accepts. The
// loopback proxy address is exempted from --block-private-networks, which
// would otherwise refuse to connect to the proxy itself.
func (s *source) proxyArgs() ([]string, error) {
	ap, err := netip.ParseAddrPort(s.proxy.Addr)
	if err != nil || !ap.Addr().IsLoopback() || ap.Port() == 0 {
		return nil, fmt.Errorf("page: proxy address %q is not a loopback ip:port", s.proxy.Addr)
	}
	if !tokenRE.MatchString(s.proxy.Token) {
		return nil, errors.New("page: proxy token is empty or has unexpected characters")
	}
	exempt := netip.PrefixFrom(ap.Addr().Unmap(), ap.Addr().Unmap().BitLen())
	return []string{
		"--block-cidrs", "-" + exempt.String(),
		"--http-proxy", "http://" + proxyUser + ":" + s.proxy.Token + "@" + ap.String(),
		"--proxy-bearer-token", s.proxy.Token,
	}, nil
}

func compilePattern(p string) (*regexp.Regexp, error) {
	if strings.TrimSpace(p) == "" {
		return nil, errors.New("page: board link_pattern is empty")
	}
	if len(p) > maxPatternLen {
		return nil, fmt.Errorf("page: board link_pattern is longer than %d bytes", maxPatternLen)
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, fmt.Errorf("page: board link_pattern: %w", err)
	}
	return re, nil
}

// fetchableURL accepts only an absolute https URL with a host and no
// credentials, in canonical form. Such a string cannot be read as a flag.
func fetchableURL(field, raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" || u.User != nil {
		return "", fmt.Errorf("page: %s %q is not an absolute https url", field, raw)
	}
	canon, err := posting.CanonicalURL(raw)
	if err != nil {
		return "", fmt.Errorf("page: %s: %w", field, err)
	}
	if strings.ContainsAny(canon, " \t\r\n") || strings.HasPrefix(canon, "-") {
		return "", fmt.Errorf("page: %s %q is not a clean url", field, raw)
	}
	return canon, nil
}

// target is one posting page to fetch.
type target struct {
	canon string // canonical absolute URL
	text  string // anchor text, a title fallback
}

// postingURLs resolves each link against base, keeps the http(s) ones whose
// canonical URL matches pattern, drops duplicates and the listing page itself,
// and enforces the cap and the non-empty rule.
func postingURLs(links []link, base string, pattern *regexp.Regexp) ([]target, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("page: board url: %w", err)
	}
	var out []target
	seen := map[string]bool{base: true}
	for _, l := range links {
		ref, err := url.Parse(strings.TrimSpace(l.href))
		if err != nil {
			continue
		}
		abs := baseURL.ResolveReference(ref)
		if abs.Scheme != "https" && abs.Scheme != "http" { // javascript:, mailto:, tel:, data: ...
			continue
		}
		canon, err := posting.CanonicalURL(abs.String())
		if err != nil || !pattern.MatchString(canon) || seen[canon] {
			continue
		}
		seen[canon] = true
		// A posting the operator's pattern selected but that cannot be fetched
		// is a failure, not something to skip: skipping it would mark it dead.
		if _, err := fetchableURL("posting link", canon); err != nil {
			return nil, err
		}
		out = append(out, target{canon: canon, text: l.text})
		if len(out) > MaxLinks {
			return nil, fmt.Errorf("page: link_pattern matches more than %d posting links; narrow the pattern", MaxLinks)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("page: no link on the page matched link_pattern; refusing to report an empty board")
	}
	return out, nil
}
