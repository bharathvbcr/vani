// Package jibe lists postings from an iCIMS Jibe careers site, the system
// behind careers.amd.com (verified 2026-10-19):
//
//	GET https://{host}/api/jobs?page={n}&limit=100&keywords={term}&location={place}
//
// The board token is the site's Jibe client code ("amd") and Host its careers
// site. Every row carries its description, so a listing costs one request per
// hundred postings and no details requests. Search is optional, as on Workday:
// without one the board lists the whole site (AMD: 1286 postings in 13 pages
// of about 3.5 MB each on 2026-10-20).
// AMD's robots.txt allows everything with a crawl-delay of 5 seconds, which
// the source keeps between its own requests.
//
// This is the Jibe front end, not the classic {tenant}.icims.com/jobs/search
// portal; postings' apply links still point into the latter.
package jibe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	// pageSize is the largest page the API serves: limit=101 answered 422 on
	// 2026-10-19.
	pageSize = 100
	maxPages = srcutil.MaxPostings/pageSize + 2
	// crawlDelay is the spacing robots.txt asks for between requests.
	crawlDelay = 5 * time.Second
	// paceBudget bounds a board's waiting, as page.BoardTimeout bounds a
	// careers page: 180 requests at the crawl delay. AMD's whole site takes
	// 14 (2026-10-20).
	paceBudget = 15 * time.Minute
	// dateLayout is posted_date's form ("2026-10-20T01:55:00+0000"), which is
	// not RFC 3339: the offset has no colon.
	dateLayout = "2006-01-02T15:04:05-0700"
	// jibeHostSuffix follows a client code in the domain Jibe hosts every
	// client's site on (amd.jibeapply.com, githubinc.jibeapply.com).
	jibeHostSuffix = ".jibeapply.com"
)

// ErrShape is a response whose layout is not the one this source knows.
var ErrShape = errors.New("jibe: unexpected response")

var (
	hostRE   = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)
	clientRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	// idRE is a req_id that is safe as one URL path segment and key part.
	// AMD's are digits; PepsiCo's site also lists Paradox-hosted roles as
	// "P1-6635769-1" (190 of 431 rows of one search on 2026-10-20).
	idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
)

type source struct {
	d   egress.Doer
	now func() time.Time
	// wait spaces requests (srcutil.Pacer.Sleep); a field only so tests need
	// not wait in real time.
	wait func(ctx context.Context, d time.Duration) error
}

// New returns the Jibe source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now, wait: srcutil.Sleep} }

func (*source) ATS() posting.ATS { return posting.Jibe }

type listing struct {
	TotalCount *int  `json:"totalCount"`
	Jobs       []row `json:"jobs"`
}

type row struct {
	Data *job `json:"data"`
}

type job struct {
	ReqID            string   `json:"req_id"`
	ClientCode       string   `json:"client_code"`
	Title            string   `json:"title"`
	FullLocation     string   `json:"full_location"`
	Department       string   `json:"department"`
	Category         []string `json:"category"`
	Description      string   `json:"description"`
	Responsibilities string   `json:"responsibilities"`
	Qualifications   string   `json:"qualifications"`
	PostedDate       string   `json:"posted_date"`
	UpdateDate       string   `json:"update_date"`
	ApplyURL         string   `json:"apply_url"`
	LocationName     string   `json:"location_name"`
	// EmploymentType is FULL_TIME, PART_TIME, TEMPORARY or CONTRACTOR, or
	// absent (AMD and PepsiCo, 2026-10-20).
	EmploymentType string `json:"employment_type"`
	// Tags2 and Tags3 are AMD's pay range bottom and top, one value each
	// ("USD $178,500.00/Yr."); see payLine.
	Tags2    []string `json:"tags2"`
	Tags3    []string `json:"tags3"`
	MetaData struct {
		CanonicalURL string `json:"canonical_url"`
	} `json:"meta_data"`
}

