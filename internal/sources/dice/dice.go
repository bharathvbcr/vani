// Package dice lists postings from Dice's public job-search MCP server,
// https://mcp.dice.com/mcp, read with Vani's own MCP client (mcphttp). Dice is
// a job board, not an employer's ATS: one search lists many employers, so each
// posting's company is its row's own, and a row that names none is a bad row
// rather than one filed under the board's name.
//
// Live facts (checked 2026-11-24):
//
//   - The endpoint speaks Streamable HTTP and answers every message as a
//     text/event-stream, needs no key and assigns no session id. Dice's own page
//     (dice.com/about/mcp) invites any MCP client to connect and asks clients
//     that hit its rate limit (HTTP 429) to back off exponentially and to cache
//     results rather than repeat requests.
//   - Three tools: search_jobs, get_job_details and get_company (never called
//     here). search_jobs takes a required keyword, location, radius with
//     radius_unit ("mi"/"km"), jobs_per_page 1..100, a 1-based page_number,
//     sort ("relevance"/"datePosted"), posted_date ONE|THREE|SEVEN,
//     workplace_types, employment_types, employer_types, willing_to_sponsor
//     and more. It answers data[] (guid, title, a ~500-character plain-text
//     summary, postedDate and modifiedDate in RFC 3339, jobLocation
//     {displayName} or null on a remote row, detailsPageUrl
//     www.dice.com/job-detail/{guid} or /direct-apply/{guid} with utm_source naming the client,
//     free-text salary, companyName, employmentType such as "Contract, Third
//     Party", workplaceTypes, isRemote, willingToSponsor ...) and metadata
//     {page, pageSize, total, totalPages, ...}. A page past totalPages is an
//     empty data[], not an error.
//   - get_job_details takes the row's guid (not its "id") and answers
//     {description: the full JD as HTML, skills: [{name}]}; an unknown guid is a
//     tool error "Job not found: <id>".
//   - Filters, keyword "machine learning engineer" (2,330 jobs): location
//     "Austin, TX" 400, with radius 10 mi 397; posted_date THREE 144;
//     workplace Remote 347, Hybrid 77, both 422 (values are a union);
//     employment FULLTIME 2,147, FULLTIME+CONTRACTS 2,323; employer Direct
//     Hire 1,805; willing_to_sponsor 43. location "Remote" is ignored (2,330),
//     so it is refused here: remote is workplace_type remote. A bad enum value
//     is a tool error.
//   - Dice's upstream search rate-limits after a few fast 100-row calls, and
//     says so as a tool error, not an HTTP status: "API request failed with
//     status 502: The Job Search API request failed with status 429". Such an
//     error is returned as a *srcutil.RateLimitedError for mcp.dice.com, so the
//     run reads the board as rate limited (retry next run), not broken. Any
//     other tool error fails the board as a plain error.
//
// Listing. The board token is "dice" and the board needs at least one search
// term; each term is a keyword, read 100 rows a page from page 1 to the
// page's totalPages and never past it, newest first (sort datePosted, so a
// posting that arrives mid-walk shifts rows down onto pages already read,
// where they are dropped as repeats, rather than reordering the walk the way a
// relevance score can). Rows are deduplicated by guid across pages and terms.
// A term whose first page counts more than MaxPostings fails the board before
// any further page, and so does a union of terms over it: a truncated listing
// would let the reconciler age out what it did not reach. Every call of a
// board, search or detail, is spaced at least a second apart.
//
// Windowed. With posted_within_days the listing is by design only the
// postings Dice dated inside that window: a posting that ages out of it is not
// closed. Such a board reports Stats.Windowed, and the run holds what it stops
// listing for a while instead of closing it. Without it the listing is the
// search's whole answer, reconciled like any board.
//
// Details. The summary is cut at ~500 characters, so a posting's text is its
// get_job_details description. Dice asks clients not to fetch details for
// every job and to cache what they read, so the detail step is SmartRecruiters'
// bounded one (srcutil.PlanDetails, srcutil.DetailStep): a source from NewWith
// reuses the postings an earlier run stored, re-reading one only when its
// turn in the RefreshDays cycle comes (not when modifiedDate moves: the ledger
// keeps a posting's first UpdatedAt when its text is unchanged, so a moved
// date would read as changed on every later run, and Dice moves it often);
// new rows
// DropListed rejects from their listing row are never fetched; at most
// MaxDetails detail calls go out per board per run, and new rows past that are
// deferred to a later run and not returned (a summary-only posting would change
// text, and be triaged again, once its detail is read). A detail call that
// fails transiently returns the stored posting (Stats.Unanswered); a "Job not
// found" detail is a posting closed since the listing and is left out, within
// srcutil.MaxGone. A source from New has no stored postings, so it reads every
// listed row's detail and fails a board that would need more than MaxDetails.
package dice

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/mcphttp"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	// Token is the one board token: the server is one site.
	Token = "dice"
	// Endpoint is Dice's MCP server.
	Endpoint = "https://mcp.dice.com/mcp"
	// MaxPostings bounds one board: what one term's first page may count, and
	// the union of the board's terms. At 100 rows a page it is 10 calls a term.
	MaxPostings = 1000
	// MaxDetails is the most get_job_details calls one board makes in one run.
	MaxDetails = 120

	host     = "mcp.dice.com"
	jobPage  = "https://www.dice.com/job-detail/"
	pageSize = 100
	// maxPages bounds one term's pages: every row of the cap, and slack.
	maxPages = MaxPostings/pageSize + 2

	// paceEvery spaces a board's calls: Dice's upstream search answered 429
	// after a few back-to-back 100-row calls on 2026-11-24.
	paceEvery = time.Second
	// paceBudget bounds a board's waiting: every page of every term at the cap
	// and MaxDetails detail calls, with room.
	paceBudget = 10 * time.Minute

	// Bounds on untrusted row fields that reach a posting.
	maxLocation  = 100
	maxSkills    = 50
	maxRoleParts = 8
)

