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

	// BoardTimeout bounds one whole List call, and with it the life of the
	// board's lightpanda process and of its proxy tunnels.
	BoardTimeout = 15 * time.Minute

	httpTimeoutMs = 20000
	// watchdogMs ends a page script that keeps the browser busy this long. It
	// is lightpanda's default, pinned so a change of default cannot slow
	// every board.
	watchdogMs = 30000

	maxPatternLen = 512
	proxyUser     = "vani"
)

// tokenRE is what a proxy token may contain; it keeps the proxy URL and the
// argument list free of anything that needs escaping.
var tokenRE = regexp.MustCompile(`^[A-Za-z0-9._~-]{16,128}$`)

// ProxyInfo is the egress proxy every lightpanda process is pointed at.
type ProxyInfo struct {
	// Addr is the proxy's loopback "ip:port" (egress.Proxy.Start's addr).
	Addr string
	// Token is the proxy's bearer token (egress.Proxy.Start's token).
	Token string
}

type source struct {
	run   Browser
	proxy ProxyInfo
	now   func() time.Time
}

// New returns the careers-page source. run loads pages; proxy names the
// egress proxy its lightpanda was started with (Flags), whose token is kept
// out of error messages.
func New(run Browser, proxy ProxyInfo) sources.Source {
	return &source{run: run, proxy: proxy, now: time.Now}
}

func (*source) ATS() posting.ATS { return posting.Page }

// List reads every currently listed posting on the board. Any page that cannot
// be loaded fails the whole board: a partial listing would make the
// reconciler mark healthy postings dead. For the same reason a listing with no
// matching links is an error, not an empty board.
//
// One lightpanda process serves all of the board's pages, so it reads the
// host's robots.txt once; the board's end resets it, so no cookie or page
// state reaches the next board.
func (s *source) List(ctx context.Context, b sources.Board) (listed []posting.Posting, err error) {
	if err := srcutil.CheckBoard(b, posting.Page); err != nil {
		return nil, err
	}
	if s.run == nil {
		return nil, errors.New("page: no browser configured")
	}
	listURL, err := fetchableURL("board url", b.URL)
	if err != nil {
		return nil, err
	}
	pattern, err := compilePattern(b.LinkPattern)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, BoardTimeout)
	defer cancel()
	defer func() {
		// A browser that cannot be ended may still hold this board's pages
		// or state; the board is not reported read.
		if rerr := s.run.Reset(); rerr != nil && err == nil {
			listed, err = nil, fmt.Errorf("page: ending the board's browser: %w", rerr)
		}
	}()
	// Relative links resolve against the URL as written: the canonical form
	// drops a trailing "/", which moves "job-1" on ".../eng/" up a directory.
	return s.list(ctx, b, listURL, strings.TrimSpace(b.URL), pattern)
}

