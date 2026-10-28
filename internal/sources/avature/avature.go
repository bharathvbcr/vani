// Package avature lists postings from an Avature careers portal, the system
// behind careers.twosigma.com (verified 2026-10-27). The portal is server-
// rendered HTML; there is no JSON API:
//
//	GET https://{host}/{portal}/OpenRoles/?jobRecordsPerPage=10&jobOffset={n}
//	GET https://{host}/{portal}/JobDetail/{slug}/{id}
//
// The board token is a short slug the owner chooses ("twosigma"), Host the
// portal's host and URL its listing page ("https://careers.twosigma.com/
// careers/OpenRoles"): the first path segment is the portal, which differs per
// customer, so it is configuration and never a constant here.
//
// What was seen live on Two Sigma, and so what this source relies on:
//
//   - A listing page holds 10 <article class="article article--result"> cards.
//     jobRecordsPerPage=50 and =100 still answered 10, so the parameter is sent
//     only as the site's own URL shape and the page size is never assumed: the
//     offset advances by the cards a page held.
//   - The page carries no total. Past the last job the site answers 200 with
//     one result article reading "No jobs found" (offsets 70 and 1000 when 62
//     were listed): the one end marker. A page with no cards and no marker is
//     a layout this source does not know, not an end of list, and fails the
//     board; so does a page of cards that are all already read, because a
//     server that ignores the offset or wraps round cannot be told from an end.
//   - The RSS feed ignores paging and holds only the 20 oldest jobs: not used.
//   - A detail page has no schema.org JSON-LD (so page's JSON-LD reader, which
//     is unexported as well, is not used) and no posting date. Its description
//     is the <section> after <div class="grid__item grid__item--main">; the
//     sidebar fields are label/value pairs. PostedAt stays unset.
//   - An unknown id answers 302 to {portal}/Error, which answers 404; the
//     egress client follows the redirect, so a closed posting arrives as a 404.
//     A real id with a wrong slug answers the same page, so the id is the key.
//   - robots.txt allows /careers and disallows "/careers/*qtvc=", a query
//     parameter this source never sends; it has no Crawl-delay. Both paths are
//     checked against the host's robots.txt (page.RobotsAllows) before the first
//     page is read.
//
// Every posting costs a detail request (the list has no description), so a
// board over MaxPostings fails rather than being truncated, and detail 404s
// are bounded like eightfold's and apple's.
package avature

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/page"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	// MaxPostings bounds one board: each posting costs a detail request.
	MaxPostings = 200
	// pageSize is the cards a listing page held on 2026-10-27. It only bounds
	// the page loop; the end marker decides when a listing is complete.
	pageSize = 10
	// maxPages is every page of a full board and the marker page after it,
	// plus one spare.
	maxPages = MaxPostings/pageSize + 2

	// A listing page is 110 KB and a detail 57 KB, mostly the site's chrome: a
	// few MiB bounds either far more tightly than srcutil's 64 MiB default.
	pageMaxBody   = 4 << 20
	detailMaxBody = 4 << 20

	// paceEvery spaces a board's requests. The robots.txt sets no Crawl-delay
	// (2026-10-27); 2 s is the sibling sources' spacing (eightfold), above
	// egress's one-second per-host floor, so the two never add. A full board
	// (22 pages and 200 details) waits 7.4 min.
	paceEvery = 2 * time.Second
	// paceBudget bounds a board's waiting: 450 requests at paceEvery, twice a
	// full board.
	paceBudget = 15 * time.Minute

	// noJobs is the text of the one article an empty results page holds.
	noJobs = "No jobs found"

	// slugPattern is a detail link's slug segment: the city, country and title
	// the site writes into the URL, as it percent-encodes them.
	slugPattern = `[A-Za-z0-9._~%-]{1,300}`
)

// ErrShape is a page whose layout is not the one this source knows.
var ErrShape = errors.New("avature: unexpected page layout")