// FilterKeys are the board filters Dice takes, each checked live on
// 2026-11-24 against result counts (package doc):
//
//	location            one place ("Austin, TX"); "Remote" is refused (Dice ignores it)
//	radius_miles        one integer 1..500, sent as radius + radius_unit "mi"; needs location
//	posted_within_days  one of 1, 3, 7 (ONE, THREE, SEVEN); makes the listing windowed
//	workplace_type      remote, on-site, hybrid (one or more, a union)
//	employment_type     fulltime, contracts, parttime, third_party, internship (a union)
//	employer_type       direct_hire, recruiter, other (a union)
//	sponsorship         "true": only employers willing to sponsor
var FilterKeys = []string{
	"location", "radius_miles", "posted_within_days",
	"workplace_type", "employment_type", "employer_type", "sponsorship",
}

var (
	// ErrShape is a response whose layout is not the one this source knows.
	ErrShape = errors.New("dice: unexpected response")
	// errIncomplete is a term's walk that ended short of the jobs it counts.
	errIncomplete = errors.New("dice: listing is incomplete")
	// errDetailGone is a detail answering "Job not found" under a listing that
	// listed the job: it closed after it was listed.
	errDetailGone = errors.New(`detail is gone ("Job not found")`)

	guidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// rateLimitRE finds an upstream 429 in a tool error's text. A guid cannot
	// match: its hex groups are four or more characters long.
	rateLimitRE = regexp.MustCompile(`\b429\b|(?i:too many requests)`)
	notFoundRE  = regexp.MustCompile(`(?i)\bjob not found\b`)
)

// Stats counts what one List did. Every listed row is counted once, in Reused,
// Fetched, Refreshed, Prefiltered, Deferred, Gone or Unanswered.
type Stats struct {
	Listed int `json:"listed"`
	// Requests is the search_jobs calls, every term's.
	Requests int `json:"requests"`
	// Matched is the jobs the terms' first pages counted, summed (overlapping
	// terms count a job once per term).
	Matched int `json:"matched"`
	// Reused rows were returned as stored, with no call.
	Reused int `json:"reused"`
	// Fetched rows were new and got a detail call; Refreshed rows were stored
	// and were re-read on their turn in the refresh cycle.
	Fetched   int `json:"fetched"`
	Refreshed int `json:"refreshed,omitempty"`
	// Prefiltered rows were new and dropped by DropListed without a call.
	Prefiltered int `json:"prefiltered,omitempty"`
	// Deferred rows were new but past MaxDetails: read on a later run, and not
	// returned now. Not being stored, their absence ages nothing.
	Deferred int `json:"deferred,omitempty"`
	// Gone rows answered their detail with "Job not found": closed since listing.
	Gone int `json:"gone,omitempty"`
	// Unanswered rows' detail call failed transiently (srcutil.DetailMisses): a
	// stored one was returned as stored, a new one left for a later run.
	Unanswered int `json:"unanswered,omitempty"`
	// Windowed is set when the board's listing is only a recency window
	// (posted_within_days), not the search's whole word.
	Windowed bool `json:"windowed,omitempty"`
}

