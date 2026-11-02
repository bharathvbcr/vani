// Package talentbrew lists postings from a Radancy TalentBrew career site
// (jobs.intuit.com, jobs.citi.com) through two kinds of unauthenticated GET,
// checked live on 2026-10-28:
//
//	GET https://{host}/sitemap.xml
//	GET https://{host}/job/{city}/{title}/{company_id}/{job_id}
//
// The board token is the owner's slug for the company ("intuit") and Host is
// the site's own host ("jobs.intuit.com"): TalentBrew sites live on the
// customer's domain, so the host cannot be derived from the token. Host must
// be a host name, config.ValidBoard checks it, and no request ever leaves it.
//
// The sitemap is the whole listing: one <urlset> with a <url> per page,
// category, location and blog pages among them. Its job pages are the <loc>s
// on Host whose path is /job/{city}/{title}/{company_id}/{job_id}. The
// company id is the tenant's (27595 on Intuit, 287 on Citi) and the Board has
// no field for it, so it is read from the sitemap and every job page must
// carry the same one. The job id is the posting's identity: the server
// resolves on it alone (a wrong title segment still serves the job, a wrong id
// is a 404), and it is not the requisition number in the page's JSON-LD
// identifier ("24846" against 102521439872). <lastmod> is the time the sitemap
// was generated, the same on every entry, so it says nothing of a posting
// changing, and a stored posting is re-read on the rolling schedule alone.
//
// The sitemap names no title, location or date, so each posting costs a detail
// request, a whole job page: 55 to 75 KB on Citi, 580 to 700 KB on Intuit. The
// page carries one <script type="application/ld+json"> JobPosting with the
// title, description (HTML), datePosted (a date, "2026-10-27"), employmentType
// (Intuit only), jobLocation[] and hiringOrganization. Its baseSalary was the
// empty template (currency "", a QuantitativeValue with an empty unitText) on
// all 25 pages sampled, so no pay was seen in it live; payLine reads the
// schema.org fields behind that template and writes a line only for a range
// that is well formed.
//
// A job that closed answers its page 404 (a 62 KB page, no redirect). Only
// that is "gone", bounded by srcutil.MaxGone as in smartrecruiters: a 200 page
// with no JobPosting is a template this source does not know, not a closed job,
// and fails the board.
//
// robots.txt of jobs.intuit.com and jobs.citi.com (GET 2026-10-28, 39 bytes
// each) is "User-agent: *" and "Disallow:/search-jobs/". The sitemap and the
// /job/ pages are allowed; this source never requests anything under
// /search-jobs, which is also where the site's own listing and its JSON results
// live. It still reads robots.txt before the sitemap (page.RobotsAllows, for
// /sitemap.xml and /job/) and fails the board if either is disallowed or the
// file cannot be read, so a site that changes its rules is not asked. Requests
// are spaced paceEvery apart, over egress's one-second per-host floor, because
// each detail is a page of up to 700 KB.
//
// Detail volume is bounded as smartrecruiters' is (srcutil.PlanDetails): a
// source from New fails a board that needs more than MaxDetails detail
// requests; one from NewWith reuses the postings an earlier run stored (each
// re-read on its turn, once every srcutil.RefreshDays days), drops new jobs
// DropListed rejects without a request, and defers new jobs past MaxDetails to
// the next run. A listed row carries no title or place, only the URL, so
// DropListed judges text taken from its slug: the title segment and the city
// segment, percent-decoded, hyphens read as spaces, each word capitalised
// ("staff-software-engineer" is "Staff Software Engineer"). That is the URL's
// wording, not the posting's: it drops "&" and punctuation, and a drop rule
// that depends on either must not be keyed on it.
package talentbrew

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/page"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// ErrBoardNotFound is returned when the site has no sitemap.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