var (
	hostRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)
	// tokenRE is the slug a board is keyed by: lower case, so the key is too.
	tokenRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	// listPathRE is a listing page's path: {portal}/{page}, each one safe path
	// segment, with an optional trailing slash.
	listPathRE = regexp.MustCompile(`^/([A-Za-z0-9_-]{1,64})/([A-Za-z0-9_-]{1,64})/?$`)
	idRE       = regexp.MustCompile(`^[0-9]{1,20}$`)

	articleRE = regexp.MustCompile(`(?is)<article\b([^>]*)>(.*?)</article>`)
	anchorRE  = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a>`)
	hrefRE    = regexp.MustCompile(`(?is)\bhref\s*=\s*"([^"]*)"`)
	classRE   = regexp.MustCompile(`(?is)\bclass\s*=\s*"([^"]*)"`)
	spanRE    = regexp.MustCompile(`(?is)<span\b[^>]*\bclass\s*=\s*"[^"]*\bparagraph_inner-span\b[^"]*"[^>]*>(.*?)</span>`)
	tagRE     = regexp.MustCompile(`(?s)<[^>]*>`)
	linkTagRE = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	relRE     = regexp.MustCompile(`(?is)\brel\s*=\s*"([^"]*)"`)
	// fieldRE is one sidebar field of a detail page, a label and its value.
	fieldRE = regexp.MustCompile(`(?is)article__content__view__field__label"[^>]*>(.*?)</div>\s*<div class="article__content__view__field__value"[^>]*>(.*?)</div>`)
)

// ListPage is a board's listing page URL taken apart.
type ListPage struct {
	// Portal is the first path segment ("careers"): the prefix of every listing
	// and detail path of the customer.
	Portal string
	// Path is the listing page's path without a trailing slash
	// ("/careers/OpenRoles").
	Path string
}

// ParseListURL reads a board's URL, which must be the https listing page of
// host: no port, user, query or fragment, and a path of exactly a portal and a
// page name. It is the one rule behind config.ValidBoard and List, so a board
// the config accepts is one this source reads.
func ParseListURL(raw, host string) (ListPage, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Opaque != "" {
		return ListPage{}, fmt.Errorf("avature: url %q is not an https listing page", raw)
	}
	if u.User != nil || u.Port() != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return ListPage{}, fmt.Errorf("avature: url %q must have no port, user, query or fragment", raw)
	}
	if !strings.EqualFold(u.Hostname(), strings.TrimSpace(host)) {
		return ListPage{}, fmt.Errorf("avature: url %q is not on the board's host %q", raw, host)
	}
	m := listPathRE.FindStringSubmatch(u.EscapedPath())
	if m == nil {
		return ListPage{}, fmt.Errorf("avature: url %q must be https://%s/{portal}/{page}, e.g. https://%s/careers/OpenRoles", raw, host, host)
	}
	return ListPage{Portal: m[1], Path: "/" + m[1] + "/" + m[2]}, nil
}

// errDetailGone is a posting page answering 404 under a listing that
// answered: the posting closed after it was listed.
var errDetailGone = errors.New("detail is gone (HTTP 404)")

// maxGone is how many of n listed postings may come back gone before the
// board fails: a tenth, at least one (workday's bound).
func maxGone(n int) int { return max(1, n/10) }

// Stats counts what one List did: the postings listed and the ones whose
// detail page answered 404 (closed since listing).
type Stats struct {
	Listed int `json:"listed"`
	Gone   int `json:"gone,omitempty"`
}

// Options configures NewWith.
type Options struct {
	// Report, when set, receives each listed board's counts.
	Report func(sources.Board, Stats)
	// Wait spaces a board's requests (srcutil.Pacer.Sleep); nil is
	// srcutil.Sleep. A field only so tests need not wait in real time.
	Wait func(ctx context.Context, d time.Duration) error
}

type source struct {
	d   egress.Doer
	now func() time.Time
	// wait spaces requests (srcutil.Pacer.Sleep); a field only so tests need
	// not wait in real time.
	wait func(ctx context.Context, d time.Duration) error
	opt  *Options
}