// Options turn on the bounded detail step (NewWith).
type Options struct {
	// Stored returns the postings an earlier run stored for b: every record
	// under b.KeyPrefix(), whatever its status. Required.
	Stored func(b sources.Board) []posting.Posting
	// Unusable reports a stored posting whose text must not be reused (see
	// srcutil.DetailRow.Unusable). Nil marks none.
	Unusable func(k posting.Key) bool
	// DropListed reports whether the engine drops a posting before triage from
	// what a listed row says: its title, company and location (no
	// description). A new row it drops is neither fetched nor returned. Nil
	// drops nothing.
	DropListed func(p posting.Posting) bool
	// Report, when set, receives each listed board's counts, once, whether the
	// board then succeeded or failed on a detail.
	Report func(sources.Board, Stats)
	// Wait spaces a board's calls (srcutil.Pacer.Sleep); nil is srcutil.Sleep.
	// A field only so tests need not wait in real time.
	Wait func(ctx context.Context, d time.Duration) error
}

type source struct {
	d    egress.Doer
	now  func() time.Time
	wait func(ctx context.Context, d time.Duration) error
	opt  *Options
}

// New returns the Dice source, sending requests through d. Every listed row
// costs a detail call, and a board needing more than MaxDetails fails.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now, wait: srcutil.Sleep} }

// NewWith returns the source with the bounded detail step: stored postings are
// reused (re-read when their refresh turn comes), new
// ones DropListed rejects are never fetched, and at most MaxDetails detail
// calls go out per board per run.
func NewWith(d egress.Doer, o Options) (sources.Source, error) {
	if o.Stored == nil {
		return nil, errors.New("dice: the bounded detail step needs the stored postings (Options.Stored)")
	}
	wait := o.Wait
	if wait == nil {
		wait = srcutil.Sleep
	}
	return &source{d: d, now: time.Now, wait: wait, opt: &o}, nil
}

func (*source) ATS() posting.ATS { return posting.Dice }

// searchResult is search_jobs' structuredContent.
type searchResult struct {
	// Data is nil when the answer has none (missing or null): never an empty
	// search, which answers "data":[].
	Data     []jobRow `json:"data"`
	Metadata *struct {
		Page       *int `json:"page"`
		PageSize   *int `json:"pageSize"`
		Total      *int `json:"total"`
		TotalPages *int `json:"totalPages"`
	} `json:"metadata"`
}

// jobRow is one search row as served. Null fields decode as zero values.
type jobRow struct {
	GUID        string `json:"guid"`
	Title       string `json:"title"`
	PostedDate  string `json:"postedDate"`
	Modified    string `json:"modifiedDate"`
	JobLocation *struct {
		DisplayName string `json:"displayName"`
	} `json:"jobLocation"`
	DetailsPageURL   string   `json:"detailsPageUrl"`
	Salary           string   `json:"salary"`
	CompanyName      string   `json:"companyName"`
	EmploymentType   string   `json:"employmentType"`
	WorkplaceTypes   []string `json:"workplaceTypes"`
	IsRemote         bool     `json:"isRemote"`
	WillingToSponsor bool     `json:"willingToSponsor"`
}

// detailResult is get_job_details' structuredContent.
type detailResult struct {
	Description *string `json:"description"`
	Skills      []struct {
		Name string `json:"name"`
	} `json:"skills"`
}

// listed is a checked search row: what the posting is built from.
type listed struct {
	guid     string
	key      posting.Key
	title    string
	company  string
	location string
	remote   bool
	posted   time.Time
	modified time.Time
	pay      string
	role     string
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckBoard(b, posting.Dice); err != nil {
		return nil, err
	}
	if b.Token != Token {
		return nil, fmt.Errorf("dice: board token %q is not %q", b.Token, Token)
	}
	if b.Host != "" || b.Region != "" || b.MaxPostings != 0 || b.URL != "" || b.LinkPattern != "" {
		return nil, fmt.Errorf("dice %q: host, region, max_postings, url and link_pattern do not apply", b.Token)
	}
	terms := b.SearchTerms()
	if len(terms) == 0 {
		return nil, fmt.Errorf("dice %q: the board needs at least one search", b.Token)
	}
	q, err := parseFilters(b.Filters)
	if err != nil {
		return nil, fmt.Errorf("dice %q: %w", b.Token, err)
	}
	c, err := mcphttp.New(s.d, Endpoint)
	if err != nil {
		return nil, fmt.Errorf("dice %q: %w", b.Token, err)
	}
	if err := c.Open(ctx); err != nil {
		return nil, fmt.Errorf("dice %q: %w", b.Token, err)
	}
	pace := &srcutil.Pacer{Every: paceEvery, Budget: paceBudget, Sleep: s.wait}
	st := Stats{Windowed: q.windowed}
	bad := srcutil.NewRows(ctx, b, 0)
	var rows []listed
	have := map[string]bool{}
	for _, term := range terms {
		got, err := s.search(ctx, c, pace, b, term, q, bad, &st)
		if err != nil {
			return nil, fmt.Errorf("dice %q: search %q: %w", b.Token, term, err)
		}
		for _, r := range got {
			if !have[r.guid] {
				have[r.guid] = true
				rows = append(rows, r)
			}
		}
		if len(rows) > MaxPostings {
			return nil, fmt.Errorf("dice %q: %w: the board's searches together list more than %d postings; narrow them with location, workplace_type or posted_within_days",
				b.Token, srcutil.ErrTooManyPostings, MaxPostings)
		}
	}
	st.Listed = len(rows)
	if s.opt != nil && s.opt.Report != nil {
		defer func() { s.opt.Report(b, st) }()
	}
	out, err := s.details(ctx, c, pace, b, rows, &st)
	if err != nil {
		return nil, fmt.Errorf("dice %q: %w", b.Token, err)
	}
	// Reported last, so a board that failed on a detail reports no bad rows.
	if err := bad.Done(); err != nil {
		return nil, fmt.Errorf("dice %q: %w", b.Token, err)
	}
	return out, nil
}