const (
	// MaxDetails is the most detail requests one board spends in one run. A
	// job page is up to 700 KB (Intuit), so at paceEvery apart this is five
	// minutes and about 100 MB a run, the most one board should take of a
	// customer's career site. Intuit (602 jobs) fills in four runs, Citi (2,993)
	// in about twenty; the rows not yet fetched are deferred, which ages
	// nothing.
	MaxDetails = 150

	// paceEvery spaces a board's requests. The site names no Crawl-delay;
	// pages are large, so this is twice egress's one-second per-host floor,
	// which the wait spans rather than adds to.
	paceEvery = 2 * time.Second
	// paceBudget bounds a board's waiting: MaxDetails requests and the sitemap,
	// with room.
	paceBudget = 10 * time.Minute

	// maxSitemapBody caps the sitemap: Citi's, with 6,588 entries, is 1.5 MB,
	// and the sitemap protocol's own limit of 50,000 entries is about 12 MB.
	maxSitemapBody = 16 << 20
	// maxEntries is that protocol limit: more <url> entries is not a sitemap
	// this source can account for.
	maxEntries = 50000
	// maxDetailBody caps one job page: Intuit's largest sampled was 695 KB.
	maxDetailBody = 2 << 20

	// maxLocLen bounds one job <loc>. The sitemap protocol's own limit is 2048.
	// Citi's longest on 2026-11-02 was 392: a percent-encoded Japanese title.
	maxLocLen = 2048
	// maxLDBlocks bounds the JSON-LD blocks read from one page, maxTagLen the
	// opening tag of a script, maxPlaces the places joined into a location.
	maxLDBlocks = 8
	maxTagLen   = 1024
	maxPlaces   = 50
)

var (
	// ErrShape is a response whose layout is not the one this source knows.
	ErrShape = errors.New("talentbrew: unexpected response")

	// tokenRE is the owner's slug for the company; the config applies the same
	// rule. ':' and '/' never match, so a token cannot split a key.
	tokenRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	// hostRE is a lower-case host name of two or more labels: no IP literal,
	// no port, no userinfo.
	hostRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)
	// jobPath is the path of a job page: /job/{city}/{title}/{company}/{id}.
	// The city and title are percent-encoded UTF-8 on Citi. One Japanese title
	// encodes to 341 bytes (jobs.citi.com, 2026-11-02), past a 300-byte segment.
	// Go's RE2 rejects a repeat count over 1000, so each of those segments stops
	// there. A loc over maxLocLen is rejected on its length, before this pattern.
	jobPath = regexp.MustCompile(`^/job/([A-Za-z0-9%._~-]{1,1000})/([A-Za-z0-9%._~-]{1,1000})/([0-9]{1,10})/([0-9]{1,20})$`)
)

// errDetailGone is a job page answering 404 under a sitemap that listed it:
// the job closed after the sitemap was written.
var errDetailGone = errors.New("detail is gone (HTTP 404)")

// Stats counts what one List did. Every listed job is counted once, in Reused,
// Fetched, Refreshed, Prefiltered, Deferred or Gone.
type Stats struct {
	Listed int `json:"listed"`
	// Reused jobs were returned as stored, with no request.
	Reused int `json:"reused"`
	// Fetched jobs were new and got a detail request; Refreshed jobs were
	// stored and were re-read on their turn.
	Fetched   int `json:"fetched"`
	Refreshed int `json:"refreshed,omitempty"`
	// Prefiltered jobs were new and dropped by DropListed without a request.
	Prefiltered int `json:"prefiltered,omitempty"`
	// Deferred jobs were new but past MaxDetails: fetched on a later run. Not
	// being stored, their absence ages nothing.
	Deferred int `json:"deferred,omitempty"`
	// Gone jobs answered their detail 404: closed since the sitemap.
	Gone int `json:"gone,omitempty"`
}