// query is one listing request series: a search term and a location value,
// either of which may be empty.
type query struct{ term, location string }

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := srcutil.CheckBoard(b, posting.Jibe); err != nil {
		return nil, err
	}
	client := strings.ToLower(strings.TrimSpace(b.Token))
	host := strings.ToLower(strings.TrimSpace(b.Host))
	if !clientRE.MatchString(client) {
		return nil, fmt.Errorf("jibe %q: token must be the site's client code, e.g. amd", b.Token)
	}
	if !hostRE.MatchString(host) {
		return nil, fmt.Errorf("jibe %q: host %q is not a hostname", b.Token, b.Host)
	}
	if err := sources.CheckFilters(posting.Jibe, b.Filters); err != nil {
		return nil, fmt.Errorf("jibe %q: %w", b.Token, err)
	}
	queries := plan(b)
	l := &lister{s: s, host: host, client: client, b: b, seen: s.now(), have: map[string]bool{},
		pace: srcutil.Pacer{Every: crawlDelay, Budget: paceBudget, Sleep: s.wait}}
	for _, q := range queries {
		if err := l.run(ctx, q); err != nil {
			return nil, fmt.Errorf("jibe %q: %s: %w", b.Token, q, err)
		}
	}
	return l.out, nil
}

func (q query) String() string {
	switch {
	case q.term != "" && q.location != "":
		return fmt.Sprintf("search %q in %q", q.term, q.location)
	case q.term != "":
		return fmt.Sprintf("search %q", q.term)
	case q.location != "":
		return fmt.Sprintf("listing in %q", q.location)
	}
	return "listing"
}

// plan is every term crossed with every location value. The site reads only
// the first of repeated location parameters (2026-10-19: Canada&Austin gave
// Canada's 10, Austin&Canada Austin's 28), so each value is its own query and
// the board is their union.
func plan(b sources.Board) []query {
	terms := b.SearchTerms()
	if len(terms) == 0 {
		terms = []string{""}
	}
	var places []string
	for _, v := range b.Filters["location"] {
		if v = strings.TrimSpace(v); v != "" && !slices.Contains(places, v) {
			places = append(places, v)
		}
	}
	if len(places) == 0 {
		places = []string{""}
	}
	out := make([]query, 0, len(terms)*len(places))
	for _, t := range terms {
		for _, p := range places {
			out = append(out, query{term: t, location: p})
		}
	}
	return out
}

// lister accumulates one board's postings across its queries.
type lister struct {
	s      *source
	host   string
	client string
	b      sources.Board
	seen   time.Time
	have   map[string]bool
	out    []posting.Posting
	pace   srcutil.Pacer // spaces every request of the board
}

func (l *lister) get(ctx context.Context, q query, page int) (listing, error) {
	if err := l.pace.Wait(ctx); err != nil {
		return listing{}, err
	}
	v := url.Values{}
	v.Set("page", strconv.Itoa(page))
	v.Set("limit", strconv.Itoa(pageSize))
	v.Set("sortBy", "relevance")
	v.Set("descending", "false")
	v.Set("internal", "false")
	if q.term != "" {
		v.Set("keywords", q.term)
	}
	if q.location != "" {
		v.Set("location", q.location)
	}
	var ls listing
	err := srcutil.GetJSON(ctx, l.s.d, "https://"+l.host+"/api/jobs?"+v.Encode(), &ls)
	var se *egress.StatusError
	if q.location != "" && errors.As(err, &se) && se.Code == http.StatusUnprocessableEntity {
		// The site answers 422, not an empty list, for a place it does not know.
		return listing{}, fmt.Errorf("location %q is not one the site knows: %w", q.location, err)
	}
	return ls, err
}

