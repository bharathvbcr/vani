// Package smartrecruiters lists postings from a SmartRecruiters company's
// public Posting API, through two unauthenticated GETs (verified 2026-10-27 on
// servicenow, canva):
//
//	GET https://api.smartrecruiters.com/v1/companies/{company}/postings?limit=100&offset={n}[&country={cc}]
//	GET https://api.smartrecruiters.com/v1/companies/{company}/postings/{id}
//
// The board token is the company identifier ("servicenow"; the API ignores its
// case). The listing answers totalFound and content[], 100 rows at most per
// page (limit=200 is clamped to 100) and an empty content[] at an offset past
// the end. It carries each posting's title, structured location, department,
// employment type and releasedDate but no description, so every posting costs a
// detail request: the detail adds postingUrl, applyUrl and jobAd.sections, the
// description as HTML. Neither endpoint carries a structured pay range, so none
// is read; dollar ranges that a company writes into its description stay in the
// description text.
//
// Two things the API does that this source cannot see through:
//
//   - An unknown company answers 200 with totalFound 0, never 404. A mistyped
//     token is an empty board (a valid result, see sources.Source), and a
//     company that closes every posting looks the same.
//   - The listing has no updated stamp. releasedDate is when the posting was
//     published (it matched the detail's on every posting sampled), so a
//     description edited in place moves nothing a listing shows. Stored
//     postings are therefore re-read on a rolling schedule
//     (srcutil.RefreshDays), and
//     at once when releasedDate no longer matches the stored posting's.
//
// The country filter (Board.Filters["country"]) is the one query narrowing the
// API offers that fits the filters model: ServiceNow lists 732 postings, 441 of
// them with country=us. The API wants one lower-case two-letter code ("us";
// "US", "us,ca" and a repeated parameter all answer zero postings), so the
// source normalises and checks each value and runs one listing per value. A
// posting outside every listed country is, to Vani, gone from the board and
// ages out like any absent posting.
//
// api.smartrecruiters.com/robots.txt disallows every path for every agent but
// LinkedInBot (checked 2026-10-27). The Posting API is SmartRecruiters' own
// documented public API for exactly this read, so this source sends no robots
// check of its own; the board owner decides whether to list a board on it.
//
// Detail volume is bounded as Workday's is: a source from New fails a board that
// needs more than MaxDetails detail requests; one from NewWith reuses the
// postings an earlier run stored, drops new postings DropListed rejects from
// their listing row without a request, and defers new postings past MaxDetails
// to the next run. Stored postings are matched by key (sr:{token}:{id}), which
// the listing row carries, so a deferred row is never a stored one.
package smartrecruiters

import (
	"context"
	"errors"
	"fmt"
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
	base = "https://api.smartrecruiters.com/v1/companies/"
	// postingHost is where a posting's own page and apply link live (postingUrl
	// and applyUrl of every posting sampled on 2026-10-27).
	postingHost = "jobs.smartrecruiters.com"

	// MaxPostings bounds one board: what one country query (or the unfiltered
	// board) counts, and the union over the board's countries.
	MaxPostings = 3000
	// MaxDetails is the most detail requests one board spends in one run.
	MaxDetails = 300
	// pageSize is the most rows the listing serves per request. A larger limit
	// is clamped to it, so it is asked for as written and never trusted back.
	pageSize = 100
	// maxPages bounds one query's pages: every row of the cap, and slack.
	maxPages = MaxPostings/pageSize + 3

	// A listing page of 100 rows is about 230 KB (ServiceNow, custom fields
	// included) and a detail about 9 KB: a few MiB bounds either far more
	// tightly than srcutil's 64 MiB default.
	listMaxBody   = 4 << 20
	detailMaxBody = 2 << 20

	// paceEvery spaces a board's requests. Forty back-to-back detail GETs on
	// 2026-10-27 all answered 200, and the API sent no rate-limit header, so
	// the rate it tolerates was not found; this is egress's one-second per-host
	// floor, which the wait spans rather than adds to.
	paceEvery = time.Second
	// paceBudget bounds a board's waiting: a full board's detail requests and
	// listing pages (up to maxPages for each of the board's countries), with room.
	paceBudget = 20 * time.Minute
)