// Options turn on the bounded detail step (NewWith).
type Options struct {
	// Stored returns the postings an earlier run stored for b: every record
	// under b.KeyPrefix(), whatever its status. Required.
	Stored func(b sources.Board) []posting.Posting
	// DropListed reports whether the engine drops a posting before triage from
	// what a listed row says: a title and city taken from the URL's slug, and
	// the board's company (no description). A new job it drops is neither
	// fetched nor returned. Nil drops nothing.
	DropListed func(p posting.Posting) bool
	// Report, when set, receives each listed board's counts, once, whether the
	// board then succeeded or failed on a detail.
	Report func(sources.Board, Stats)
	// Wait spaces a board's requests (srcutil.Pacer.Sleep); nil is srcutil.Sleep.
	// A field only so tests need not wait in real time.
	Wait func(ctx context.Context, d time.Duration) error
}

type source struct {
	d    egress.Doer
	now  func() time.Time
	wait func(ctx context.Context, d time.Duration) error
	opt  *Options
}

// New returns the TalentBrew source, sending requests through d. Every listed
// job costs a detail request, and a board needing more than MaxDetails fails.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now, wait: srcutil.Sleep} }

// NewWith returns the source with the bounded detail step: stored postings are
// reused (a share of them re-read each run), new jobs DropListed rejects are
// never fetched, and at most MaxDetails detail requests go out per board per
// run.
func NewWith(d egress.Doer, o Options) (sources.Source, error) {
	if o.Stored == nil {
		return nil, errors.New("talentbrew: the bounded detail step needs the stored postings (Options.Stored)")
	}
	wait := o.Wait
	if wait == nil {
		wait = srcutil.Sleep
	}
	return &source{d: d, now: time.Now, wait: wait, opt: &o}, nil
}

func (*source) ATS() posting.ATS { return posting.TalentBrew }

// row is one job the sitemap lists.
type row struct {
	id   string
	loc  string
	slug string // the title segment, as written
	city string // the city segment, as written
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckBoard(b, posting.TalentBrew); err != nil {
		return nil, err
	}
	if !tokenRE.MatchString(b.Token) {
		return nil, fmt.Errorf("talentbrew %q: token must be a lower-case slug of letters, digits and '-', e.g. intuit", b.Token)
	}
	// Lower-cased as config.BoardHost does, so the host asked is the host the
	// config allowed.
	host := strings.ToLower(strings.TrimSpace(b.Host))
	if !hostRE.MatchString(host) {
		return nil, fmt.Errorf("talentbrew %q: host %q must be the site's lower-case host name, e.g. jobs.intuit.com", b.Token, b.Host)
	}
	if len(b.SearchTerms()) > 0 || len(b.Filters) > 0 || b.Region != "" || b.MaxPostings != 0 || b.URL != "" || b.LinkPattern != "" {
		return nil, fmt.Errorf("talentbrew %q: searches, filters, region, max_postings, url and link_pattern do not apply", b.Token)
	}
	pace := &srcutil.Pacer{Every: paceEvery, Budget: paceBudget, Sleep: s.wait}
	if err := s.checkRobots(ctx, pace, host); err != nil {
		return nil, fmt.Errorf("talentbrew %q: %w", b.Token, err)
	}
	if err := pace.Wait(ctx); err != nil {
		return nil, err
	}
	raw, err := srcutil.GetBodyLimit(ctx, s.d, "https://"+host+"/sitemap.xml", "application/xml, text/xml", maxSitemapBody)
	if err != nil {
		if errors.Is(err, ErrBoardNotFound) {
			err = fmt.Errorf("%w (the site has no sitemap)", err)
		}
		return nil, fmt.Errorf("talentbrew %q: %w", b.Token, err)
	}
	rows, company, err := parseSitemap(raw, host)
	if err != nil {
		return nil, fmt.Errorf("talentbrew %q: %w", b.Token, err)
	}
	if err := srcutil.CheckCount(len(rows)); err != nil {
		return nil, fmt.Errorf("talentbrew %q: %w", b.Token, err)
	}
	st := Stats{Listed: len(rows)}
	if s.opt != nil && s.opt.Report != nil {
		defer func() { s.opt.Report(b, st) }()
	}
	out, err := s.details(ctx, pace, b, host, company, rows, &st)
	if err != nil {
		return nil, fmt.Errorf("talentbrew %q: %w", b.Token, err)
	}
	return out, nil
}