// run pages one query to its total; a total over the cap fails first.
func (l *lister) run(ctx context.Context, q query) error {
	read := map[string]bool{}
	total := -1
	for page := 1; page <= maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		ls, err := l.get(ctx, q, page)
		if err != nil {
			return err
		}
		if ls.TotalCount == nil || *ls.TotalCount < 0 || ls.Jobs == nil {
			return fmt.Errorf("%w: no total or jobs array", ErrShape)
		}
		if total < 0 {
			total = *ls.TotalCount
			if err := srcutil.CheckCount(total); err != nil {
				return fmt.Errorf("%w; use a narrower search or the location filter", err)
			}
		}
		added := 0
		for i, r := range ls.Jobs {
			if r.Data == nil {
				return fmt.Errorf("%w: page %d row %d has no data", ErrShape, page, i)
			}
			id := strings.TrimSpace(r.Data.ReqID)
			if !idRE.MatchString(id) {
				return fmt.Errorf("%w: req_id %q", ErrShape, r.Data.ReqID)
			}
			// The key lowercases the id, so ids differing only in case are one
			// posting.
			lid := strings.ToLower(id)
			if read[lid] {
				continue
			}
			read[lid] = true
			added++
			if l.have[lid] {
				continue // already listed by an earlier query
			}
			p, err := l.posting(*r.Data, id)
			if err != nil {
				return fmt.Errorf("req %s: %w", id, err)
			}
			l.have[lid] = true
			l.out = append(l.out, p)
			if err := srcutil.CheckCount(len(l.out)); err != nil {
				return fmt.Errorf("%w: the board's queries together list more; narrow them", err)
			}
		}
		if len(read) >= total {
			return nil
		}
		if added == 0 {
			// A partial listing would age healthy postings as absent.
			return fmt.Errorf("%w: page %d added nothing with %d of %d read", ErrShape, page, len(read), total)
		}
	}
	return fmt.Errorf("%w: listing did not end within %d pages", ErrShape, maxPages)
}

func (l *lister) posting(j job, id string) (posting.Posting, error) {
	// Unsearched listings carry the client code, searched ones omit it.
	if c := strings.TrimSpace(j.ClientCode); c != "" && !strings.EqualFold(c, l.client) {
		return posting.Posting{}, fmt.Errorf("%w: client_code %q is not %q", ErrShape, c, l.client)
	}
	u := "https://" + l.host + "/jobs/" + id
	if c := j.MetaData.CanonicalURL; c != "" {
		// The canonical may name the board's host or Jibe's hosted copy of
		// the same site, {client}.jibeapply.com: GitHub's rows all did on
		// 2026-10-28 while www.github.careers served each job. The posting
		// keeps the board's host either way.
		cu, err := url.Parse(c)
		ownHost := err == nil && (strings.EqualFold(cu.Host, l.host) || strings.EqualFold(cu.Host, l.client+jibeHostSuffix))
		if err != nil || cu.Scheme != "https" || !ownHost || cu.Path != "/jobs/"+id || cu.User != nil {
			return posting.Posting{}, fmt.Errorf("%w: canonical_url %q is not %s", ErrShape, c, u)
		}
	}
	title := srcutil.Title(j.Title)
	if title == "" {
		return posting.Posting{}, fmt.Errorf("%w: empty title", ErrShape)
	}
	apply := strings.TrimSpace(j.ApplyURL)
	if err := srcutil.CheckURL("apply_url", apply); err != nil {
		return posting.Posting{}, err
	}
	posted, err := parseTime("posted_date", j.PostedDate)
	if err != nil {
		return posting.Posting{}, err
	}
	updated, err := parseTime("update_date", j.UpdateDate)
	if err != nil {
		return posting.Posting{}, err
	}
	key, err := posting.JibeKey(l.client, id)
	if err != nil {
		return posting.Posting{}, err
	}
	company := l.b.Company
	if company == "" {
		company = l.client
	}
	dept := strings.TrimSpace(j.Department)
	if dept == "" {
		dept = joinUnique(j.Category)
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.Jibe,
		Company:     company,
		Title:       title,
		Location:    strings.TrimSpace(j.FullLocation),
		Department:  dept,
		Remote:      homeOffice(j.LocationName),
		URL:         u,
		ApplyURL:    apply,
		PostedAt:    posted,
		UpdatedAt:   updated,
		Description: description(j),
		SeenAt:      l.seen,
	}, nil
}