// shortfall is how many of a term's counted jobs may be missing when its walk
// reaches totalPages: a hundredth, at least one. A posting closing mid-walk
// shifts a later row up onto a page already read.
func shortfall(count int) int { return max(1, count/100) }

// search reads one term's pages, 1 to totalPages and never past it. A later
// page's total and totalPages may grow (a listing that changes while it is
// read) but never lower what page 1 promised: a later page answering a smaller
// total, or none, must not end the walk with the rest unread (that would be a
// truncated listing returned as whole, and its absent postings aged);
// a first page counting more than MaxPostings fails the term before any
// further page. Rows shifted onto a page already read are dropped as repeats,
// and a page that adds nothing while the total is unmet is ErrShape. A row
// that cannot become a posting goes to bad (srcutil.Rows).
func (s *source) search(ctx context.Context, c *mcphttp.Client, pace *srcutil.Pacer, b sources.Board, term string, q query, bad *srcutil.Rows, st *Stats) ([]listed, error) {
	var out []listed
	have := map[string]bool{}
	read := 0
	want, wantPages := 0, 0 // what page 1 promised
	for page := 1; page <= maxPages; page++ {
		if err := pace.Wait(ctx); err != nil {
			return nil, err
		}
		st.Requests++
		var res searchResult
		if err := c.Call(ctx, "search_jobs", q.args(term, page), &res); err != nil {
			return nil, toolErr(err)
		}
		m := res.Metadata
		if m == nil || m.Page == nil || m.PageSize == nil || m.Total == nil || m.TotalPages == nil || res.Data == nil {
			return nil, fmt.Errorf("%w: page %d has no data list or no page, pageSize, total and totalPages", ErrShape, page)
		}
		total, pages := *m.Total, *m.TotalPages
		if *m.Page != page || *m.PageSize != pageSize || total < 0 || pages < 0 || len(res.Data) > pageSize {
			return nil, fmt.Errorf("%w: page %d answered as page %d of %d with %d of %d rows a page, total %d",
				ErrShape, page, *m.Page, pages, len(res.Data), *m.PageSize, total)
		}
		if page == 1 {
			st.Matched += total
			want, wantPages = total, pages
		}
		if total > MaxPostings {
			return nil, fmt.Errorf("%w: search matches %d postings, over the %d limit; narrow it with location, workplace_type or posted_within_days",
				srcutil.ErrTooManyPostings, total, MaxPostings)
		}
		bad.Add(len(res.Data))
		added := 0
		for _, j := range res.Data {
			guid := strings.ToLower(strings.TrimSpace(j.GUID))
			if guidRE.MatchString(guid) {
				if have[guid] {
					continue // paging shifted under a changing listing
				}
				have[guid] = true
			}
			read++
			added++
			r, err := j.check(b)
			if err != nil {
				if ferr := bad.Bad(fmt.Errorf("job %q: %w", mcphttp.Clean(j.GUID), err)); ferr != nil {
					return nil, ferr
				}
				continue
			}
			out = append(out, r)
		}
		if page == 1 {
			// A page cannot list more jobs than the search matches.
			if err := srcutil.TotalCoversRows(total, read); err != nil {
				return nil, fmt.Errorf("%w: %w", ErrShape, err)
			}
		}
		target, lastPage := max(want, total), max(wantPages, pages)
		if read >= target {
			return out, nil
		}
		if added == 0 {
			return nil, fmt.Errorf("%w: page %d added nothing with %d of %d read", ErrShape, page, read, target)
		}
		if page >= lastPage {
			if target-read > shortfall(target) {
				return nil, fmt.Errorf("%w: the search counts %d postings and its %d pages listed %d", errIncomplete, target, lastPage, read)
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("%w: listing did not end within %d pages", ErrShape, maxPages)
}

// toolErr turns a tool error that reports Dice's upstream 429 into a
// *srcutil.RateLimitedError, so the run reads the board as rate limited, not
// broken. Any other error is returned as it is.
func toolErr(err error) error {
	var te *mcphttp.ToolError
	if errors.As(err, &te) && rateLimitRE.MatchString(te.Text) {
		return &srcutil.RateLimitedError{
			Host: host, Method: http.MethodPost, Path: "/mcp", Attempts: 1,
			Why:    "Dice reported its upstream search rate limited (HTTP 429)",
			Status: &egress.StatusError{Code: http.StatusTooManyRequests, Method: http.MethodPost, Host: host},
		}
	}
	return err
}

// line is untrusted board text as one display line: srcutil.Title, then any
// control character it let through removed.
func line(s string) string { return htmltext.Plain(srcutil.Title(s), srcutil.MaxTitle) }

// check turns a search row into a listed row, or says why it cannot be one.
func (j jobRow) check(b sources.Board) (listed, error) {
	guid := strings.ToLower(strings.TrimSpace(j.GUID))
	if !guidRE.MatchString(guid) {
		return listed{}, fmt.Errorf("%w: guid is not a UUID", ErrShape)
	}
	key, err := posting.DiceKey(b.Token, guid)
	if err != nil {
		return listed{}, err
	}
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return listed{}, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := line(j.Title)
	if title == "" {
		return listed{}, fmt.Errorf("%w: empty title", ErrShape)
	}
	company := line(j.CompanyName)
	if company == "" {
		return listed{}, fmt.Errorf("%w: empty companyName", ErrShape)
	}
	posted, err := srcutil.ParseTime("postedDate", strings.TrimSpace(j.PostedDate))
	if err != nil {
		return listed{}, fmt.Errorf("%w: %v", ErrShape, err)
	}
	modified, err := srcutil.ParseTime("modifiedDate", strings.TrimSpace(j.Modified))
	if err != nil {
		return listed{}, fmt.Errorf("%w: %v", ErrShape, err)
	}
	if err := checkPage(j.DetailsPageURL, guid); err != nil {
		return listed{}, err
	}
	loc := ""
	if j.JobLocation != nil {
		loc = htmltext.Truncate(line(j.JobLocation.DisplayName), maxLocation)
	}
	remote := j.IsRemote || slices.ContainsFunc(j.WorkplaceTypes, func(w string) bool { return strings.EqualFold(strings.TrimSpace(w), "remote") })
	return listed{
		guid: guid, key: key, title: title, company: company, location: loc, remote: remote,
		posted: utc(posted), modified: utc(modified),
		pay: payLine(j.Salary), role: roleLine(j.EmploymentType, j.WorkplaceTypes, remote, j.WillingToSponsor),
	}, nil
}

func utc(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC()
}

// checkPage accepts an empty detailsPageUrl or one that is the row's own page,
// https://www.dice.com/job-detail/{guid} or /direct-apply/{guid}, whatever its
// query. The posting's URL is built from the guid, without the query (whose
// utm_source echoes the client's name), so this only refuses a row that points
// somewhere else.
func checkPage(raw, guid string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if err := srcutil.CheckURL("detailsPageUrl", raw); err != nil {
		return err
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "www.dice.com") || u.Port() != "" ||
		(!strings.EqualFold(u.Path, "/job-detail/"+guid) && !strings.EqualFold(u.Path, "/direct-apply/"+guid)) {
		return fmt.Errorf("%w: detailsPageUrl is not the job's www.dice.com page", ErrShape)
	}
	return nil
}

// posting is what the listed row says before its detail is read: the fields
// DropListed may judge.
func (r listed) posting() posting.Posting {
	return posting.Posting{ATS: posting.Dice, Company: r.company, Title: r.title, Location: r.location, Remote: r.remote}
}

// details turns listed rows into postings, as SmartRecruiters' detail step
// does. Without Options every row is fetched, and a board needing more than
// MaxDetails fails. With Options the rows are planned by srcutil.PlanDetails: a
// stored row is reused unless its turn has come; new rows DropListed rejects
// are skipped; new rows are fetched first, then the scheduled ones, up to MaxDetails, and the new rows
// past it are deferred. Results keep listing order.
func (s *source) details(ctx context.Context, c *mcphttp.Client, pace *srcutil.Pacer, b sources.Board, rows []listed, st *Stats) ([]posting.Posting, error) {
	seen := s.now()
	stored := map[posting.Key]posting.Posting{}
	if s.opt != nil {
		// Looked up by the key a row builds, so another board's posting can
		// never match one of this board's rows.
		for _, p := range s.opt.Stored(b) {
			stored[p.Key] = p
		}
	}
	plan := make([]srcutil.DetailRow, len(rows))
	for i, r := range rows {
		_, ok := stored[r.key]
		plan[i] = srcutil.DetailRow{ID: r.guid, Stored: ok, Unusable: ok && s.opt != nil && srcutil.Marked(s.opt.Unusable, r.key)}
		if !ok && s.opt != nil && s.opt.DropListed != nil {
			plan[i].Drop = s.opt.DropListed(r.posting())
		}
	}
	pl := srcutil.PlanDetails(plan, srcutil.Day(seen), MaxDetails)
	if s.opt == nil && pl.Deferred > 0 {
		// Without stored postings a deferred row cannot be told from a stored
		// one, and leaving out a stored one would age it as absent.
		return nil, fmt.Errorf("%w: the board lists %d postings and each costs a detail call, over the %d limit; narrow it with location, workplace_type or posted_within_days",
			srcutil.ErrTooManyPostings, pl.New+pl.Deferred, MaxDetails)
	}
	st.Deferred, st.Prefiltered = pl.Deferred, pl.Prefiltered
	res, err := srcutil.DetailStep{
		Board: b, Rows: len(rows), Plan: pl, CanDefer: s.opt != nil,
		Fetch: func(ctx context.Context, i int) (*posting.Posting, error) {
			p, err := s.detail(ctx, c, pace, rows[i], seen)
			return &p, err
		},
		Reuse: func(i int) posting.Posting {
			p := stored[rows[i].key]
			p.SeenAt = seen
			return p
		},
		Gone: errDetailGone,
		GoneErr: func(gone, fetch int) error {
			return fmt.Errorf(`%d of %d job details are gone ("Job not found"); the detail tool is not answering`, gone, fetch)
		},
		FetchErr: func(i int, err error) error { return fmt.Errorf("job %s: %w", rows[i].guid, err) },
	}.Run(ctx)
	st.Gone, st.Reused, st.Fetched, st.Refreshed, st.Unanswered = res.Gone, res.Reused, res.Fetched, res.Refreshed, res.Unanswered
	if err != nil {
		return nil, err
	}
	return srcutil.UniquePostings(res.Slots), nil
}

// detail reads one row's get_job_details and builds its posting.
func (s *source) detail(ctx context.Context, c *mcphttp.Client, pace *srcutil.Pacer, r listed, seen time.Time) (posting.Posting, error) {
	if err := pace.Wait(ctx); err != nil {
		return posting.Posting{}, err
	}
	var d detailResult
	if err := c.Call(ctx, "get_job_details", map[string]any{"job_id": r.guid}, &d); err != nil {
		var te *mcphttp.ToolError
		if errors.As(err, &te) && notFoundRE.MatchString(te.Text) {
			return posting.Posting{}, errDetailGone
		}
		return posting.Posting{}, toolErr(err)
	}
	if d.Description == nil {
		return posting.Posting{}, fmt.Errorf("%w: get_job_details answered no description", ErrShape)
	}
	skills := make([]string, 0, len(d.Skills))
	for _, sk := range d.Skills {
		skills = append(skills, sk.Name)
	}
	page := jobPage + r.guid
	return posting.Posting{
		Key:         r.key,
		ATS:         posting.Dice,
		Company:     r.company,
		Title:       r.title,
		Location:    r.location,
		Remote:      r.remote,
		URL:         page,
		ApplyURL:    page,
		PostedAt:    r.posted,
		UpdatedAt:   r.modified,
		Description: description(r, *d.Description, skills),
		SeenAt:      seen,
	}, nil
}

// description is the detail's HTML as text, then a Skills section, led by the
// row's pay line (when its salary text is a range) and role line.
func description(r listed, body string, skills []string) string {
	lead, room := srcutil.Lead(r.pay, r.role)
	parts := []srcutil.Section{{HTML: body}}
	if s := skillList(skills); s != "" {
		parts = append(parts, srcutil.Section{Heading: "Skills", HTML: html.EscapeString(s)})
	}
	return lead + srcutil.Sections(room, parts...)
}

// skillList is the skills as one sorted line, each once (compared without
// case; of "Alpha" and "alpha" the one that sorts first is kept, whatever
// order Dice served them in), at most maxSkills.
func skillList(names []string) string {
	var all []string
	for i, n := range names {
		if i >= 4*maxSkills {
			break
		}
		if n = line(n); n != "" {
			all = append(all, n)
		}
	}
	slices.SortFunc(all, func(a, b string) int {
		if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	var out []string
	for _, n := range all {
		if len(out) == maxSkills {
			break
		}
		if len(out) == 0 || !strings.EqualFold(out[len(out)-1], n) {
			out = append(out, n)
		}
	}
	return strings.Join(out, ", ")
}

// roleLine is the row's role facts as one srcutil.RoleLine: its employment
// types (employmentType split on ","), then its workplace types, each sorted,
// then "sponsorship offered". Sorted so the same row renders the same line
// whatever order Dice lists them in.
func roleLine(employment string, workplace []string, remote, sponsor bool) string {
	var kinds []string
	for i, p := range strings.Split(employment, ",") {
		if i >= maxRoleParts {
			break
		}
		if p = strings.ToLower(srcutil.EmploymentType(line(p))); p != "" && !slices.Contains(kinds, p) {
			kinds = append(kinds, p)
		}
	}
	slices.Sort(kinds)
	var places []string
	for i, w := range workplace {
		if i >= maxRoleParts {
			break
		}
		if w = strings.ToLower(line(w)); w != "" && !slices.Contains(places, w) {
			places = append(places, w)
		}
	}
	if remote && !slices.Contains(places, "remote") {
		places = append(places, "remote")
	}
	slices.Sort(places)
	flags := append(kinds, places...)
	if sponsor {
		flags = append(flags, "sponsorship offered")
	}
	return srcutil.RoleLine(flags...)
}

// salaryRE is the one salary text read as pay: a range of two amounts, each
// optionally with "$" or a three-letter code before it and "k" after, with an
// optional code and period after the range ("$120,000 - $150,000", "USD
// 105,050.00 - 161,800.00 per year", "$55-$65/hr"). Anything else ("Depends on
// Experience", a single amount, prose) is not read.
var salaryRE = regexp.MustCompile(`^(?:([A-Z]{3}) ?)?(\$)? ?(\d{1,3}(?:,\d{3})*(?:\.\d+)?|\d+(?:\.\d+)?) ?([kK])? ?(?:-|–|—|(?i:to)) ?(?:([A-Z]{3}) ?)?(\$)? ?(\d{1,3}(?:,\d{3})*(?:\.\d+)?|\d+(?:\.\d+)?) ?([kK])?(?: ([A-Z]{3}))?(?: ?(?i:(?:per|/|an?) ?(hour|hr|year|yr|annum|month|mo|week|wk|day)|(annually|yearly|hourly|monthly|weekly|daily)))?\.?$`)

// payLine is the row's free-text salary as a srcutil.PayLine, or "" when the
// text is not a plain range in a known currency.
func payLine(salary string) string {
	s := strings.Join(strings.Fields(salary), " ")
	if s == "" || len(s) > 200 {
		return ""
	}
	m := salaryRE.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	cur := ""
	for _, code := range []string{m[1], m[5], m[9]} {
		if code == "" {
			continue
		}
		if cur != "" && cur != code {
			return ""
		}
		cur = code
	}
	if cur == "" {
		if m[2] == "" && m[6] == "" {
			return ""
		}
		cur = "USD"
	}
	amount := func(num, k string) *float64 {
		v, err := strconv.ParseFloat(strings.ReplaceAll(num, ",", ""), 64)
		if err != nil {
			return nil
		}
		if k != "" {
			v *= 1000
		}
		return &v
	}
	lo, hi := amount(m[3], m[4]), amount(m[7], m[8])
	if lo == nil || hi == nil {
		return ""
	}
	period := strings.ToLower(m[10] + m[11])
	switch period {
	case "hr", "hourly":
		period = "hour"
	case "yr", "annum", "annually", "yearly":
		period = "year"
	case "mo", "monthly":
		period = "month"
	case "wk", "weekly":
		period = "week"
	case "daily":
		period = "day"
	}
	return srcutil.PayLine(cur, lo, hi, period)
}

// query is a board's filters as search_jobs arguments.
type query struct {
	filters  map[string]any
	windowed bool
}

// args is the search_jobs arguments for one page of term.
func (q query) args(term string, page int) map[string]any {
	a := map[string]any{"keyword": term, "jobs_per_page": pageSize, "page_number": page, "sort": "datePosted"}
	for k, v := range q.filters {
		a[k] = v
	}
	return a
}

var (
	postedWithin = map[string]string{"1": "ONE", "3": "THREE", "7": "SEVEN"}
	// The set filters' values, in the order they are sent.
	workplaceValues  = []struct{ in, out string }{{"remote", "Remote"}, {"on-site", "On-Site"}, {"hybrid", "Hybrid"}}
	employmentValues = []struct{ in, out string }{{"fulltime", "FULLTIME"}, {"contracts", "CONTRACTS"}, {"parttime", "PARTTIME"}, {"third_party", "THIRD_PARTY"}, {"internship", "INTERNSHIP"}}
	employerValues   = []struct{ in, out string }{{"direct_hire", "Direct Hire"}, {"recruiter", "Recruiter"}, {"other", "Other"}}
)

// parseFilters checks a board's filters strictly, before any request: a value
// Dice would refuse fails the board here, and one it would ignore (a location
// of "Remote", a radius with no location) is refused rather than silently
// widening the search.
// CheckFilters is the filter rules List applies, for config validation: the
// keys are FilterKeys and the values are checked as List checks them before
// any request.
func CheckFilters(f map[string][]string) error {
	_, err := parseFilters(f)
	return err
}

func parseFilters(f map[string][]string) (query, error) {
	q := query{filters: map[string]any{}}
	for k, vs := range f {
		if !slices.Contains(FilterKeys, k) {
			return query{}, fmt.Errorf("filter %q is not one of %s", k, strings.Join(FilterKeys, ", "))
		}
		if len(vs) == 0 {
			return query{}, fmt.Errorf("filter %q has no value", k)
		}
		if len(vs) > sources.MaxFilterValues {
			return query{}, fmt.Errorf("filter %q has %d values, more than %d", k, len(vs), sources.MaxFilterValues)
		}
	}
	one := func(k string) (string, bool, error) {
		vs, ok := f[k]
		if !ok {
			return "", false, nil
		}
		if len(vs) != 1 || strings.TrimSpace(vs[0]) == "" {
			return "", false, fmt.Errorf("filter %q takes exactly one value", k)
		}
		return strings.TrimSpace(vs[0]), true, nil
	}
	set := func(k string, values []struct{ in, out string }) error {
		vs, ok := f[k]
		if !ok {
			return nil
		}
		want := map[string]bool{}
		for _, v := range vs {
			v = strings.ToLower(strings.TrimSpace(v))
			i := slices.IndexFunc(values, func(x struct{ in, out string }) bool { return x.in == v })
			if i < 0 {
				names := make([]string, len(values))
				for j, x := range values {
					names[j] = x.in
				}
				return fmt.Errorf("filter %q value %q is not one of %s", k, mcphttp.Clean(v), strings.Join(names, ", "))
			}
			want[values[i].out] = true
		}
		var out []string
		for _, x := range values {
			if want[x.out] {
				out = append(out, x.out)
			}
		}
		q.filters[apiName[k]] = out
		return nil
	}

	loc, hasLoc, err := one("location")
	if err != nil {
		return query{}, err
	}
	if hasLoc {
		loc = strings.Join(strings.Fields(loc), " ")
		if len(loc) > maxLocation || !utf8.ValidString(loc) || strings.ContainsFunc(loc, unicode.IsControl) {
			return query{}, fmt.Errorf("filter \"location\" must be one place name of at most %d bytes", maxLocation)
		}
		if strings.EqualFold(loc, "remote") {
			return query{}, errors.New(`filter "location" cannot be "Remote" (Dice ignores it); use workplace_type remote`)
		}
		q.filters["location"] = loc
	}
	if r, ok, err := one("radius_miles"); err != nil {
		return query{}, err
	} else if ok {
		n, err := strconv.Atoi(r)
		if err != nil || n < 1 || n > 500 {
			return query{}, fmt.Errorf("filter \"radius_miles\" %q must be a whole number of miles from 1 to 500", mcphttp.Clean(r))
		}
		if !hasLoc {
			return query{}, errors.New(`filter "radius_miles" needs a location filter`)
		}
		q.filters["radius"] = n
		q.filters["radius_unit"] = "mi"
	}
	if d, ok, err := one("posted_within_days"); err != nil {
		return query{}, err
	} else if ok {
		v, known := postedWithin[d]
		if !known {
			return query{}, fmt.Errorf("filter \"posted_within_days\" %q must be 1, 3 or 7", mcphttp.Clean(d))
		}
		q.filters["posted_date"] = v
		q.windowed = true
	}
	if err := set("workplace_type", workplaceValues); err != nil {
		return query{}, err
	}
	if err := set("employment_type", employmentValues); err != nil {
		return query{}, err
	}
	if err := set("employer_type", employerValues); err != nil {
		return query{}, err
	}
	if v, ok, err := one("sponsorship"); err != nil {
		return query{}, err
	} else if ok {
		if !strings.EqualFold(v, "true") {
			return query{}, fmt.Errorf("filter \"sponsorship\" %q must be true", mcphttp.Clean(v))
		}
		q.filters["willing_to_sponsor"] = true
	}
	return q, nil
}

// apiName is the search_jobs argument a set filter is sent as.
var apiName = map[string]string{
	"workplace_type":  "workplace_types",
	"employment_type": "employment_types",
	"employer_type":   "employer_types",
}