// checkRobots reads the host's robots.txt and refuses a board whose sitemap or
// job pages it disallows, as avature does for its paths. A robots.txt that
// cannot be read fails the board (page.RobotsAllows); a 4xx is no robots.txt
// and allows. Each read is its own paced request, the way page.RobotsAllows
// reads the file once per path.
func (s *source) checkRobots(ctx context.Context, pace *srcutil.Pacer, host string) error {
	for _, p := range []string{"/sitemap.xml", "/job/"} {
		if err := pace.Wait(ctx); err != nil {
			return err
		}
		ok, err := page.RobotsAllows(ctx, s.d, host, p)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("robots.txt of %s disallows %s", host, p)
		}
	}
	return nil
}

// parseSitemap reads a sitemap's job rows, each job once, in sitemap order,
// and the tenant's company id. Pages that are not jobs (the home page,
// categories, locations, blog posts) and pages on other hosts are skipped;
// a job page on host that this source cannot read, a sitemap index, a gzip, a
// document that is not whole, more than maxEntries entries, two company ids,
// or no job at all is an error, never a shorter list. Both live sitemaps start
// with a UTF-8 byte-order mark, which encoding/xml reads past on its own (a trim
// of it was tried and removed: no test needed it).
func parseSitemap(raw []byte, host string) ([]row, string, error) {
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		return nil, "", fmt.Errorf("%w: the sitemap is gzip-compressed, which this source does not read", ErrShape)
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var root xml.StartElement
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, "", fmt.Errorf("%w: sitemap: %v", ErrShape, err)
		}
		if se, ok := tok.(xml.StartElement); ok {
			root = se
			break
		}
	}
	switch root.Name.Local {
	case "urlset":
	case "sitemapindex":
		return nil, "", fmt.Errorf("%w: the sitemap is a sitemap index, which this source does not follow", ErrShape)
	default:
		return nil, "", fmt.Errorf("%w: the sitemap's root element is <%s>, not <urlset>", ErrShape, root.Name.Local)
	}
	var (
		rows    []row
		company string
		at      = map[string]int{}
		entries int
		closed  bool
	)
	for !closed {
		tok, err := dec.Token()
		if err != nil {
			return nil, "", fmt.Errorf("%w: sitemap: %v", ErrShape, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local != "url" {
				if err := dec.Skip(); err != nil {
					return nil, "", fmt.Errorf("%w: sitemap: %v", ErrShape, err)
				}
				continue
			}
			var e struct {
				Loc string `xml:"loc"`
			}
			if err := dec.DecodeElement(&e, &t); err != nil {
				return nil, "", fmt.Errorf("%w: sitemap: %v", ErrShape, err)
			}
			if entries++; entries > maxEntries {
				return nil, "", fmt.Errorf("%w: the sitemap has more than %d entries", ErrShape, maxEntries)
			}
			r, companyID, isJob, err := jobRow(strings.TrimSpace(e.Loc), host)
			if err != nil {
				return nil, "", err
			}
			if !isJob {
				continue
			}
			if company == "" {
				company = companyID
			} else if company != companyID {
				return nil, "", fmt.Errorf("%w: the sitemap's jobs carry company ids %s and %s; a site must have one", ErrShape, company, companyID)
			}
			if i, dup := at[r.id]; dup {
				if rows[i].loc != r.loc {
					return nil, "", fmt.Errorf("%w: job id %s is listed at two addresses, %s and %s", ErrShape, r.id, rows[i].loc, r.loc)
				}
				continue
			}
			at[r.id] = len(rows)
			rows = append(rows, r)
		case xml.EndElement:
			closed = true
		}
	}
	// Only whitespace and comments may follow the root.
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("%w: sitemap: %v", ErrShape, err)
		}
		switch t := tok.(type) {
		case xml.Comment, xml.ProcInst:
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return nil, "", fmt.Errorf("%w: sitemap: text after the closing </urlset>", ErrShape)
			}
		default:
			return nil, "", fmt.Errorf("%w: sitemap: content after the closing </urlset>", ErrShape)
		}
	}
	if len(rows) == 0 {
		// A site with no job at all is told apart from a sitemap that came back
		// short only by its jobs, so an empty one is not read as every job
		// closing.
		return nil, "", fmt.Errorf("%w: the sitemap lists no job page on %s", ErrShape, host)
	}
	return rows, company, nil
}