// homeOffice reports whether location_name places the role in a home office,
// AMD's only mark of a remote role: the payload has no remote field, and
// full_location names just the state or country ("US,TX,Home Office" is
// "Texas, United States"). Live on 2026-10-20, 33 of 1286 AMD rows had a
// comma-separated part "Home Office" or "Home Office_Area1"/"_Area2", and one
// of 431 PepsiCo rows wrote it in a dash-separated name ("Neu-Isenburg Home
// Office-DEU"); no title or full_location said remote. A part counts when it
// is "home office", starts "home office_" or ends " home office", so "Home
// Officers" and "Home Office Depot" do not.
func homeOffice(name string) bool {
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == ',' || r == '-' }) {
		p := strings.ToLower(strings.Join(strings.Fields(part), " "))
		if p == "home office" || strings.HasPrefix(p, "home office_") || strings.HasSuffix(p, " home office") {
			return true
		}
	}
	return false
}

// payRE is one AMD pay tag: an ISO currency, its symbol, an amount with
// thousands commas and a period ("INR ₹3,338,160.00/Yr."). Live on
// 2026-10-20 all 2566 tags of 1286 rows had this form and the period Yr.
var payRE = regexp.MustCompile(`^([A-Z]{3}) \D{0,4}?([0-9]{1,3}(?:,[0-9]{3})*(?:\.[0-9]{1,2})?)/(Yr|Mo|Wk|Hr|Day)\.?$`)

// payPeriods maps payRE's period to a word srcutil.PayLine reads.
var payPeriods = map[string]string{"Yr": "year", "Mo": "month", "Wk": "week", "Hr": "hour", "Day": "day"}

// payLine renders the tags2..tags3 pay range, or "" when the pair is not one
// range: not exactly one tag each, a tag of another form, two currencies or
// periods, or zero pay (AMD's internships list "USD $0.00/Yr."). A tag this
// cannot read leaves the posting without a pay line rather than failing it:
// pay is shown and filtered on, never needed to list.
func payLine(lo, hi []string) string {
	if len(lo) != 1 || len(hi) != 1 {
		return ""
	}
	a, b := payRE.FindStringSubmatch(strings.TrimSpace(lo[0])), payRE.FindStringSubmatch(strings.TrimSpace(hi[0]))
	if a == nil || b == nil || a[1] != b[1] || a[3] != b[3] {
		return ""
	}
	amount := func(s string) *float64 {
		v, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
		if err != nil {
			return nil
		}
		return &v
	}
	return srcutil.PayLine(a[1], amount(a[2]), amount(b[2]), payPeriods[a[3]])
}

func parseTime(field, s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(dateLayout, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("%w: %s %q", ErrShape, field, s)
}

func joinUnique(ss []string) string {
	var out []string
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return strings.Join(out, "; ")
}

// description is the job description as text, followed by the
// responsibilities and qualifications fields where the description does not
// already hold them: AMD's description embeds its responsibilities on some
// postings and not others, and never its qualifications (2026-10-19). The pay
// line and the employment type lead (srcutil.Lead), so the description cap
// never cuts them.
func description(j job) string {
	body := j.Description
	text := htmltext.Text(body, srcutil.MaxDescription)
	for _, extra := range []string{j.Responsibilities, j.Qualifications} {
		t := htmltext.Text(extra, srcutil.MaxDescription)
		if t == "" || strings.Contains(text, t) {
			continue
		}
		body += "<br>" + extra
		text = htmltext.Text(body, srcutil.MaxDescription)
	}
	lead, room := srcutil.Lead(payLine(j.Tags2, j.Tags3), srcutil.RoleLine(srcutil.EmploymentType(j.EmploymentType)))
	if lead == "" {
		return text
	}
	return lead + htmltext.Text(body, room)
}
