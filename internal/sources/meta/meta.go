// Package meta lists Meta's postings from its own careers site through two
// kinds of unauthenticated GET, checked live on 2026-11-17:
//
//	GET https://www.metacareers.com/jobsearch/sitemap.xml
//	GET https://www.metacareers.com/profile/job_details/{id}/
//
// The sitemap (178 KB, 1,362 jobs) names only each job page and a lastmod that
// is the sitemap's own generation time on every entry, so it says nothing of a
// job's title or place: each job costs one detail request, a whole page of
// about 560 KB that carries one <script type="application/ld+json">
// JobPosting (title, description, responsibilities, qualifications,
// datePosted as an RFC 3339 time, employmentType, jobLocation[], and
// jobLocationType "TELECOMMUTE" on a job that may be remote: 2 of 11 sampled).
// The block has no url, so a page is this job's only when the answer came from
// this job's own address: a redirect anywhere else (another job, /login/)
// fails the board. The job search page renders its list in the browser and
// embeds none of it.
//
// A job that closed, or an id that was never a job, answers 200 with a page
// that has no JSON-LD block at all: a made-up id did (no og: tags either), and
// so did sitemap job 1426965155339653 (2026-11-17), whose page kept its
// og:title and og:description but carried no job text. Every live job page
// sampled carried exactly one block. A page with no block, and a 404, are
// "gone", bounded by srcutil.MaxGone with the rest, so a site that stopped
// writing JSON-LD everywhere fails the board rather than closing every job; a
// page with blocks but no JobPosting this source can read is a layout it does
// not know and fails the board.
//
// robots.txt (GET 2026-11-17) disallows /ajax/, /*.php, /*cursor=, /login/
// and a few others for "User-agent: *", and lists /jobsearch/sitemap.xml as a
// sitemap; this source reads robots.txt for both of its paths before the
// sitemap and fails the board if either is disallowed or the file cannot be
// read. The file also opens with a comment that automated collection is
// prohibited without Meta's written permission; the owner decided on
// 2026-11-17 to read the site anyway, at this low pace.
//
// Detail volume is bounded as talentbrew's is (srcutil.PlanDetails): a source
// from New fails a board that needs more than MaxDetails detail requests; one
// from NewWith reuses the postings an earlier run stored (each re-read on its
// turn), and defers new jobs past MaxDetails to the next run. A listed row
// carries no title, so there is no pre-filter: every new job is read.
package meta

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/jsonld"
	"github.com/bharathvbcr/Vani/internal/sources/page"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	// Host is the careers site, the only host this source asks.
	Host = "www.metacareers.com"

	sitemapPath = "/jobsearch/sitemap.xml"
	jobPrefix   = "/profile/job_details/"

	// MaxDetails is the most job pages one run reads. At paceEvery apart that
	// is five minutes and about 84 MB; the 1,362 jobs of 2026-11-17 fill in ten
	// runs, and the rows not yet read are deferred, which ages nothing.
	MaxDetails = 150

	// paceEvery spaces the board's requests: twice egress's one-second
	// per-host floor, since each page is about 560 KB.
	paceEvery = 2 * time.Second
	// paceBudget bounds the board's waiting: MaxDetails pages, robots.txt
	// twice and the sitemap, with room.
	paceBudget = 10 * time.Minute

	// maxSitemapBody caps the sitemap (178 KB on 2026-11-17); maxEntries is
	// the sitemap protocol's own limit.
	maxSitemapBody = 16 << 20
	maxEntries     = 50000
	// maxDetailBody caps one job page (555 to 565 KB sampled).
	maxDetailBody = 4 << 20
	// maxPlaces bounds the places joined into a location (13 the most seen).
	maxPlaces = 50
	// maxBullets bounds the bullets read from responsibilities or
	// qualifications.
	maxBullets = 100
)

// ErrShape is a sitemap or job page whose layout is not the one this source
// knows.
var ErrShape = errors.New("meta: unexpected response")

// ErrBoardNotFound is returned when the site has no sitemap.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

var (
	// tokenRE is the owner's slug for the board ("meta").
	tokenRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	// jobPath is a job page's path. Ids seen are 15 to 17 digits.
	jobPath = regexp.MustCompile(`^/profile/job_details/([0-9]{1,20})/?$`)
)

