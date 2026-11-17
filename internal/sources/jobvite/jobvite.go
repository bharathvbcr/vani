// Package jobvite lists postings from a Jobvite careers site through two
// kinds of unauthenticated GET, checked live on 2026-11-17 (Nutanix):
//
//	GET https://jobs.jobvite.com/{token}/jobs
//	GET https://jobs.jobvite.com/{token}/job/{id}
//
// The list page (108 KB, 126 jobs for Nutanix) is HTML: one table row per job
// with the title in <td class="jv-job-list-name"><a href="/{token}/job/{id}">
// and the place in <td class="jv-job-list-location">, grouped under category
// headings, all on one page. It carries no description, so each new job costs
// a detail page (78 KB) whose <h2 class="jv-header"> is the title,
// <p class="jv-job-detail-meta"> the category, place and requisition number,
// and <div class="jv-job-detail-description"> the text. Neither page names a
// posting date. There is no JSON-LD.
//
// A closed job answers 303 to /careers/{token}/jobs?error=404, which is
// "gone" (with a 404), bounded by srcutil.MaxGone. A company Jobvite does not
// serve answers 302 to search.jobvite.com, a host egress does not allow: the
// board fails. robots.txt answers 404 (no rules), and is still read for both
// paths before the list, failing the board if it ever disallows them.
//
// Detail volume is bounded as talentbrew's is (srcutil.PlanDetails): stored
// postings are reused (re-read on their turn), new rows DropListed rejects
// (by the list's title and place) are never fetched, and at most MaxDetails
// pages go out a run. A list page that shows pagination, a row this source
// cannot read, or a list with no job is an error, never a shorter list.
package jobvite

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
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
	// Host is the one host every Jobvite careers site is served from.
	Host = "jobs.jobvite.com"

	// MaxDetails is the most job pages one board reads a run: at paceEvery
	// apart, five minutes.
	MaxDetails = 150
	paceEvery  = 2 * time.Second
	paceBudget = 10 * time.Minute

	// maxListBody caps the list page (108 KB for 126 jobs); maxDetailBody one
	// job page (78 KB).
	maxListBody   = 8 << 20
	maxDetailBody = 2 << 20
	// maxRows bounds the rows read from one list page.
	maxRows = 5000
)

// ErrShape is a page whose layout is not the one this source knows.
var ErrShape = errors.New("jobvite: unexpected response")

var (
	tokenRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	idRE    = regexp.MustCompile(`^[A-Za-z0-9]{1,20}$`)
	// rowRE is one list row: the job's link and title cell, then its place
	// cell. The cells are what Jobvite writes; their text is untrusted.
	rowRE = regexp.MustCompile(`(?s)<td class="jv-job-list-name">\s*<a href="/([a-z0-9-]{1,63})/job/([A-Za-z0-9]{1,20})"[^>]{0,200}>(.{0,1000}?)</a>\s*</td>\s*<td class="jv-job-list-location">(.{0,1000}?)</td>`)
	// metaRE and headerRE are the detail page's title and meta line.
	headerRE = regexp.MustCompile(`(?s)<h2 class="jv-header">(.{0,1000}?)</h2>`)
	metaRE   = regexp.MustCompile(`(?s)<p class="jv-job-detail-meta">(.{0,1000}?)</p>`)
	sepRE    = regexp.MustCompile(`<span class='jv-inline-separator'></span>`)
	descOpen = `<div class="jv-job-detail-description"`
	descEnd  = `<div class="jv-job-detail-bottom-actions">`
)

// errDetailGone is a job page answering 404, or sending the request back to
// the list with error=404, under a list that named it.
var errDetailGone = errors.New("detail is gone (HTTP 404, or sent back to the list as not found)")

// Stats counts what one List did. Every listed job is counted once, in Reused,
// Fetched, Refreshed, Prefiltered, Deferred, Gone or Unanswered.
type Stats struct {
	Listed      int `json:"listed"`
	Reused      int `json:"reused"`
	Fetched     int `json:"fetched"`
	Refreshed   int `json:"refreshed,omitempty"`
	Prefiltered int `json:"prefiltered,omitempty"`
	Deferred    int `json:"deferred,omitempty"`
	Gone        int `json:"gone,omitempty"`
	Unanswered  int `json:"unanswered,omitempty"`
}

// Options turn on the bounded detail step (NewWith).
type Options struct {
	// Stored returns the postings an earlier run stored for b. Required.
	Stored func(b sources.Board) []posting.Posting
	// Unusable reports a stored posting whose text must not be reused.
	Unusable func(k posting.Key) bool
	// DropListed reports whether the engine drops a posting before triage from
	// what its list row says (title, place, company). Nil drops nothing.
	DropListed func(p posting.Posting) bool
	// Report receives each listed board's counts, once.
	Report func(sources.Board, Stats)
	// Wait spaces the board's requests; nil is srcutil.Sleep.
	Wait func(ctx context.Context, d time.Duration) error
}

type source struct {
	d    egress.Doer
	now  func() time.Time
	wait func(ctx context.Context, d time.Duration) error
	opt  *Options
}