// jobRow reads one <loc>. isJob is false for a page that is not a job page of
// host. A job page of host (its path starts /job/) that is not exactly
// https://{host}/job/{city}/{title}/{company}/{id} is an error: it cannot be
// accounted for, and skipping it would age a live posting.
func jobRow(loc, host string) (r row, companyID string, isJob bool, err error) {
	u, perr := url.Parse(loc)
	if perr != nil {
		return row{}, "", false, fmt.Errorf("%w: sitemap entry %q is not a URL", ErrShape, trunc(loc))
	}
	if !strings.EqualFold(u.Hostname(), host) || !strings.HasPrefix(u.Path, "/job/") {
		return row{}, "", false, nil
	}
	m := jobPath.FindStringSubmatch(u.EscapedPath())
	if len(loc) > maxLocLen || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" ||
		strings.Contains(loc, "?") || strings.Contains(loc, "#") || m == nil ||
		strings.Trim(m[1], ".") == "" || strings.Trim(m[2], ".") == "" { // "." and ".." are not names
		return row{}, "", false, fmt.Errorf("%w: job page %q is not https://%s/job/{city}/{title}/{company}/{id}", ErrShape, trunc(loc), host)
	}
	return row{id: m[4], loc: "https://" + host + u.EscapedPath(), city: m[1], slug: m[2]}, m[3], true, nil
}