// New returns the Avature source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now, wait: srcutil.Sleep} }

// NewWith is New with the board report of opt.
func NewWith(d egress.Doer, opt Options) sources.Source {
	wait := opt.Wait
	if wait == nil {
		wait = srcutil.Sleep
	}
	return &source{d: d, now: time.Now, wait: wait, opt: &opt}
}

func (*source) ATS() posting.ATS { return posting.Avature }

// row is one card of a listing page.
type row struct {
	id       string
	path     string // the detail link's escaped path, checked against the portal
	title    string
	location string
}

// site is what List knows of a board while it reads it.
type site struct {
	host   string
	list   ListPage
	pace   *srcutil.Pacer
	detail *regexp.Regexp // a detail path of this portal
}

func newSite(host string, lp ListPage, pace *srcutil.Pacer) *site {
	return &site{host: host, list: lp, pace: pace,
		detail: regexp.MustCompile(`^` + regexp.QuoteMeta("/"+lp.Portal) + `/JobDetail/(` + slugPattern + `)/([0-9]{1,20})$`)}
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := srcutil.CheckBoard(b, posting.Avature); err != nil {
		return nil, err
	}
	token := strings.ToLower(strings.TrimSpace(b.Token))
	host := strings.ToLower(strings.TrimSpace(b.Host))
	if !tokenRE.MatchString(token) {
		return nil, fmt.Errorf("avature %q: token must be a lower-case slug of letters, digits and '-', e.g. twosigma", b.Token)
	}
	if !hostRE.MatchString(host) {
		return nil, fmt.Errorf("avature %q: host %q is not a hostname", b.Token, b.Host)
	}
	lp, err := ParseListURL(b.URL, host)
	if err != nil {
		return nil, fmt.Errorf("avature %q: %w", b.Token, err)
	}
	if len(b.SearchTerms()) > 0 {
		return nil, fmt.Errorf("avature %q: searches do not apply; the board lists every open role", b.Token)
	}
	if err := sources.CheckFilters(posting.Avature, b.Filters); err != nil {
		return nil, fmt.Errorf("avature %q: %w", b.Token, err)
	}
	st := newSite(host, lp, &srcutil.Pacer{Every: paceEvery, Budget: paceBudget, Sleep: s.wait})
	if err := s.checkRobots(ctx, st); err != nil {
		return nil, fmt.Errorf("avature %q: %w", b.Token, err)
	}
	rows, err := s.listing(ctx, st)
	if err != nil {
		return nil, fmt.Errorf("avature %q: %w", b.Token, err)
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(rows))
	stats := Stats{Listed: len(rows)}
	if s.opt != nil && s.opt.Report != nil {
		defer func() { s.opt.Report(b, stats) }()
	}
	allowed := maxGone(len(rows))
	for _, r := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p, err := s.detail(ctx, st, b, token, r, seen)
		if errors.Is(err, errDetailGone) {
			// The posting closed between the listing and its page; leaving it out
			// is the truth. Many at once is the detail page failing. The error is
			// deliberately not ErrBoardNotFound: the listing answered.
			if stats.Gone++; stats.Gone > allowed || stats.Gone == len(rows) {
				return nil, fmt.Errorf("avature %q: %d of %d posting details are gone (HTTP 404); the detail pages are not answering",
					b.Token, stats.Gone, len(rows))
			}
			continue
		}
		if err != nil {
			// A partial listing would age healthy postings as absent.
			return nil, fmt.Errorf("avature %q: posting %s: %w", b.Token, r.id, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// checkRobots reads the host's robots.txt and refuses a board whose listing or
// detail path it disallows. A robots.txt that cannot be read fails the board
// (page.RobotsAllows). The path is checked without its query: the one rule
// seen live, "Disallow: /careers/*qtvc=", is a query parameter this source
// never sends.
func (s *source) checkRobots(ctx context.Context, st *site) error {
	for _, p := range []string{st.list.Path + "/", "/" + st.list.Portal + "/JobDetail/"} {
		ok, err := page.RobotsAllows(ctx, s.d, st.host, p)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("robots.txt of %s disallows %s", st.host, p)
		}
	}
	return nil
}

func (st *site) pageURL(offset int) string {
	q := url.Values{}
	q.Set("jobRecordsPerPage", strconv.Itoa(pageSize))
	q.Set("jobOffset", strconv.Itoa(offset))
	return "https://" + st.host + st.list.Path + "/?" + q.Encode()
}

// listing reads every page up to the end marker. The offset advances by the
// cards a page held, so a site that serves fewer than it was asked for leaves
// no gap; a card read twice is dropped by id.
func (s *source) listing(ctx context.Context, st *site) ([]row, error) {
	var rows []row
	have := map[string]bool{}
	offset := 0
	for n := 1; n <= maxPages; n++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := st.pace.Wait(ctx); err != nil {
			return nil, err
		}
		body, err := srcutil.GetBody(ctx, s.d, st.pageURL(offset))
		if err != nil {
			return nil, fmt.Errorf("listing at offset %d: %w", offset, err)
		}
		if len(body) > pageMaxBody {
			return nil, fmt.Errorf("listing at offset %d: %w: %d bytes", offset, srcutil.ErrBodyTooLarge, len(body))
		}
		got, end, err := st.parseListing(string(body))
		if err != nil {
			return nil, fmt.Errorf("listing at offset %d: %w", offset, err)
		}
		if end {
			return rows, nil
		}
		added := 0
		for _, r := range got {
			if have[r.id] {
				continue
			}
			have[r.id] = true
			rows = append(rows, r)
			added++
		}
		if added == 0 {
			return nil, fmt.Errorf("%w: the page at offset %d held only postings already read; the site is not paging, or wrapped round without the %q marker",
				ErrShape, offset, noJobs)
		}
		if len(rows) > MaxPostings {
			return nil, fmt.Errorf("%w: the board lists more than %d postings; each costs a detail request", srcutil.ErrTooManyPostings, MaxPostings)
		}
		offset += len(got)
	}
	return nil, fmt.Errorf("%w: the listing did not end within %d pages", ErrShape, maxPages)
}

// parseListing reads one listing page: its cards, or end when it is the empty
// page (one article reading noJobs and no cards). A card with no job link, a
// card whose links name two jobs, or no results panel at all is ErrShape.
func (st *site) parseListing(body string) (rows []row, end bool, err error) {
	if !strings.Contains(body, "results__panel") {
		return nil, false, fmt.Errorf("%w: no results panel", ErrShape)
	}
	marker := false
	for _, m := range articleRE.FindAllStringSubmatch(body, -1) {
		if !hasClass(m[1], "article--result") {
			continue
		}
		r, ok, err := st.card(m[2])
		if err != nil {
			return nil, false, err
		}
		if ok {
			rows = append(rows, r)
			continue
		}
		if !strings.Contains(htmltext.Text(m[2], 200), noJobs) {
			return nil, false, fmt.Errorf("%w: a result card has no job link", ErrShape)
		}
		marker = true
	}
	switch {
	case marker && len(rows) > 0:
		return nil, false, fmt.Errorf("%w: %q beside %d postings", ErrShape, noJobs, len(rows))
	case marker:
		return nil, true, nil
	case len(rows) == 0:
		return nil, false, fmt.Errorf("%w: neither postings nor %q", ErrShape, noJobs)
	}
	return rows, false, nil
}

func hasClass(attrs, want string) bool {
	m := classRE.FindStringSubmatch(attrs)
	if m == nil {
		return false
	}
	for _, c := range strings.Fields(m[1]) {
		if c == want {
			return true
		}
	}
	return false
}

// card reads one result article. Its job links are the anchor: the title link
// first, then two "View role" buttons, all to the one detail path. ok is false
// for an article with no job link.
func (st *site) card(inner string) (r row, ok bool, err error) {
	for _, a := range anchorRE.FindAllStringSubmatch(inner, -1) {
		h := hrefRE.FindStringSubmatch(a[1])
		if h == nil {
			continue
		}
		path, id, linked := st.jobLink(html.UnescapeString(h[1]))
		if !linked {
			continue
		}
		if !ok {
			r = row{id: id, path: path, title: srcutil.Title(tagRE.ReplaceAllString(a[2], " "))}
			ok = true
			continue
		}
		if id != r.id {
			return row{}, false, fmt.Errorf("%w: one card links jobs %s and %s", ErrShape, r.id, id)
		}
	}
	if !ok {
		return row{}, false, nil
	}
	if r.title == "" || strings.EqualFold(r.title, "view role") {
		return row{}, false, fmt.Errorf("%w: job %s has no title link before its buttons", ErrShape, r.id)
	}
	if sp := spanRE.FindStringSubmatch(inner); sp != nil {
		r.location = srcutil.Title(tagRE.ReplaceAllString(sp[1], " "))
	}
	return r, true, nil
}

// jobLink reports whether href is a job of this portal: https (or a path on
// the page's own host), the board's host, no port, user, query or fragment,
// and a path {portal}/JobDetail/{slug}/{id}. Any other link on a page is
// ignored and never fetched.
func (st *site) jobLink(href string) (path, id string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return "", "", false
	}
	if u.IsAbs() {
		if u.Scheme != "https" || !strings.EqualFold(u.Hostname(), st.host) {
			return "", "", false
		}
	} else if u.Host != "" || !strings.HasPrefix(u.Path, "/") {
		return "", "", false
	}
	if u.User != nil || u.Port() != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", "", false
	}
	m := st.detail.FindStringSubmatch(u.EscapedPath())
	// A slug of dots only is a dot segment, which a server resolves to another
	// path.
	if m == nil || !idRE.MatchString(m[2]) || strings.Trim(m[1], ".") == "" {
		return "", "", false
	}
	return u.EscapedPath(), m[2], true
}