// New returns the Jobvite source. Every listed job costs a detail request, and
// a board needing more than MaxDetails fails.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now, wait: srcutil.Sleep} }

// NewWith returns the source with the bounded detail step.
func NewWith(d egress.Doer, o Options) (sources.Source, error) {
	if o.Stored == nil {
		return nil, errors.New("jobvite: the bounded detail step needs the stored postings (Options.Stored)")
	}
	wait := o.Wait
	if wait == nil {
		wait = srcutil.Sleep
	}
	return &source{d: d, now: time.Now, wait: wait, opt: &o}, nil
}

func (*source) ATS() posting.ATS { return posting.Jobvite }

// row is one job the list names.
type row struct {
	id, title, place string
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckBoard(b, posting.Jobvite); err != nil {
		return nil, err
	}
	if !tokenRE.MatchString(b.Token) {
		return nil, fmt.Errorf("jobvite %q: token must be the site's lower-case company slug, e.g. nutanix", b.Token)
	}
	if len(b.SearchTerms()) > 0 || len(b.Filters) > 0 || b.Region != "" || b.MaxPostings != 0 || b.URL != "" ||
		b.LinkPattern != "" || b.Host != "" || b.BoardName != "" {
		return nil, fmt.Errorf("jobvite %q: searches, filters, region, max_postings, url, link_pattern, host and board_name do not apply", b.Token)
	}
	pace := &srcutil.Pacer{Every: paceEvery, Budget: paceBudget, Sleep: s.wait}
	for _, p := range []string{"/" + b.Token + "/jobs", "/" + b.Token + "/job/"} {
		if err := pace.Wait(ctx); err != nil {
			return nil, err
		}
		ok, err := page.RobotsAllows(ctx, s.d, Host, p)
		if err != nil {
			return nil, fmt.Errorf("jobvite %q: %w", b.Token, err)
		}
		if !ok {
			return nil, fmt.Errorf("jobvite %q: robots.txt of %s disallows %s", b.Token, Host, p)
		}
	}
	if err := pace.Wait(ctx); err != nil {
		return nil, err
	}
	tr := srcutil.NewTracker(s.d)
	listURL := "https://" + Host + "/" + b.Token + "/jobs"
	body, err := srcutil.GetBodyLimit(ctx, tr, listURL, "text/html,application/xhtml+xml", maxListBody)
	if final := tr.Final(); final != nil && (!strings.EqualFold(final.Hostname(), Host) || final.EscapedPath() != "/"+b.Token+"/jobs") {
		return nil, fmt.Errorf("jobvite %q: %w: the list went to %s%s (no such company on Jobvite)", b.Token, srcutil.ErrBoardNotFound, final.Hostname(), final.EscapedPath())
	}
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) && errors.Is(err, egress.ErrNotAllowed) {
			return nil, fmt.Errorf("jobvite %q: %w: the list redirected to a host that is not allowed (no such company on Jobvite)", b.Token, srcutil.ErrBoardNotFound)
		}
		return nil, fmt.Errorf("jobvite %q: %w", b.Token, err)
	}
	rows, err := parseList(string(body), b.Token)
	if err != nil {
		return nil, fmt.Errorf("jobvite %q: %w", b.Token, err)
	}
	if err := srcutil.CheckCount(len(rows)); err != nil {
		return nil, fmt.Errorf("jobvite %q: %w", b.Token, err)
	}
	st := Stats{Listed: len(rows)}
	if s.opt != nil && s.opt.Report != nil {
		defer func() { s.opt.Report(b, st) }()
	}
	out, err := s.details(ctx, pace, b, rows, &st)
	if err != nil {
		return nil, fmt.Errorf("jobvite %q: %w", b.Token, err)
	}
	return out, nil
}

// parseList reads the list page's rows, each job once, in page order. A page
// with pagination, a job-list cell this source cannot read, a row of another
// company, more than maxRows rows, or no job at all is an error.
func parseList(doc, token string) ([]row, error) {
	if strings.Contains(doc, "jv-pagination") {
		return nil, fmt.Errorf("%w: the list is paginated, which this source does not follow", ErrShape)
	}
	cells := strings.Count(doc, `<td class="jv-job-list-name">`)
	matches := rowRE.FindAllStringSubmatch(doc, maxRows+1)
	if len(matches) > maxRows {
		return nil, fmt.Errorf("%w: the list has more than %d rows", ErrShape, maxRows)
	}
	if len(matches) != cells {
		return nil, fmt.Errorf("%w: %d job rows, of which %d could be read", ErrShape, cells, len(matches))
	}
	var rows []row
	seen := map[string]bool{}
	for _, m := range matches {
		if m[1] != token {
			return nil, fmt.Errorf("%w: a row links to company %q, not %q", ErrShape, m[1], token)
		}
		if seen[m[2]] {
			continue
		}
		seen[m[2]] = true
		title := line(m[3])
		if title == "" {
			return nil, fmt.Errorf("%w: job %s has an empty title", ErrShape, m[2])
		}
		rows = append(rows, row{id: m[2], title: title, place: place(m[4])})
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: the list names no job", ErrShape)
	}
	return rows, nil
}