func trunc(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

// slugText is a URL path segment as words: percent-decoded when it is valid
// UTF-8, split at '-', '_' and spaces, each word's first letter upper-cased.
func slugText(seg string) string {
	if dec, err := url.PathUnescape(seg); err == nil {
		seg = dec
	}
	words := strings.FieldsFunc(seg, func(r rune) bool { return r == '-' || r == '_' || unicode.IsSpace(r) })
	for i, w := range words {
		rs := []rune(w)
		rs[0] = unicode.ToUpper(rs[0])
		words[i] = string(rs)
	}
	return line(strings.Join(words, " "))
}

// line is untrusted site text as one display line: srcutil.Title, then any
// control character it let through (a NUL, an escape) removed.
func line(s string) string { return htmltext.Plain(srcutil.Title(s), srcutil.MaxTitle) }

// boardCompany is the company a board's postings carry: the board's, else the
// name the page gives, else the token.
func boardCompany(b sources.Board, pageName string) string {
	if c := strings.TrimSpace(b.Company); c != "" {
		return c
	}
	if c := line(pageName); c != "" {
		return c
	}
	return b.Token
}

// listedPosting is what a listed row says of its job before its page is read:
// the fields DropListed may judge. The title and place are the URL's slug text.
func listedPosting(b sources.Board, r row) posting.Posting {
	return posting.Posting{
		ATS: posting.TalentBrew, Company: boardCompany(b, ""),
		Title: slugText(r.slug), Location: slugText(r.city),
	}
}

// details turns listed rows into postings. Without Options every row is
// fetched, and a board needing more than MaxDetails fails. With Options, the
// rows are planned by srcutil.PlanDetails: a stored row is reused unless its
// turn has come; new rows DropListed rejects are skipped; new rows are fetched
// first, then the scheduled ones, up to MaxDetails, and the new rows past it
// are deferred. Results keep sitemap order.
func (s *source) details(ctx context.Context, pace *srcutil.Pacer, b sources.Board, host, company string, rows []row, st *Stats) ([]posting.Posting, error) {
	seen := s.now()
	stored := map[posting.Key]posting.Posting{}
	if s.opt != nil {
		// Looked up by the key a row builds, so another board's posting can
		// never match one of this board's rows.
		for _, p := range s.opt.Stored(b) {
			stored[p.Key] = p
		}
	}
	keys := make([]posting.Key, len(rows))
	plan := make([]srcutil.DetailRow, len(rows))
	for i, r := range rows {
		key, err := posting.TalentBrewKey(b.Token, r.id)
		if err != nil {
			return nil, err
		}
		keys[i] = key
		_, ok := stored[key]
		plan[i] = srcutil.DetailRow{ID: r.id, Stored: ok}
		if !ok && s.opt != nil && s.opt.DropListed != nil {
			plan[i].Drop = s.opt.DropListed(listedPosting(b, r))
		}
	}
	pl := srcutil.PlanDetails(plan, srcutil.Day(seen), MaxDetails)
	if s.opt == nil && pl.Deferred > 0 {
		// Without stored postings a deferred row cannot be told from a stored
		// one, and leaving out a stored one would age it as absent.
		return nil, fmt.Errorf("%w: the site lists %d jobs and each costs a detail request, over the %d limit",
			srcutil.ErrTooManyPostings, pl.New+pl.Deferred, MaxDetails)
	}
	st.Deferred, st.Prefiltered = pl.Deferred, pl.Prefiltered
	slot := make([]*posting.Posting, len(rows))
	for _, i := range pl.Reuse {
		p := stored[keys[i]]
		p.SeenAt = seen
		slot[i] = &p
		st.Reused++
	}

	allowed := srcutil.MaxGone(len(pl.Fetch))
	for n, i := range pl.Fetch {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r := rows[i]
		p, err := s.detail(ctx, pace, b, host, company, r, keys[i], seen)
		if errors.Is(err, errDetailGone) {
			// The job closed between the sitemap and its page; leaving it out
			// is the truth. Many at once is the site failing, not jobs closing,
			// and would empty a healthy board. With every other row reused, one
			// new job gone is only that job. Deliberately not ErrBoardNotFound:
			// the sitemap answered.
			if st.Gone++; st.Gone > allowed || (st.Gone == len(pl.Fetch) && st.Reused == 0) {
				return nil, fmt.Errorf("%d of %d job pages are gone (HTTP 404); the site is not answering", st.Gone, len(pl.Fetch))
			}
			continue
		}
		if err != nil {
			// A partial listing would make the reconciler age healthy postings
			// as absent, so one bad page fails the whole board.
			return nil, fmt.Errorf("job %s: %w", r.id, err)
		}
		if n >= pl.New {
			st.Refreshed++
		} else {
			st.Fetched++
		}
		slot[i] = &p
	}
	out := make([]posting.Posting, 0, len(rows))
	for _, p := range slot {
		if p != nil {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (s *source) detail(ctx context.Context, pace *srcutil.Pacer, b sources.Board, host, company string, r row, key posting.Key, seen time.Time) (posting.Posting, error) {
	if err := pace.Wait(ctx); err != nil {
		return posting.Posting{}, err
	}
	page, err := srcutil.GetBodyLimit(ctx, s.d, r.loc, "text/html,application/xhtml+xml", maxDetailBody)
	if err != nil {
		if errors.Is(err, srcutil.ErrBoardNotFound) {
			return posting.Posting{}, errDetailGone
		}
		return posting.Posting{}, err
	}
	jp, ok := findJobPosting(string(page), host, company, r.id)
	if !ok {
		return posting.Posting{}, fmt.Errorf("%w: the page has no JobPosting for job %s", ErrShape, r.id)
	}
	return jp.posting(b, r, key, seen)
}