// errDetailGone is a job page answering 404, or a page with no JSON-LD block,
// under a sitemap that listed it.
var errDetailGone = errors.New("detail is gone (HTTP 404, or a page with no job on it)")

// Stats counts what one List did. Every listed job is counted once, in Reused,
// Fetched, Refreshed, Deferred, Gone or Unanswered.
type Stats struct {
	Listed     int `json:"listed"`
	Reused     int `json:"reused"`
	Fetched    int `json:"fetched"`
	Refreshed  int `json:"refreshed,omitempty"`
	Deferred   int `json:"deferred,omitempty"`
	Gone       int `json:"gone,omitempty"`
	Unanswered int `json:"unanswered,omitempty"`
}

// Options turn on the bounded detail step (NewWith).
type Options struct {
	// Stored returns the postings an earlier run stored for b: every record
	// under b.KeyPrefix(), whatever its status. Required.
	Stored func(b sources.Board) []posting.Posting
	// Unusable reports a stored posting whose text must not be reused (see
	// srcutil.DetailRow.Unusable). Nil marks none.
	Unusable func(k posting.Key) bool
	// Report, when set, receives each listed board's counts, once, whether the
	// board then succeeded or failed on a detail.
	Report func(sources.Board, Stats)
	// Wait spaces the board's requests (srcutil.Pacer.Sleep); nil is
	// srcutil.Sleep.
	Wait func(ctx context.Context, d time.Duration) error
}

type source struct {
	d    egress.Doer
	now  func() time.Time
	wait func(ctx context.Context, d time.Duration) error
	opt  *Options
}

// New returns the Meta source, sending requests through d. Every listed job
// costs a detail request, and a board needing more than MaxDetails fails.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now, wait: srcutil.Sleep} }

// NewWith returns the source with the bounded detail step: stored postings are
// reused (a share of them re-read each run) and at most MaxDetails detail
// requests go out per run.
func NewWith(d egress.Doer, o Options) (sources.Source, error) {
	if o.Stored == nil {
		return nil, errors.New("meta: the bounded detail step needs the stored postings (Options.Stored)")
	}
	wait := o.Wait
	if wait == nil {
		wait = srcutil.Sleep
	}
	return &source{d: d, now: time.Now, wait: wait, opt: &o}, nil
}

func (*source) ATS() posting.ATS { return posting.Meta }

// row is one job the sitemap lists.
type row struct {
	id  string
	loc string
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckBoard(b, posting.Meta); err != nil {
		return nil, err
	}
	if !tokenRE.MatchString(b.Token) {
		return nil, fmt.Errorf("meta %q: token must be a lower-case slug of letters, digits and '-', e.g. meta", b.Token)
	}
	if len(b.SearchTerms()) > 0 || len(b.Filters) > 0 || b.Region != "" || b.MaxPostings != 0 || b.URL != "" ||
		b.LinkPattern != "" || b.Host != "" || b.BoardName != "" {
		return nil, fmt.Errorf("meta %q: searches, filters, region, max_postings, url, link_pattern, host and board_name do not apply", b.Token)
	}
	pace := &srcutil.Pacer{Every: paceEvery, Budget: paceBudget, Sleep: s.wait}
	if err := s.checkRobots(ctx, pace); err != nil {
		return nil, fmt.Errorf("meta %q: %w", b.Token, err)
	}
	if err := pace.Wait(ctx); err != nil {
		return nil, err
	}
	raw, err := srcutil.GetBodyLimit(ctx, s.d, "https://"+Host+sitemapPath, "application/xml, text/xml", maxSitemapBody)
	if err != nil {
		if errors.Is(err, ErrBoardNotFound) {
			err = fmt.Errorf("%w (the site has no job sitemap)", err)
		}
		return nil, fmt.Errorf("meta %q: %w", b.Token, err)
	}
	rows, err := parseSitemap(raw)
	if err != nil {
		return nil, fmt.Errorf("meta %q: %w", b.Token, err)
	}
	if err := srcutil.CheckCount(len(rows)); err != nil {
		return nil, fmt.Errorf("meta %q: %w", b.Token, err)
	}
	st := Stats{Listed: len(rows)}
	if s.opt != nil && s.opt.Report != nil {
		defer func() { s.opt.Report(b, st) }()
	}
	out, err := s.details(ctx, pace, b, rows, &st)
	if err != nil {
		return nil, fmt.Errorf("meta %q: %w", b.Token, err)
	}
	return out, nil
}