var (
	// ErrShape is a response whose layout is not the one this source knows.
	ErrShape = errors.New("smartrecruiters: unexpected response")

	// tokenRE is a company identifier as sent in the request path; the config
	// applies the same rule. ':' and '/' never match, so a token cannot change
	// the path or split a key.
	tokenRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,99}$`)
	idRE    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// countryRE is the one form the country filter answers: two lower-case letters.
	countryRE = regexp.MustCompile(`^[a-z]{2}$`)
)

// errDetailGone is a posting detail answering 404 under a listing that
// answered: the posting closed after it was listed.
var errDetailGone = errors.New("detail is gone (HTTP 404)")

// errIncomplete is a listing that ended before it could account for every
// posting the board counts. It fails the board: a partial listing would make
// the reconciler age healthy postings as absent.
var errIncomplete = errors.New("smartrecruiters: listing is incomplete")

// shortfall is how many rows of a counted query may be missing before the
// listing is refused: a hundredth, at least one. A posting closing mid-walk
// shifts later rows up a page, so one is skipped; more is the listing failing.
func shortfall(count int) int { return max(1, count/100) }

// Stats counts what one List did. Every listed row is counted once, in Reused,
// Fetched, Refreshed, Prefiltered, Deferred or Gone.
type Stats struct {
	Listed int `json:"listed"`
	// Requests is the listing requests, every country's.
	Requests int `json:"requests"`
	// Reused rows were returned as stored, with no request.
	Reused int `json:"reused"`
	// Fetched rows were new and got a detail request; Refreshed rows were
	// stored and were re-read, on schedule or because releasedDate changed.
	Fetched   int `json:"fetched"`
	Refreshed int `json:"refreshed,omitempty"`
	// Prefiltered rows were new and dropped by DropListed without a request.
	Prefiltered int `json:"prefiltered,omitempty"`
	// Deferred rows were new but past MaxDetails: fetched on a later run. Not
	// being stored, their absence ages nothing.
	Deferred int `json:"deferred,omitempty"`
	// Gone rows answered their detail with 404: closed since listing.
	Gone int `json:"gone,omitempty"`
}

// Options turn on the bounded detail step (NewWith).
type Options struct {
	// Stored returns the postings an earlier run stored for b: every record
	// under b.KeyPrefix(), whatever its status. Required.
	Stored func(b sources.Board) []posting.Posting
	// DropListed reports whether the engine drops a posting before triage from
	// what a listed row says: its title, the board's company and its location
	// text (no description). A new row it drops is neither fetched nor returned.
	// Nil drops nothing.
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

// New returns the SmartRecruiters source, sending requests through d. Every
// listed posting costs a detail request, and a board needing more than
// MaxDetails fails.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now, wait: srcutil.Sleep} }

// NewWith returns the source with the bounded detail step: stored postings are
// reused (a share of them, and any whose releasedDate changed, re-read each
// run), new ones DropListed rejects are never fetched, and at most MaxDetails
// detail requests go out per board per run.
func NewWith(d egress.Doer, o Options) (sources.Source, error) {
	if o.Stored == nil {
		return nil, errors.New("smartrecruiters: the bounded detail step needs the stored postings (Options.Stored)")
	}
	wait := o.Wait
	if wait == nil {
		wait = srcutil.Sleep
	}
	return &source{d: d, now: time.Now, wait: wait, opt: &o}, nil
}

func (*source) ATS() posting.ATS { return posting.SmartRecruiters }

// location is the structured place of a posting, listing and detail alike.
type location struct {
	City         string `json:"city"`
	Region       string `json:"region"`
	Country      string `json:"country"`
	Remote       bool   `json:"remote"`
	Hybrid       bool   `json:"hybrid"`
	FullLocation string `json:"fullLocation"`
}

type label struct {
	Label string `json:"label"`
}

// listedRow is one listing row.
type listedRow struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	ReleasedDate     string   `json:"releasedDate"`
	Location         location `json:"location"`
	Department       label    `json:"department"`
	TypeOfEmployment label    `json:"typeOfEmployment"`
	Company          struct {
		Name string `json:"name"`
	} `json:"company"`
}

type listResponse struct {
	TotalFound *int `json:"totalFound"`
	// Content is nil when the answer has none (missing or null): an error page
	// or a new shape, never an empty board, which answers "content":[].
	Content []listedRow `json:"content"`
}

type section struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// sectionOrder is the order the description reads in: what the job is first,
// the company's own boilerplate last.
var sectionOrder = []struct{ key, head string }{
	{"jobDescription", "Job Description"},
	{"qualifications", "Qualifications"},
	{"additionalInformation", "Additional Information"},
	{"companyDescription", "Company Description"},
}

type detailResponse struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	ReleasedDate     string   `json:"releasedDate"`
	PostingURL       string   `json:"postingUrl"`
	ApplyURL         string   `json:"applyUrl"`
	Location         location `json:"location"`
	Department       label    `json:"department"`
	TypeOfEmployment label    `json:"typeOfEmployment"`
	Company          struct {
		Name       string `json:"name"`
		Identifier string `json:"identifier"`
	} `json:"company"`
	JobAd struct {
		Sections map[string]section `json:"sections"`
	} `json:"jobAd"`
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := srcutil.CheckBoard(b, posting.SmartRecruiters); err != nil {
		return nil, err
	}
	if !tokenRE.MatchString(b.Token) {
		return nil, fmt.Errorf("smartrecruiters %q: token must be the company identifier (letters, digits, '-' and '_')", b.Token)
	}
	if len(b.SearchTerms()) > 0 {
		return nil, fmt.Errorf("smartrecruiters %q: searches are not supported; narrow the board with the country filter", b.Token)
	}
	if b.Host != "" || b.Region != "" || b.MaxPostings != 0 {
		return nil, fmt.Errorf("smartrecruiters %q: host, region and max_postings do not apply", b.Token)
	}
	countries, err := countriesOf(b)
	if err != nil {
		return nil, fmt.Errorf("smartrecruiters %q: %w", b.Token, err)
	}
	pace := &srcutil.Pacer{Every: paceEvery, Budget: paceBudget, Sleep: s.wait}
	company := base + url.PathEscape(b.Token) + "/postings"

	var rows []listedRow
	have := map[string]bool{}
	st := Stats{}
	for _, cc := range countries {
		got, requests, err := s.list(ctx, pace, company, cc)
		st.Requests += requests
		if err != nil {
			return nil, fmt.Errorf("smartrecruiters %q: %s: %w", b.Token, queryName(cc), err)
		}
		for _, r := range got {
			if !have[r.ID] {
				have[r.ID] = true
				rows = append(rows, r)
			}
		}
		if len(rows) > MaxPostings {
			return nil, fmt.Errorf("smartrecruiters %q: %w: the board's countries together list more than %d postings; narrow the country filter",
				b.Token, srcutil.ErrTooManyPostings, MaxPostings)
		}
	}
	st.Listed = len(rows)
	if s.opt != nil && s.opt.Report != nil {
		defer func() { s.opt.Report(b, st) }()
	}
	out, err := s.details(ctx, pace, b, company, rows, &st)
	if err != nil {
		return nil, fmt.Errorf("smartrecruiters %q: %w", b.Token, err)
	}
	return out, nil
}

func queryName(cc string) string {
	if cc == "" {
		return "listing"
	}
	return "listing for country " + cc
}

// countriesOf is the board's country queries: one per distinct filter value,
// sorted, or the single unfiltered query "" when it has no filter. The API
// answers zero postings for any form but one lower-case two-letter code, which
// the reconciler would read as every posting closing, so a value that is not
// one is refused rather than sent.
func countriesOf(b sources.Board) ([]string, error) {
	if err := sources.CheckFilters(posting.SmartRecruiters, b.Filters); err != nil {
		return nil, err
	}
	vs := b.Filters["country"]
	if len(vs) == 0 {
		return []string{""}, nil
	}
	if len(vs) > sources.MaxFilterValues {
		return nil, fmt.Errorf("country filter has %d values, more than %d", len(vs), sources.MaxFilterValues)
	}
	var out []string
	for _, v := range vs {
		cc := strings.ToLower(strings.TrimSpace(v))
		if !countryRE.MatchString(cc) {
			return nil, fmt.Errorf("country %q must be one two-letter ISO code such as us", v)
		}
		if !slices.Contains(out, cc) {
			out = append(out, cc)
		}
	}
	slices.Sort(out)
	return out, nil
}

func listURL(company, country string, offset int) string {
	q := url.Values{}
	q.Set("limit", strconv.Itoa(pageSize))
	q.Set("offset", strconv.Itoa(offset))
	if country != "" {
		q.Set("country", country)
	}
	return company + "?" + q.Encode()
}

// list reads every page of one query (country "" is the whole board) and
// returns its rows, each id once, and the requests it sent. totalFound is read
// from the first page only. The listing is complete when the rows read reach
// it; an empty page ends it early. A page that adds no new id (a wrap back to
// the first page, or a stuck offset), a total over the cap, and a listing that
// ends short of its count each fail the query rather than truncate it. The
// next offset is the rows read, not the page size, so a server that clamps a
// page below the limit asked for is still walked to its end.
func (s *source) list(ctx context.Context, pace *srcutil.Pacer, company, country string) (rows []listedRow, requests int, err error) {
	have := map[string]bool{}
	count, read := -1, 0
	ended := false
	for pg := 0; pg < maxPages && !ended; pg++ {
		if err := ctx.Err(); err != nil {
			return nil, requests, err
		}
		if err := pace.Wait(ctx); err != nil {
			return nil, requests, err
		}
		requests++
		var lr listResponse
		if err := srcutil.GetJSONLimit(ctx, s.d, listURL(company, country, read), listMaxBody, &lr); err != nil {
			return nil, requests, err
		}
		if lr.TotalFound == nil || *lr.TotalFound < 0 {
			return nil, requests, fmt.Errorf("%w: no totalFound", ErrShape)
		}
		if lr.Content == nil {
			return nil, requests, fmt.Errorf("%w: no content array at offset %d", ErrShape, read)
		}
		if pg == 0 {
			if count = *lr.TotalFound; count > MaxPostings {
				return nil, requests, fmt.Errorf("%w: %d postings, over the %d limit; narrow the board with the country filter",
					srcutil.ErrTooManyPostings, count, MaxPostings)
			}
		}
		if len(lr.Content) == 0 {
			if pg == 0 && count > 0 {
				return nil, requests, fmt.Errorf("%w: the first page is empty though the board counts %d postings", errIncomplete, count)
			}
			ended = true
			break
		}
		added := 0
		for _, r := range lr.Content {
			if !idRE.MatchString(r.ID) {
				// A row that cannot be fetched or keyed cannot be accounted for.
				return nil, requests, fmt.Errorf("%w: posting id %q", ErrShape, r.ID)
			}
			if _, err := parseStamp(r.ReleasedDate); err != nil {
				return nil, requests, fmt.Errorf("%w: posting %s: %v", ErrShape, r.ID, err)
			}
			if !have[r.ID] {
				have[r.ID] = true
				rows = append(rows, r)
				added++
			}
		}
		if added == 0 {
			return nil, requests, fmt.Errorf("%w: the board served a page with nothing new at offset %d, after %d postings (a wrap to the first page or a stuck offset)", errIncomplete, read, len(rows))
		}
		read += len(lr.Content)
		ended = read >= count
	}
	if !ended {
		return nil, requests, fmt.Errorf("%w: the listing did not end within %d pages", errIncomplete, maxPages)
	}
	if len(rows) < count-shortfall(count) {
		return nil, requests, fmt.Errorf("%w: the board counts %d postings and listed %d", errIncomplete, count, len(rows))
	}
	return rows, requests, nil
}

// line is untrusted board text as one display line: srcutil.Title, then any
// control character it let through (a NUL, an escape) removed.
func line(s string) string { return htmltext.Plain(srcutil.Title(s), srcutil.MaxTitle) }

// parseStamp reads a releasedDate: an RFC 3339 time, or the zero time when empty.
func parseStamp(s string) (time.Time, error) { return srcutil.ParseTime("releasedDate", s) }

// boardCompany is the company a board's postings carry: the board's, else the
// name the API gives, else the token.
func boardCompany(b sources.Board, apiName string) string {
	if c := strings.TrimSpace(b.Company); c != "" {
		return c
	}
	if c := strings.TrimSpace(apiName); c != "" {
		return c
	}
	return b.Token
}

// placeOf is a location as one line: fullLocation without its empty parts
// ("Madrid, , Spain" is "Madrid, Spain"), else the city, region and upper-cased
// country code.
func placeOf(l location) string {
	join := func(parts []string) string {
		var out []string
		for _, p := range parts {
			if p = line(p); p != "" && !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
		return strings.Join(out, ", ")
	}
	if s := join(strings.Split(l.FullLocation, ",")); s != "" {
		return s
	}
	return join([]string{l.City, l.Region, strings.ToUpper(l.Country)})
}

// listedPosting is what a listed row says of its posting before its detail is
// read: the fields DropListed may judge.
func listedPosting(b sources.Board, r listedRow) posting.Posting {
	return posting.Posting{
		ATS: posting.SmartRecruiters, Company: boardCompany(b, r.Company.Name),
		Title: line(r.Name), Location: placeOf(r.Location), Remote: r.Location.Remote,
	}
}

// details turns listed rows into postings. Without Options every row is
// fetched, and a board needing more than MaxDetails fails. With Options, the
// rows are planned by srcutil.PlanDetails: a stored row is reused unless its
// turn has come or its releasedDate changed; new rows DropListed rejects are
// skipped; new rows are fetched first, then the changed ones, then the
// scheduled ones, up to MaxDetails, and the new rows past it are deferred.
// Results keep listing order.
func (s *source) details(ctx context.Context, pace *srcutil.Pacer, b sources.Board, company string, rows []listedRow, st *Stats) ([]posting.Posting, error) {
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
		key, err := posting.SmartRecruitersKey(b.Token, r.ID)
		if err != nil {
			return nil, err
		}
		keys[i] = key
		p, ok := stored[key]
		plan[i] = srcutil.DetailRow{ID: r.ID, Stored: ok}
		if ok {
			stamp, _ := parseStamp(r.ReleasedDate) // read by list
			plan[i].Changed = !p.PostedAt.Equal(stamp)
		} else if s.opt != nil && s.opt.DropListed != nil {
			plan[i].Drop = s.opt.DropListed(listedPosting(b, r))
		}
	}
	pl := srcutil.PlanDetails(plan, srcutil.Day(seen), MaxDetails)
	if s.opt == nil && pl.Deferred > 0 {
		// Without stored postings a deferred row cannot be told from a stored
		// one, and leaving out a stored one would age it as absent.
		return nil, fmt.Errorf("%w: the board lists %d postings and each costs a detail request, over the %d limit; narrow it with the country filter",
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
		p, err := s.detail(ctx, pace, company, b, r, seen)
		if errors.Is(err, errDetailGone) {
			// The posting closed between the listing and its detail; leaving it
			// out is the truth. Many at once is the detail endpoint failing, not
			// postings closing, and would empty a healthy board. With every
			// other row reused, one new posting gone is only that posting.
			// Deliberately not ErrBoardNotFound: the listing answered.
			if st.Gone++; st.Gone > allowed || (st.Gone == len(pl.Fetch) && st.Reused == 0) {
				return nil, fmt.Errorf("%d of %d posting details are gone (HTTP 404); the detail endpoint is not answering", st.Gone, len(pl.Fetch))
			}
			continue
		}
		if err != nil {
			// A partial listing would make the reconciler age healthy postings
			// as absent, so one bad posting fails the whole board.
			return nil, fmt.Errorf("posting %s: %w", r.ID, err)
		}
		if n >= pl.New {
			st.Refreshed++
		} else {
			st.Fetched++
		}
		slot[i] = &p
	}
	out := make([]posting.Posting, 0, len(rows))
	seenKeys := make(map[posting.Key]bool, len(rows))
	for _, p := range slot {
		if p == nil || seenKeys[p.Key] {
			continue
		}
		seenKeys[p.Key] = true
		out = append(out, *p)
	}
	return out, nil
}

// postingPage checks a posting URL taken from the (untrusted) detail: https on
// postingHost, its path "/{company}/{id}" or "/{company}/{id}-{slug}" with the
// company the detail names (its identifier, else the token). The stored URL is
// never one that leaves SmartRecruiters' own posting pages.
func postingPage(field, raw, company, id string) error {
	if err := srcutil.CheckURL(field, raw); err != nil {
		return err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %s %q", ErrShape, field, raw)
	}
	segs := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if u.Scheme != "https" || !strings.EqualFold(u.Hostname(), postingHost) || u.Port() != "" || len(segs) != 2 ||
		!strings.EqualFold(segs[0], company) || (segs[1] != id && !strings.HasPrefix(segs[1], id+"-")) {
		return fmt.Errorf("%w: %s %q is not a %s page for posting %s", ErrShape, field, raw, postingHost, id)
	}
	return nil
}

func (s *source) detail(ctx context.Context, pace *srcutil.Pacer, company string, b sources.Board, r listedRow, seen time.Time) (posting.Posting, error) {
	if err := pace.Wait(ctx); err != nil {
		return posting.Posting{}, err
	}
	var d detailResponse
	if err := srcutil.GetJSONLimit(ctx, s.d, company+"/"+url.PathEscape(r.ID), detailMaxBody, &d); err != nil {
		if errors.Is(err, srcutil.ErrBoardNotFound) {
			return posting.Posting{}, errDetailGone
		}
		return posting.Posting{}, err
	}
	if d.ID != r.ID {
		return posting.Posting{}, fmt.Errorf("%w: details are for posting %q, the listing row named %q", ErrShape, d.ID, r.ID)
	}
	key, err := posting.SmartRecruitersKey(b.Token, r.ID)
	if err != nil {
		return posting.Posting{}, err
	}
	title := line(d.Name)
	if title == "" {
		title = line(r.Name)
	}
	if title == "" {
		return posting.Posting{}, fmt.Errorf("%w: empty name", ErrShape)
	}
	owner := strings.TrimSpace(d.Company.Identifier)
	if owner == "" {
		owner = b.Token
	}
	if err := postingPage("postingUrl", d.PostingURL, owner, r.ID); err != nil {
		return posting.Posting{}, err
	}
	if d.ApplyURL != "" {
		if err := postingPage("applyUrl", d.ApplyURL, owner, r.ID); err != nil {
			return posting.Posting{}, err
		}
	}
	// Detail and listing carry the same stamp; the detail's is read, the row's
	// stands in for one that is missing.
	stampText := d.ReleasedDate
	if stampText == "" {
		stampText = r.ReleasedDate
	}
	posted, err := parseStamp(stampText)
	if err != nil {
		return posting.Posting{}, fmt.Errorf("%w: %v", ErrShape, err)
	}
	loc := d.Location
	if placeOf(loc) == "" {
		loc = r.Location
	}
	dept := strings.TrimSpace(d.Department.Label)
	if dept == "" {
		dept = strings.TrimSpace(r.Department.Label)
	}
	employment := d.TypeOfEmployment.Label
	if strings.TrimSpace(employment) == "" {
		employment = r.TypeOfEmployment.Label
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.SmartRecruiters,
		Company:     boardCompany(b, d.Company.Name),
		Title:       title,
		Location:    placeOf(loc),
		Remote:      loc.Remote,
		Department:  line(dept),
		URL:         d.PostingURL,
		ApplyURL:    d.ApplyURL,
		PostedAt:    posted,
		Description: description(d, employment, loc.Hybrid),
		SeenAt:      seen,
	}, nil
}

// description is the posting's sections as text, each under its heading where
// it has text, led by a srcutil.RoleLine of the employment type and a hybrid
// flag (the API's own hybrid boolean: remote is Posting.Remote).
func description(d detailResponse, employment string, hybrid bool) string {
	flags := []string{srcutil.EmploymentType(employment)}
	if hybrid {
		flags = append(flags, "hybrid")
	}
	lead, room := srcutil.Lead(srcutil.RoleLine(flags...))
	left := room
	var parts []string
	for _, sec := range sectionOrder {
		if left <= 0 {
			break
		}
		x := d.JobAd.Sections[sec.key]
		text := htmltext.Text(x.Text, left)
		if text == "" {
			continue
		}
		head := line(x.Title)
		if head == "" {
			head = sec.head
		}
		part := head + "\n" + text
		parts = append(parts, part)
		left -= len(part) + 2
	}
	return lead + htmltext.Truncate(strings.Join(parts, "\n\n"), room)
}