// url is the detail page, built from the board's host and the link's checked
// path, never taken from the page as written.
func (r row) url(host string) string { return "https://" + host + r.path }

func (s *source) detail(ctx context.Context, st *site, b sources.Board, token string, r row, seen time.Time) (posting.Posting, error) {
	if err := st.pace.Wait(ctx); err != nil {
		return posting.Posting{}, err
	}
	u := r.url(st.host)
	raw, err := srcutil.GetBody(ctx, s.d, u)
	if err != nil {
		if errors.Is(err, srcutil.ErrBoardNotFound) {
			return posting.Posting{}, errDetailGone
		}
		return posting.Posting{}, err
	}
	if len(raw) > detailMaxBody {
		return posting.Posting{}, fmt.Errorf("%w: %d bytes", srcutil.ErrBodyTooLarge, len(raw))
	}
	body := string(raw)
	if err := st.checkCanonical(body, r.id); err != nil {
		return posting.Posting{}, err
	}
	side, main, err := splitDetail(body)
	if err != nil {
		return posting.Posting{}, err
	}
	fields := sidebar(side)
	location := firstNonEmpty(r.location, fields["location"])
	key, err := posting.AvatureKey(token, r.id)
	if err != nil {
		return posting.Posting{}, err
	}
	company := b.Company
	if company == "" {
		company = token
	}
	// The pay sentence is read from the whole text: a long description's cap
	// must not cut it, as it never cuts the line that carries it.
	lead, room := srcutil.Lead(payLine(htmltext.Text(main, len(main)+1), location))
	text := htmltext.Text(main, room)
	if text == "" {
		return posting.Posting{}, fmt.Errorf("%w: the description section is empty", ErrShape)
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.Avature,
		Company:     company,
		Title:       r.title,
		Location:    location,
		Department:  fields["function"],
		URL:         u,
		Description: lead + text,
		SeenAt:      seen,
	}, nil
}