// place is a location cell as one line: its lines joined by ", ".
func place(cell string) string {
	var parts []string
	for _, p := range strings.Split(htmltext.Plain(html.UnescapeString(stripTags(cell)), 4000), "\n") {
		if p = strings.TrimSuffix(strings.TrimSpace(p), ","); p != "" {
			parts = append(parts, p)
		}
	}
	return line(strings.Join(parts, ", "))
}

var tagRE = regexp.MustCompile(`<[^>]{0,500}>`)

func stripTags(s string) string { return tagRE.ReplaceAllString(s, "\n") }

func line(s string) string {
	return htmltext.Plain(srcutil.Title(html.UnescapeString(stripTags(s))), srcutil.MaxTitle)
}

func jobURL(token, id string) string { return "https://" + Host + "/" + token + "/job/" + id }

func company(b sources.Board) string {
	if c := strings.TrimSpace(b.Company); c != "" {
		return c
	}
	return b.Token
}

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
		key, err := posting.JobviteKey(b.Token, r.id)
		if err != nil {
			return nil, err
		}
		keys[i] = key
		_, ok := stored[key]
		plan[i] = srcutil.DetailRow{ID: r.id, Stored: ok, Unusable: ok && s.opt != nil && srcutil.Marked(s.opt.Unusable, key)}
		if !ok && s.opt != nil && s.opt.DropListed != nil {
			plan[i].Drop = s.opt.DropListed(posting.Posting{ATS: posting.Jobvite, Company: company(b), Title: r.title, Location: r.place})
		}
	}
	pl := srcutil.PlanDetails(plan, srcutil.Day(seen), MaxDetails)
	if s.opt == nil && pl.Deferred > 0 {
		return nil, fmt.Errorf("%w: the site lists %d jobs and each costs a detail request, over the %d limit",
			srcutil.ErrTooManyPostings, pl.New+pl.Deferred, MaxDetails)
	}
	st.Deferred, st.Prefiltered = pl.Deferred, pl.Prefiltered
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
			return fmt.Errorf("%d of %d job pages are gone (HTTP 404, or sent back to the list); the site is not answering", gone, fetch)
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
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	if err := pace.Wait(ctx); err != nil {
		return posting.Posting{}, err
	}
	tr := srcutil.NewTracker(s.d)
	addr := jobURL(b.Token, r.id)
	body, err := srcutil.GetBodyLimit(ctx, tr, addr, "text/html,application/xhtml+xml", maxDetailBody)
	if final := tr.Final(); final != nil && strings.EqualFold(final.Hostname(), Host) &&
		final.EscapedPath() == "/careers/"+b.Token+"/jobs" && final.Query().Get("error") == "404" {
		return posting.Posting{}, errDetailGone
	}
	if err != nil {
		if errors.Is(err, srcutil.ErrBoardNotFound) {
			return posting.Posting{}, errDetailGone
		}
		return posting.Posting{}, err
	}
	if final := tr.Final(); final != nil && (!strings.EqualFold(final.Hostname(), Host) || final.Scheme != "https" ||
		final.EscapedPath() != "/"+b.Token+"/job/"+r.id) {
		return posting.Posting{}, fmt.Errorf("%w: the page for job %s came from %s%s, not its own address", ErrShape, r.id, final.Hostname(), final.EscapedPath())
	}
	return parseDetail(string(body), b, r, key, addr, seen)
}

// parseDetail reads a job page: its title (else the list's), the place from
// the list row, and the description up to the bottom actions.
func parseDetail(doc string, b sources.Board, r row, key posting.Key, addr string, seen time.Time) (posting.Posting, error) {
	open := strings.Index(doc, descOpen)
	if open < 0 {
		return posting.Posting{}, fmt.Errorf("%w: job %s's page has no description block", ErrShape, r.id)
	}
	gt := strings.IndexByte(doc[open:], '>')
	if gt < 0 {
		return posting.Posting{}, fmt.Errorf("%w: job %s's description block is cut", ErrShape, r.id)
	}
	start := open + gt + 1
	end := strings.Index(doc[start:], descEnd)
	if end < 0 {
		return posting.Posting{}, fmt.Errorf("%w: job %s's description block has no end", ErrShape, r.id)
	}
	title := r.title
	if m := headerRE.FindStringSubmatch(doc); m != nil {
		if t := line(m[1]); t != "" {
			title = t
		}
	}
	var lead string
	if m := metaRE.FindStringSubmatch(doc); m != nil {
		parts := sepRE.Split(m[1], -1)
		if len(parts) > 0 {
			if cat := line(parts[0]); cat != "" {
				lead = "Team: " + cat + "\n"
			}
		}
	}
	desc := htmltext.Text(doc[start:start+end], srcutil.MaxDescription-len(lead))
	if strings.TrimSpace(desc) == "" {
		return posting.Posting{}, fmt.Errorf("%w: job %s has an empty description", ErrShape, r.id)
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.Jobvite,
		Company:     company(b),
		Title:       title,
		Location:    r.place,
		Remote:      strings.Contains(strings.ToLower(r.place), "remote"),
		URL:         addr,
		Description: lead + desc,
		SeenAt:      seen,
	}, nil
}