// checkRobots reads robots.txt for the sitemap and the job pages, each its own
// paced request as page.RobotsAllows reads it. A file that cannot be read
// fails the board; a 4xx is no robots.txt and allows.
func (s *source) checkRobots(ctx context.Context, pace *srcutil.Pacer) error {
	for _, p := range []string{sitemapPath, jobPrefix} {
		if err := pace.Wait(ctx); err != nil {
			return err
		}
		ok, err := page.RobotsAllows(ctx, s.d, Host, p)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("robots.txt of %s disallows %s", Host, p)
		}
	}
	return nil
}

// parseSitemap reads the sitemap's job rows, each job once, in sitemap order.
// Entries that are not job pages of Host are skipped; a job page this source
// cannot read, a sitemap index, a gzip, a document that is not whole, more
// than maxEntries entries, one id at two addresses, or no job at all is an
// error, never a shorter list.
func parseSitemap(raw []byte) ([]row, error) {
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		return nil, fmt.Errorf("%w: the sitemap is gzip-compressed, which this source does not read", ErrShape)
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var root xml.StartElement
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: sitemap: %v", ErrShape, err)
		}
		if se, ok := tok.(xml.StartElement); ok {
			root = se
			break
		}
	}
	switch root.Name.Local {
	case "urlset":
	case "sitemapindex":
		return nil, fmt.Errorf("%w: the sitemap is a sitemap index, which this source does not follow", ErrShape)
	default:
		return nil, fmt.Errorf("%w: the sitemap's root element is <%s>, not <urlset>", ErrShape, root.Name.Local)
	}
	var (
		rows    []row
		at      = map[string]int{}
		entries int
	)
	for closed := false; !closed; {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: sitemap: %v", ErrShape, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local != "url" {
				if err := dec.Skip(); err != nil {
					return nil, fmt.Errorf("%w: sitemap: %v", ErrShape, err)
				}
				continue
			}
			var e struct {
				Loc string `xml:"loc"`
			}
			if err := dec.DecodeElement(&e, &t); err != nil {
				return nil, fmt.Errorf("%w: sitemap: %v", ErrShape, err)
			}
			if entries++; entries > maxEntries {
				return nil, fmt.Errorf("%w: the sitemap has more than %d entries", ErrShape, maxEntries)
			}
			r, isJob, err := jobRow(strings.TrimSpace(e.Loc))
			if err != nil {
				return nil, err
			}
			if !isJob {
				continue
			}
			if i, dup := at[r.id]; dup {
				if rows[i].loc != r.loc {
					return nil, fmt.Errorf("%w: job id %s is listed at two addresses, %s and %s", ErrShape, r.id, rows[i].loc, r.loc)
				}
				continue
			}
			at[r.id] = len(rows)
			rows = append(rows, r)
		case xml.EndElement:
			closed = true
		}
	}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: sitemap: %v", ErrShape, err)
		}
		switch t := tok.(type) {
		case xml.Comment, xml.ProcInst:
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return nil, fmt.Errorf("%w: sitemap: text after the closing </urlset>", ErrShape)
			}
		default:
			return nil, fmt.Errorf("%w: sitemap: content after the closing </urlset>", ErrShape)
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: the sitemap lists no job page", ErrShape)
	}
	return rows, nil
}

// jobRow reads one <loc>. isJob is false for a page that is not under
// jobPrefix on Host. A page under it that is not exactly
// https://{Host}/profile/job_details/{id}/ is an error: skipping it would age
// a live posting.
func jobRow(loc string) (r row, isJob bool, err error) {
	u, perr := url.Parse(loc)
	if perr != nil {
		return row{}, false, fmt.Errorf("%w: sitemap entry %q is not a URL", ErrShape, trunc(loc))
	}
	if !strings.EqualFold(u.Hostname(), Host) || !strings.HasPrefix(u.Path, jobPrefix) {
		return row{}, false, nil
	}
	m := jobPath.FindStringSubmatch(u.EscapedPath())
	if len(loc) > 2048 || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" ||
		strings.ContainsAny(loc, "?#") || m == nil {
		return row{}, false, fmt.Errorf("%w: job page %q is not https://%s%s{id}/", ErrShape, trunc(loc), Host, jobPrefix)
	}
	return row{id: m[1], loc: "https://" + Host + jobPrefix + m[1] + "/"}, true, nil
}