// checkCanonical refuses a page that names another job: a 200 answering for
// the wrong id is not this posting. A page with no canonical link is not
// refused; the request URL carries the id.
func (st *site) checkCanonical(body, id string) error {
	for _, tag := range linkTagRE.FindAllString(body, -1) {
		rel := relRE.FindStringSubmatch(tag)
		if rel == nil || !strings.EqualFold(strings.TrimSpace(rel[1]), "canonical") {
			continue
		}
		h := hrefRE.FindStringSubmatch(tag)
		if h == nil {
			continue
		}
		_, got, ok := st.jobLink(html.UnescapeString(h[1]))
		if !ok || got != id {
			return fmt.Errorf("%w: canonical link %q is not job %s", ErrShape, h[1], id)
		}
		return nil
	}
	return nil
}

// mainMarker opens the detail page's main column; its first section is the
// description (the sidebar before it holds the label/value fields).
const mainMarker = "grid__item--main"

// splitDetail returns the sidebar HTML (everything before the main column)
// and the description section's inner HTML.
func splitDetail(body string) (side, main string, err error) {
	at := htmltext.IndexFold(body, mainMarker)
	if at < 0 {
		return "", "", fmt.Errorf("%w: no main column on the detail page", ErrShape)
	}
	rest := body[at:]
	open := htmltext.IndexFold(rest, "<section")
	if open < 0 {
		return "", "", fmt.Errorf("%w: no description section on the detail page", ErrShape)
	}
	gt := strings.IndexByte(rest[open:], '>')
	if gt < 0 {
		return "", "", fmt.Errorf("%w: unterminated description section", ErrShape)
	}
	start := open + gt + 1
	end := htmltext.IndexFold(rest[start:], "</section>")
	if end < 0 {
		return "", "", fmt.Errorf("%w: unterminated description section", ErrShape)
	}
	return body[:at], rest[start : start+end], nil
}