func (s *source) list(ctx context.Context, b sources.Board, listURL, docURL string, pattern *regexp.Regexp) ([]posting.Posting, error) {
	doc, err := s.fetch(ctx, listURL)
	if err != nil {
		return nil, err
	}
	sc := scanHTML(doc)
	base, err := documentBase(docURL, sc)
	if err != nil {
		return nil, err
	}
	urls, err := postingURLs(sc.links, listURL, base, pattern)
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
		doc, err := s.fetch(ctx, u.canon)
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

// emptyRetryWait is the pause before the one retry of a fetch that dumped an
// empty document. A variable only so tests need not wait in real time.
var emptyRetryWait = 2 * time.Second

// fetch loads rawURL, which the caller has already validated. An empty page
// (its first response missed lightpanda's goto timeout) is loaded again once
// after emptyRetryWait; Apple's careers pages did this on one posting in nine
// on a live run. A second empty page fails with ErrEmptyDocument. Other
// failures are not retried: a bot challenge or a 404 must not be asked again.
func (s *source) fetch(ctx context.Context, rawURL string) (string, error) {
	doc, err := s.fetchOnce(ctx, rawURL)
	if !errors.Is(err, ErrEmptyDocument) {
		return doc, err
	}
	t := time.NewTimer(emptyRetryWait)
	select {
	case <-t.C:
	case <-ctx.Done():
		t.Stop()
		return "", fmt.Errorf("page: fetching %s: %w", rawURL, ctx.Err())
	}
	return s.fetchOnce(ctx, rawURL)
}

// fetchOnce is one page load.
func (s *source) fetchOnce(ctx context.Context, rawURL string) (string, error) {
	out, err := s.run.Load(ctx, rawURL)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return "", fmt.Errorf("page: fetching %s: %w", rawURL, cerr)
		}
		return "", fmt.Errorf("page: fetching %s: %w", rawURL, redacted{err: err, token: s.proxy.Token})
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

// ErrEmptyDocument is a page with no element. Lightpanda's goto reports one
// when the page's first response has not arrived within its timeout (its page
// is then a bare "<!DOCTYPE html>"). Accepting that would turn a posting into
// an empty one, so it fails the board.
var ErrEmptyDocument = errors.New("page: lightpanda loaded an empty document (the page did not load in time)")

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

// redacted is err with the proxy token taken out of its message. It still
// unwraps to err, so ErrEmptyDocument and the other sentinels stay testable.
type redacted struct {
	err   error
	token string
}

func (r redacted) Error() string {
	if r.token == "" {
		return r.err.Error()
	}
	return strings.ReplaceAll(r.err.Error(), r.token, "[redacted]")
}

func (r redacted) Unwrap() error { return r.err }

// Flags returns the lightpanda process flags for a browser whose traffic
// all goes through proxy.
//
// --user-agent UserAgent makes lightpanda identify as Vani, so --obey-robots
// applies the same robots.txt group whose Crawl-delay Paced honours:
// lightpanda picks the group by its own User-Agent's product token, and by
// default that is "Lightpanda" (measured 2026-10-19, lightpanda
// 1.1.0-nightly.10455).
//
// --http-max-host-open 1 makes the process open one connection, so one proxy
// CONNECT, per host. The egress proxy paces CONNECTs per host; with
// lightpanda's default of 6 parallel connections, one page with same-host
// scripts would queue several paced CONNECTs, and any that outlast
// lightpanda's connect timeout are dropped without failing the page
// (measured 2026-10-18, lightpanda 1.1.0-nightly).
//
// --http-max-response-size MaxStdout fails a page whose response is larger
// than Vani would read, before it is read.
//
// Lightpanda sends --proxy-bearer-token on plain-HTTP proxying only, not on
// CONNECT (docs/PHASE0-lightpanda.md), so the token also rides as the proxy
// URL's Basic password, which the egress proxy accepts. The loopback proxy
// address is exempted from --block-private-networks, which would otherwise
// refuse to connect to the proxy itself.
func Flags(proxy ProxyInfo) ([]string, error) {
	ap, err := netip.ParseAddrPort(proxy.Addr)
	if err != nil || !ap.Addr().IsLoopback() || ap.Port() == 0 {
		return nil, fmt.Errorf("page: proxy address %q is not a loopback ip:port", proxy.Addr)
	}
	if !tokenRE.MatchString(proxy.Token) {
		return nil, errors.New("page: proxy token is empty or has unexpected characters")
	}
	exempt := netip.PrefixFrom(ap.Addr().Unmap(), ap.Addr().Unmap().BitLen())
	return []string{
		"--obey-robots", "--block-private-networks",
		"--http-timeout", strconv.Itoa(httpTimeoutMs), "--http-max-host-open", "1",
		"--http-max-response-size", strconv.Itoa(MaxStdout), "--watchdog-ms", strconv.Itoa(watchdogMs),
		"--user-agent", UserAgent,
		"--block-cidrs", "-" + exempt.String(),
		"--http-proxy", "http://" + proxyUser + ":" + proxy.Token + "@" + ap.String(),
		"--proxy-bearer-token", proxy.Token,
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

// documentBase is the URL relative links on the listing resolve against: the
// listing URL as the operator wrote it (trailing "/" kept), or the page's
// first <base href> resolved against it, as a
// browser does (Google's careers results use one). Page content picks the
// base, so it only shapes candidate URLs: each still has to match the
// operator's pattern, be a clean https URL and pass the egress allowlist. A
// base that does not resolve to an absolute http(s) URL is ignored.
func documentBase(listing string, sc scanned) (string, error) {
	listURL, err := url.Parse(listing)
	if err != nil {
		return "", fmt.Errorf("page: board url: %w", err)
	}
	if !sc.hasBase {
		return listing, nil
	}
	ref, err := url.Parse(strings.TrimSpace(sc.baseHref))
	if err != nil {
		return listing, nil
	}
	abs := listURL.ResolveReference(ref)
	if (abs.Scheme != "https" && abs.Scheme != "http") || abs.Host == "" {
		return listing, nil
	}
	return abs.String(), nil
}

// postingURLs resolves each link against base (see documentBase), upgrades
// http to https (see upgradeHTTP), keeps the links whose canonical URL matches
// pattern, drops duplicates and the listing page itself, and enforces the cap
// and the non-empty rule.
func postingURLs(links []link, listing, base string, pattern *regexp.Regexp) ([]target, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("page: board url: %w", err)
	}
	var out []target
	seen := map[string]bool{listing: true}
	for _, l := range links {
		ref, err := url.Parse(strings.TrimSpace(l.href))
		if err != nil {
			continue
		}
		abs := baseURL.ResolveReference(ref)
		if abs.Scheme != "https" && abs.Scheme != "http" { // javascript:, mailto:, tel:, data: ...
			continue
		}
		upgradeHTTP(abs)
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

// upgradeHTTP rewrites an http link on the default port to https, as a
// browser in HTTPS-first mode does. Older career sites still link postings as http;
// Vani fetches only https, so those links either failed the board or, under an
// https pattern, never matched and the board read as empty. A link on another
// port is left alone: its https form is a guess, and it fails the board as an
// unfetchable posting rather than being fetched somewhere else.
func upgradeHTTP(u *url.URL) {
	if u.Scheme != "http" {
		return
	}
	switch u.Port() {
	case "":
	case "80":
		u.Host = strings.TrimSuffix(u.Host, ":80")
	default:
		return
	}
	u.Scheme = "https"
}