func trunc(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

// details turns listed rows into postings, as talentbrew's does: without
// Options every row is fetched and a board over MaxDetails fails; with them
// srcutil.PlanDetails reuses stored rows, fetches new ones first and then the
// scheduled re-reads up to MaxDetails, and defers the new rows past it.
func (s *source) details(ctx context.Context, pace *srcutil.Pacer, b sources.Board, rows []row, st *Stats) ([]posting.Posting, error) {
	seen := s.now()
	stored := map[posting.Key]posting.Posting{}
	if s.opt != nil {
		for _, p := range s.opt.Stored(b) {
			stored[p.Key] = p
		}
	}
	keys := make([]posting.Key, len(rows))
	plan := make([]srcutil.DetailRow, len(rows))
	for i, r := range rows {
		key, err := posting.MetaKey(b.Token, r.id)
		if err != nil {
			return nil, err
		}
		keys[i] = key
		_, ok := stored[key]
		plan[i] = srcutil.DetailRow{ID: r.id, Stored: ok, Unusable: ok && s.opt != nil && srcutil.Marked(s.opt.Unusable, key)}
	}
	pl := srcutil.PlanDetails(plan, srcutil.Day(seen), MaxDetails)
	if s.opt == nil && pl.Deferred > 0 {
		return nil, fmt.Errorf("%w: the site lists %d jobs and each costs a detail request, over the %d limit",
			srcutil.ErrTooManyPostings, pl.New+pl.Deferred, MaxDetails)
	}
	st.Deferred = pl.Deferred
	res, err := srcutil.DetailStep{
		Board: b, Rows: len(rows), Plan: pl, CanDefer: s.opt != nil,
		Fetch: func(ctx context.Context, i int) (*posting.Posting, error) {
			p, err := s.detail(ctx, pace, b, rows[i], keys[i], seen)
			return &p, err
		},
		Reuse: func(i int) posting.Posting {
			p := stored[keys[i]]
			p.SeenAt = seen
			return p
		},
		Gone: errDetailGone,
		GoneErr: func(gone, fetch int) error {
			return fmt.Errorf("%d of %d job pages are gone (HTTP 404, or no job on the page); the site is not answering", gone, fetch)
		},
		FetchErr: func(i int, err error) error { return fmt.Errorf("job %s: %w", rows[i].id, err) },
	}.Run(ctx)
	st.Gone, st.Reused, st.Fetched, st.Refreshed, st.Unanswered = res.Gone, res.Reused, res.Fetched, res.Refreshed, res.Unanswered
	if err != nil {
		return nil, err
	}
	return srcutil.Postings(res.Slots), nil
}

func (s *source) detail(ctx context.Context, pace *srcutil.Pacer, b sources.Board, r row, key posting.Key, seen time.Time) (posting.Posting, error) {
	if err := pace.Wait(ctx); err != nil {
		return posting.Posting{}, err
	}
	tr := srcutil.NewTracker(s.d)
	body, err := srcutil.GetBodyLimit(ctx, tr, r.loc, "text/html,application/xhtml+xml", maxDetailBody)
	if err != nil {
		if errors.Is(err, srcutil.ErrBoardNotFound) {
			return posting.Posting{}, errDetailGone
		}
		return posting.Posting{}, err
	}
	if final := tr.Final(); final != nil && !sameAddress(final, r.id) {
		return posting.Posting{}, fmt.Errorf("%w: the page for job %s came from %s%s, not its own address", ErrShape, r.id, final.Hostname(), final.EscapedPath())
	}
	j, ok := findJobPosting(string(body))
	if !ok {
		if len(jsonld.Blocks(string(body))) == 0 {
			return posting.Posting{}, errDetailGone
		}
		return posting.Posting{}, fmt.Errorf("%w: the page's JSON-LD has no JobPosting for job %s", ErrShape, r.id)
	}
	return j.posting(b, r, key, seen)
}

// sameAddress reports whether the answer came from job id's own page on Host.
func sameAddress(final *url.URL, id string) bool {
	if !strings.EqualFold(final.Hostname(), Host) || final.Scheme != "https" {
		return false
	}
	m := jobPath.FindStringSubmatch(final.EscapedPath())
	return m != nil && m[1] == id
}

// ldJob is the part of Meta's JobPosting this source reads.
type ldJob struct {
	Type             jsonld.StringList `json:"@type"`
	Title            string            `json:"title"`
	Description      string            `json:"description"`
	Responsibilities string            `json:"responsibilities"`
	Qualifications   string            `json:"qualifications"`
	DatePosted       string            `json:"datePosted"`
	EmploymentType   jsonld.StringList `json:"employmentType"`
	JobLocation      []jsonld.Place    `json:"jobLocation"`
	JobLocationType  string            `json:"jobLocationType"`
	BaseSalary       json.RawMessage   `json:"baseSalary"`
}

// findJobPosting returns the page's first JobPosting block. The block names
// no url; the page's address was checked before (sameAddress).
func findJobPosting(doc string) (ldJob, bool) {
	for _, raw := range jsonld.Blocks(doc) {
		var j ldJob
		if err := jsonld.Decode(raw, &j); err != nil {
			continue
		}
		if jsonld.IsJobPosting(j.Type) {
			return j, true
		}
	}
	return ldJob{}, false
}

func (j ldJob) posting(b sources.Board, r row, key posting.Key, seen time.Time) (posting.Posting, error) {
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := line(j.Title)
	if title == "" {
		return posting.Posting{}, fmt.Errorf("%w: empty title for job %s", ErrShape, r.id)
	}
	posted, err := jsonld.ParseDate(strings.TrimSpace(j.DatePosted))
	if err != nil {
		return posting.Posting{}, fmt.Errorf("%w: job %s: %v", ErrShape, r.id, err)
	}
	remote := strings.EqualFold(strings.TrimSpace(j.JobLocationType), "TELECOMMUTE")
	flags := make([]string, 0, len(j.EmploymentType)+1)
	for _, e := range j.EmploymentType {
		flags = append(flags, srcutil.EmploymentType(e))
	}
	if remote {
		flags = append(flags, "remote")
	}
	lead, room := srcutil.Lead(jsonld.PayLine(j.BaseSalary), srcutil.RoleLine(flags...))
	company := strings.TrimSpace(b.Company)
	if company == "" {
		company = "Meta"
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.Meta,
		Company:     company,
		Title:       title,
		Location:    places(j.JobLocation),
		Remote:      remote,
		URL:         r.loc,
		PostedAt:    posted,
		Description: lead + htmltext.Text(bodyHTML(j), room),
		SeenAt:      seen,
	}, nil
}

// bodyHTML is the job's text as HTML: the description, then responsibilities
// and qualifications as lists. Meta writes those two as one string whose
// items are separated by "&nbsp;" (every page sampled), so each part between
// separators is an item.
func bodyHTML(j ldJob) string {
	var b strings.Builder
	b.WriteString("<p>")
	b.WriteString(j.Description)
	b.WriteString("</p>")
	for _, sec := range []struct{ head, text string }{
		{"Responsibilities", j.Responsibilities},
		{"Qualifications", j.Qualifications},
	} {
		items := bullets(sec.text)
		if len(items) == 0 {
			continue
		}
		b.WriteString("<h3>" + sec.head + "</h3><ul>")
		for _, it := range items {
			b.WriteString("<li>")
			b.WriteString(it)
			b.WriteString("</li>")
		}
		b.WriteString("</ul>")
	}
	return b.String()
}

// bullets splits a responsibilities or qualifications string at its "&nbsp;"
// separators, dropping empty items, at most maxBullets.
func bullets(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "&nbsp;") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if len(out) == maxBullets {
			break
		}
		out = append(out, p)
	}
	return out
}

// places is the job's locations as one line, each "locality, region,
// country" without its empty parts, joined by "; ", at most maxPlaces.
func places(in []jsonld.Place) string {
	var parts []string
	for _, p := range in {
		if len(parts) == maxPlaces {
			break
		}
		a := p.Address
		if part := srcutil.JoinDistinct(", ", line(a.Locality), line(a.Region), line(a.Country)); part != "" {
			parts = append(parts, part)
		}
	}
	return srcutil.JoinDistinct("; ", parts...)
}

// line is untrusted site text as one display line.
func line(s string) string { return htmltext.Plain(srcutil.Title(s), srcutil.MaxTitle) }