// sidebar reads the label/value fields before the main column, keyed by the
// lower-cased label. A label seen twice keeps its first value.
func sidebar(side string) map[string]string {
	out := map[string]string{}
	for _, m := range fieldRE.FindAllStringSubmatch(side, -1) {
		k := strings.ToLower(srcutil.Title(tagRE.ReplaceAllString(m[1], " ")))
		v := srcutil.Title(tagRE.ReplaceAllString(m[2], " "))
		if _, dup := out[k]; k != "" && v != "" && !dup {
			out[k] = v
		}
	}
	return out
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// basePayRE is the range sentence of Two Sigma's US postings ("The base pay
// for this role will be between $165,000 and $250,000."). The sentence states
// no period unless it is hourly.
var basePayRE = regexp.MustCompile(`(?i)\bbase pay\b[^$]{0,120}?\$([0-9]{1,3}(?:,[0-9]{3})*(?:\.[0-9]{1,2})?)\s*(?:and|to|-|–)\s*\$([0-9]{1,3}(?:,[0-9]{3})*(?:\.[0-9]{1,2})?)( per hour| an hour| hourly)?`)

// payLine is the base pay range of a United States posting as one
// srcutil.PayLine, or "" when the text states none, states more than one
// distinct range, or the posting is not in the United States ("$" alone does
// not say whose dollars). The text still carries the sentence either way.
func payLine(text, location string) string {
	if !strings.Contains(strings.ToLower(location), "united states") {
		return ""
	}
	var lo, hi *float64
	period := ""
	for _, m := range basePayRE.FindAllStringSubmatch(text, -1) {
		a, errA := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
		b, errB := strconv.ParseFloat(strings.ReplaceAll(m[2], ",", ""), 64)
		p := ""
		if m[3] != "" {
			p = "hour"
		}
		if errA != nil || errB != nil || a > b {
			return ""
		}
		if lo != nil && (*lo != a || *hi != b || p != period) {
			return ""
		}
		lo, hi, period = &a, &b, p
	}
	if lo == nil {
		return ""
	}
	return srcutil.PayLine("USD", lo, hi, period)
}
