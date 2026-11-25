// Package ziprecruiter lists postings from ZipRecruiter's public job-search
// MCP server, https://api.ziprecruiter.com/mcp, through its search_jobs tool.
// ZipRecruiter is a job board, not an employer's ATS: one search lists many
// employers, so each posting's company is its row's own, and a row that names
// none is a bad row rather than one filed under the board's name.
//
// Live facts, checked 2026-11-24 (initialize, tools/list and search_jobs at
// offsets 0, 5, 10, 15, 20, 25 and 30):
//
//   - The server speaks the Streamable HTTP transport (internal/sources/mcphttp)
//     and answers JSON with no key, session or account. It serves the United
//     States and Canada only: country_admin_code GB answered status
//     UNSUPPORTED_COUNTRY.
//   - It has three tools: search_jobs, connect_account and connection_status.
//     Vani calls only search_jobs, never connect_account, and never sends
//     conversation_id, skills or unsupported_filters.
//   - search_jobs serves 5 rows a call. Offsets 0 to 25 each served new rows;
//     offset 30 served the same rows as offset 25, so one search reaches at
//     most 30 rows. The reach can be smaller: on 2026-11-25 "machine learning
//     engineer" in Austin, TX, posted within 7 days (meta.total 82), served
//     new rows at offsets 0 and 5 and repeated offset 5's rows at every offset
//     from 10 to 25, so that search reached 10 rows. A page that adds fewer
//     than 5 new rows ends the term, which covers both. meta.total is an estimate that drifts between calls (920,
//     921, 919) and some filters raised it (845 to 1,054 with FULL_TIME,
//     although every row served did match the filter), so it is read for
//     Stats.Matched only, never to decide whether the listing is complete.
//   - A row carries no job description. Its job_redirect_url carries a
//     match_token that differs on every call; the stable job page is
//     https://www.ziprecruiter.com/jobs//j?lvk={listing_key}, which renders
//     the posting.
//   - Filters that changed the rows served: location (with
//     max_posted_minutes_ago 4320, total fell to 29), location_types,
//     employment_types and seniority_classes (rows matched each). salary_min
//     and salary_max did not change the total, so Vani does not offer them.
//     A search with no location is a United States-wide search, with the
//     warning "No location was provided; defaulting to a United States-wide
//     search."; a nonsense role answered NO_RESULTS with total 0; an enum
//     value the schema does not list is a tools/call isError.
//
// Terms footing: ZipRecruiter's partner page offers the server to "ChatGPT /
// Claude, or other MCP clients"; Vani is such a client, reading its owner's
// own searches at a person's pace (at least a second between calls). Vani
// keeps the stable job page as the posting URL (Posting.URL), leaves ApplyURL
// empty, never fetches either page, and never stores the partner's
// per-call redirect link.
//
// The listing is windowed (Stats.Windowed is always true): 30 rows a search
// is a window onto the board, not its whole word, so a posting that leaves the
// window has not closed. The run reconciles such a board by its window rule,
// holding a missing posting for a while before closing it as expected,
// rather than by the ordinary two-miss rule.
//
// With no description, a posting's Description holds only the lines Vani
// synthesizes from the row's structured fields (srcutil.Lead): a pay line
// (min_annual and max_annual, per year; currency USD when the board's country
// filter is US, CAD when it is CA, and no pay line without a country filter,
// since the row names no currency) and a role line (the job_type parts, such
// as "full-time" and "on-site", sorted, and "remote" when is_remote). Its
// srcutil.SourceText is therefore empty: the pipeline has no board text to
// compare, and a change of pay or role is a re-render, not a re-triage.
// Benefits are left out, since a "Benefits:" line is not a lead line and would
// stand as the posting's whole board text. days_ago becomes PostedAt, the UTC
// day of the listing minus days_ago; application_status, the redirect link and
// days_ago itself are kept out of every field, so the same row lists the same
// posting on every run.
package ziprecruiter

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/mcphttp"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	// Token is the one board token: the server is one site.
	Token = "ziprecruiter"
	// Endpoint is the MCP server.
	Endpoint = "https://api.ziprecruiter.com/mcp"
	// JobPage is the stable job page a listing_key is appended to,
	// query-escaped.
	JobPage = "https://www.ziprecruiter.com/jobs//j?lvk="
	// PageSize is the rows search_jobs serves a call.
	PageSize = 5
	// MaxOffset is the last offset that served new rows (2026-11-24); one
	// search reads offsets 0, 5, ..., MaxOffset, at most 6 calls.
	MaxOffset = 25
	// MaxPostings bounds one board, the union of its terms: a full window
	// for every term a board may have. A sanity bound; paging cannot pass it.
	MaxPostings = (MaxOffset + PageSize) * sources.MaxSearches

	tool = "search_jobs"
	// paceEvery is the gap between calls: a person's pace, not a crawler's.
	paceEvery = time.Second
	// paceBudget bounds a board's waits: MaxSearches terms of 6 calls each
	// at paceEvery is a minute.
	paceBudget = 2 * time.Minute
	// maxDaysAgo is the oldest days_ago read as a posting date.
	maxDaysAgo = 365
	// maxListingKey bounds a listing_key before it is decoded; live keys are
	// 22 characters.
	maxListingKey = 100
	// maxText bounds a row's company and location, as srcutil.MaxTitle does
	// its title.
	maxText = srcutil.MaxTitle
	// maxTotal bounds meta.total as read into Stats.Matched.
	maxTotal = 1e9
)

// The board filters, by key. A value is matched case-insensitively.
const (
	// FilterLocation is a city, state, zip code or address, sent as
	// location. One value.
	FilterLocation = "location"
	// FilterCountry is US or CA, sent as country_admin_code. One value.
	FilterCountry = "country"
	// FilterRadius is a whole number of miles, 1 to MaxRadiusMiles, sent as
	// radius_miles. One value; it needs FilterLocation.
	FilterRadius = "radius_miles"
	// FilterPostedWithin is a whole number of days, 1 to MaxPostedDays, sent
	// as max_posted_minutes_ago (days × 1440). One value.
	FilterPostedWithin = "posted_within_days"
	// FilterLocationType is physical, remote or hybrid, sent upper-case in
	// location_types. One or more.
	FilterLocationType = "location_type"
	// FilterEmploymentType is full_time, part_time, contract,
	// contract_to_hire, temporary, as_needed, other or internship ('-' is
	// read as '_'), sent upper-case in employment_types. One or more.
	FilterEmploymentType = "employment_type"
	// FilterSeniority is no_experience, junior, mid or senior, sent
	// upper-case in seniority_classes. One or more.
	FilterSeniority = "seniority"

	// MaxRadiusMiles bounds FilterRadius.
	MaxRadiusMiles = 500
	// MaxPostedDays bounds FilterPostedWithin.
	MaxPostedDays = 30
)

// FilterKeys is every filter a ZipRecruiter board takes, for
// sources.FilterKeys.
var FilterKeys = []string{FilterLocation, FilterCountry, FilterRadius, FilterPostedWithin,
	FilterLocationType, FilterEmploymentType, FilterSeniority}

// The enum values search_jobs takes, from its inputSchema (2026-11-24), less
// the *_UNSPECIFIED ones.
var (
	locationTypes   = []string{"PHYSICAL", "REMOTE", "HYBRID"}
	employmentTypes = []string{"FULL_TIME", "PART_TIME", "CONTRACT", "CONTRACT_TO_HIRE", "TEMPORARY", "AS_NEEDED", "OTHER", "INTERNSHIP"}
	seniorities     = []string{"NO_EXPERIENCE", "JUNIOR", "MID", "SENIOR"}
)

var (
	// ErrShape is a search_jobs result whose layout is not the one this
	// source knows, or a row that cannot become a posting.
	ErrShape = errors.New("ziprecruiter: unexpected search_jobs result")
	// ErrStatus is a search_jobs result whose status is ERROR,
	// UNSUPPORTED_CONSTRAINTS or UNSUPPORTED_COUNTRY.
	ErrStatus = errors.New("ziprecruiter: search_jobs failed")
)

// Stats counts what one List did.
type Stats struct {
	// Listed is the distinct rows the board's searches served, each
	// listing_key once; a bad row is among them.
	Listed int `json:"listed"`
	// Requests is the search_jobs calls, every term's (the handshake's two
	// requests are not counted).
	Requests int `json:"requests"`
	// Matched is the sum over the terms of the first call's meta.total, an
	// estimate the server gives; informational only.
	Matched int `json:"matched,omitempty"`
	// Windowed is always true: a search serves at most 30 rows, so the
	// listing is a window onto the board.
	Windowed bool `json:"windowed,omitempty"`
}

// Options configure NewWith.
type Options struct {
	// Report, when set, receives the board's counts once per List that read
	// every search, whether the board then succeeded or failed on its rows.
	Report func(sources.Board, Stats)
	// Wait spaces a board's calls (srcutil.Pacer.Sleep); nil is
	// srcutil.Sleep. A field only so tests need not wait in real time.
	Wait func(ctx context.Context, d time.Duration) error
}

type source struct {
	d      egress.Doer
	now    func() time.Time
	wait   func(ctx context.Context, d time.Duration) error
	report func(sources.Board, Stats)
}

// New returns the ZipRecruiter source, sending requests through d.
func New(d egress.Doer) sources.Source {
	return &source{d: d, now: time.Now, wait: srcutil.Sleep}
}

// NewWith returns the source with o's report and wait.
func NewWith(d egress.Doer, o Options) (sources.Source, error) {
	if d == nil {
		return nil, errors.New("ziprecruiter: no http doer configured")
	}
	wait := o.Wait
	if wait == nil {
		wait = srcutil.Sleep
	}
	return &source{d: d, now: time.Now, wait: wait, report: o.Report}, nil
}

func (*source) ATS() posting.ATS { return posting.ZipRecruiter }

// reply is search_jobs' structuredContent, the fields Vani reads.
type reply struct {
	Status       string `json:"status"`
	ErrorMessage string `json:"errorMessage"`
	Results      []row  `json:"results"`
	Meta         *struct {
		Count *float64 `json:"count"`
		Total *float64 `json:"total"`
	} `json:"meta"`
}

// row is one result. company_logo, job_redirect_url, benefits,
// application_status and is_external are not read.
type row struct {
	ListingKey string `json:"listing_key"`
	Title      string `json:"title"`
	Company    string `json:"company"`
	Location   string `json:"location"`
	IsRemote   bool   `json:"is_remote"`
	Salary     *struct {
		MinAnnual *float64 `json:"min_annual"`
		MaxAnnual *float64 `json:"max_annual"`
	} `json:"salary"`
	JobType *string  `json:"job_type"`
	DaysAgo *float64 `json:"days_ago"`
}

// query is a board's filters as search_jobs arguments.
type query struct {
	location        string
	country         string
	radius          int
	postedDays      int
	locationTypes   []string
	employmentTypes []string
	seniorities     []string
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckBoard(b, posting.ZipRecruiter); err != nil {
		return nil, err
	}
	if b.Token != Token {
		return nil, fmt.Errorf("ziprecruiter: board token %q is not %q", b.Token, Token)
	}
	if b.Host != "" || b.Region != "" || b.MaxPostings != 0 || b.URL != "" || b.LinkPattern != "" {
		return nil, fmt.Errorf("ziprecruiter %q: host, region, max_postings, url and link_pattern do not apply", b.Token)
	}
	terms := b.SearchTerms()
	if len(terms) == 0 {
		return nil, fmt.Errorf("ziprecruiter %q: the board needs at least one search", b.Token)
	}
	if len(terms) > sources.MaxSearches {
		return nil, fmt.Errorf("ziprecruiter %q: %d searches, over the %d a board may have", b.Token, len(terms), sources.MaxSearches)
	}
	for _, t := range terms {
		if len(t) > sources.MaxSearchLen {
			return nil, fmt.Errorf("ziprecruiter %q: a search is over %d bytes", b.Token, sources.MaxSearchLen)
		}
	}
	q, err := parseFilters(b.Filters)
	if err != nil {
		return nil, fmt.Errorf("ziprecruiter %q: %w", b.Token, err)
	}
	if s.d == nil {
		return nil, errors.New("ziprecruiter: no http doer configured")
	}
	c, err := mcphttp.New(s.d, Endpoint)
	if err != nil {
		return nil, fmt.Errorf("ziprecruiter %q: %w", b.Token, err)
	}
	pace := &srcutil.Pacer{Every: paceEvery, Budget: paceBudget, Sleep: s.wait}
	if err := pace.Wait(ctx); err != nil {
		return nil, err
	}
	if err := c.Open(ctx); err != nil {
		return nil, fmt.Errorf("ziprecruiter %q: %w", b.Token, err)
	}

	st := Stats{Windowed: true}
	var listed []row
	have := map[string]bool{}
	for _, term := range terms {
		got, total, err := search(ctx, c, pace, q, term, &st.Requests)
		if err != nil {
			return nil, fmt.Errorf("ziprecruiter %q: search %q: %w", b.Token, term, err)
		}
		st.Matched += total
		for _, r := range got {
			if k := strings.TrimSpace(r.ListingKey); k != "" {
				if have[k] {
					continue // another term served it
				}
				have[k] = true
			}
			listed = append(listed, r)
		}
		if len(listed) > MaxPostings {
			return nil, fmt.Errorf("ziprecruiter %q: %w: the board's searches together list more than %d postings",
				b.Token, srcutil.ErrTooManyPostings, MaxPostings)
		}
	}
	st.Listed = len(listed)
	if s.report != nil {
		defer func() { s.report(b, st) }()
	}

	seen := s.now()
	currency := currencyOf(q.country)
	rows := srcutil.NewRows(ctx, b, len(listed))
	keys := map[posting.Key]bool{}
	out := make([]posting.Posting, 0, len(listed))
	for _, r := range listed {
		p, err := r.posting(b, currency, seen)
		if err != nil {
			if ferr := rows.Bad(err); ferr != nil {
				return nil, fmt.Errorf("ziprecruiter %q: %w", b.Token, ferr)
			}
			continue
		}
		if keys[p.Key] {
			continue
		}
		keys[p.Key] = true
		out = append(out, p)
	}
	if err := rows.Done(); err != nil {
		return nil, fmt.Errorf("ziprecruiter %q: %w", b.Token, err)
	}
	return out, nil
}

// search reads one term's window: offsets 0, 5, ..., MaxOffset, ending early
// at a page with no rows, a page that adds fewer than PageSize new rows (a
// short page, or one that repeats rows already read, which is how an offset
// past the window answers), or NO_RESULTS. total is the first call's
// meta.total. requests counts the calls made.
func search(ctx context.Context, c *mcphttp.Client, pace *srcutil.Pacer, q query, term string, requests *int) ([]row, int, error) {
	var out []row
	seen := map[string]bool{}
	total := 0
	for offset := 0; offset <= MaxOffset; offset += PageSize {
		if err := pace.Wait(ctx); err != nil {
			return nil, 0, err
		}
		*requests++
		var rep reply
		if err := c.Call(ctx, tool, q.args(term, offset), &rep); err != nil {
			return nil, 0, fmt.Errorf("offset %d: %w", offset, err)
		}
		n, end, err := rep.check(offset)
		if err != nil {
			return nil, 0, fmt.Errorf("offset %d: %w", offset, err)
		}
		if offset == 0 {
			total = n
		}
		if end {
			break
		}
		added := 0
		for _, r := range rep.Results {
			k := strings.TrimSpace(r.ListingKey)
			if k != "" && seen[k] {
				continue // paging shifted, or an offset past the window
			}
			if k != "" {
				seen[k] = true
			}
			out = append(out, r)
			added++
		}
		if added < PageSize {
			break
		}
	}
	return out, total, nil
}

// check validates one result and returns its meta.total. end is a result
// that ends the term with no rows to read: NO_RESULTS, or an empty page.
func (r reply) check(offset int) (total int, end bool, err error) {
	switch r.Status {
	case "OK", "NO_RESULTS":
	case "ERROR", "UNSUPPORTED_CONSTRAINTS", "UNSUPPORTED_COUNTRY":
		if msg := mcphttp.Clean(r.ErrorMessage); msg != "" {
			return 0, false, fmt.Errorf("%w: status %s: %s", ErrStatus, r.Status, msg)
		}
		return 0, false, fmt.Errorf("%w: status %s", ErrStatus, r.Status)
	default:
		return 0, false, fmt.Errorf("%w: status %q", ErrShape, mcphttp.Clean(r.Status))
	}
	if r.Meta == nil || r.Meta.Count == nil || r.Meta.Total == nil {
		return 0, false, fmt.Errorf("%w: no meta count and total", ErrShape)
	}
	count, ok := whole(*r.Meta.Count, PageSize)
	if !ok {
		return 0, false, fmt.Errorf("%w: meta.count %v is not 0 to %d", ErrShape, *r.Meta.Count, PageSize)
	}
	total, ok = whole(*r.Meta.Total, maxTotal)
	if !ok {
		return 0, false, fmt.Errorf("%w: meta.total %v is not a count", ErrShape, *r.Meta.Total)
	}
	if r.Status == "NO_RESULTS" {
		if len(r.Results) != 0 || count != 0 || (offset == 0 && total != 0) {
			return 0, false, fmt.Errorf("%w: NO_RESULTS with %d rows, count %d and total %d", ErrShape, len(r.Results), count, total)
		}
		return total, true, nil
	}
	if r.Results == nil {
		return 0, false, fmt.Errorf("%w: status OK with no results list", ErrShape)
	}
	if len(r.Results) != count {
		return 0, false, fmt.Errorf("%w: %d rows but meta.count %d", ErrShape, len(r.Results), count)
	}
	if offset == 0 && count == 0 && total != 0 {
		// An empty first page of a search that matches: not an empty board.
		return 0, false, fmt.Errorf("%w: no rows although meta.total is %d", ErrShape, total)
	}
	return total, count == 0, nil
}

// whole reads v as a whole number from 0 to most.
func whole(v, most float64) (int, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > most || v != math.Trunc(v) {
		return 0, false
	}
	return int(v), true
}

// args is one call's search_jobs arguments. Only the fields Vani sets are
// sent: never conversation_id, skills or unsupported_filters.
func (q query) args(term string, offset int) map[string]any {
	a := map[string]any{"job_role": term}
	if offset > 0 {
		a["offset"] = offset
	}
	if q.location != "" {
		a["location"] = q.location
	}
	if q.country != "" {
		a["country_admin_code"] = q.country
	}
	if q.radius > 0 {
		a["radius_miles"] = q.radius
	}
	if q.postedDays > 0 {
		a["max_posted_minutes_ago"] = q.postedDays * 1440
	}
	if len(q.locationTypes) > 0 {
		a["location_types"] = q.locationTypes
	}
	if len(q.employmentTypes) > 0 {
		a["employment_types"] = q.employmentTypes
	}
	if len(q.seniorities) > 0 {
		a["seniority_classes"] = q.seniorities
	}
	return a
}

// CheckFilters reports whether f is a valid ZipRecruiter board's filters
// (FilterKeys, with the values each takes), as List checks them before any
// request.
func CheckFilters(f map[string][]string) error {
	_, err := parseFilters(f)
	return err
}

func parseFilters(f map[string][]string) (query, error) {
	var q query
	for k, vs := range f {
		if !slices.Contains(FilterKeys, k) {
			return query{}, fmt.Errorf("filter %q is not one of %s", k, strings.Join(FilterKeys, ", "))
		}
		if len(vs) == 0 {
			return query{}, fmt.Errorf("filter %q has no value", k)
		}
		if len(vs) > sources.MaxFilterValues {
			return query{}, fmt.Errorf("filter %q has %d values, over %d", k, len(vs), sources.MaxFilterValues)
		}
		for _, v := range vs {
			if !cleanValue(v) {
				return query{}, fmt.Errorf("filter %q has an empty, over-long or control-character value", k)
			}
		}
		one := func() (string, error) {
			if len(vs) != 1 {
				return "", fmt.Errorf("filter %q takes exactly one value", k)
			}
			return strings.TrimSpace(vs[0]), nil
		}
		var err error
		switch k {
		case FilterLocation:
			q.location, err = one()
		case FilterCountry:
			var v string
			if v, err = one(); err == nil {
				switch v = strings.ToUpper(v); v {
				case "US", "CA":
					q.country = v
				default:
					err = fmt.Errorf("filter %q is %q; the server serves US and CA only", k, v)
				}
			}
		case FilterRadius:
			var v string
			if v, err = one(); err == nil {
				q.radius, err = bounded(k, v, MaxRadiusMiles)
			}
		case FilterPostedWithin:
			var v string
			if v, err = one(); err == nil {
				q.postedDays, err = bounded(k, v, MaxPostedDays)
			}
		case FilterLocationType:
			q.locationTypes, err = enum(k, vs, locationTypes)
		case FilterEmploymentType:
			q.employmentTypes, err = enum(k, vs, employmentTypes)
		case FilterSeniority:
			q.seniorities, err = enum(k, vs, seniorities)
		}
		if err != nil {
			return query{}, err
		}
	}
	if q.radius > 0 && q.location == "" {
		return query{}, fmt.Errorf("filter %q needs a %q filter to measure from", FilterRadius, FilterLocation)
	}
	return q, nil
}

// cleanValue is a filter value that is not blank, at most
// sources.MaxSearchLen bytes, valid UTF-8 and free of control characters.
func cleanValue(v string) bool {
	t := strings.TrimSpace(v)
	return t != "" && len(t) <= sources.MaxSearchLen && utf8.ValidString(t) &&
		strings.IndexFunc(t, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) < 0
}

// bounded reads v as a whole number from 1 to most, in plain digits.
func bounded(k, v string, most int) (int, error) {
	if v == "" || strings.Trim(v, "0123456789") != "" || len(v) > 4 {
		return 0, fmt.Errorf("filter %q is %q, not a whole number from 1 to %d", k, v, most)
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > most {
		return 0, fmt.Errorf("filter %q is %q, not a whole number from 1 to %d", k, v, most)
	}
	return n, nil
}

// enum reads vs as values of allowed, case-insensitively with '-' read as
// '_', and returns them upper-case and sorted. A repeated value is an error.
func enum(k string, vs, allowed []string) ([]string, error) {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		u := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(v), "-", "_"))
		if !slices.Contains(allowed, u) {
			return nil, fmt.Errorf("filter %q value %q is not one of %s", k, v, strings.ToLower(strings.Join(allowed, ", ")))
		}
		if slices.Contains(out, u) {
			return nil, fmt.Errorf("filter %q repeats %q", k, v)
		}
		out = append(out, u)
	}
	slices.Sort(out)
	return out, nil
}

// currencyOf is the pay line's currency for the board's country filter: the
// row names none, and a search without one may span both countries.
func currencyOf(country string) string {
	switch country {
	case "US":
		return "USD"
	case "CA":
		return "CAD"
	}
	return ""
}

// posting is the row as a posting.
func (r row) posting(b sources.Board, currency string, seen time.Time) (posting.Posting, error) {
	lk := strings.TrimSpace(r.ListingKey)
	if lk == "" {
		return posting.Posting{}, fmt.Errorf("%w: row has no listing_key", ErrShape)
	}
	if len(lk) > maxListingKey {
		return posting.Posting{}, fmt.Errorf("%w: listing_key is %d bytes", ErrShape, len(lk))
	}
	key, err := posting.ZipRecruiterKey(b.Token, lk)
	if err != nil {
		return posting.Posting{}, err
	}
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := srcutil.Title(r.Title)
	if title == "" {
		return posting.Posting{}, fmt.Errorf("%w: listing %q has an empty title", ErrShape, lk)
	}
	company := fold(r.Company)
	if company == "" {
		return posting.Posting{}, fmt.Errorf("%w: listing %q has an empty company", ErrShape, lk)
	}
	page := JobPage + url.QueryEscape(lk)
	if err := srcutil.CheckURL("job page", page); err != nil {
		return posting.Posting{}, err
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.ZipRecruiter,
		Company:     company,
		Title:       title,
		Location:    fold(r.Location),
		Remote:      r.IsRemote,
		URL:         page,
		PostedAt:    r.postedAt(seen),
		Description: r.description(currency),
		SeenAt:      seen,
	}, nil
}

// fold is s with every run of whitespace one space, trimmed, and at most
// maxText bytes, cut at a rune boundary.
func fold(s string) string {
	t := strings.Join(strings.Fields(s), " ")
	if len(t) <= maxText {
		return t
	}
	cut := maxText
	for cut > 0 && !utf8.RuneStart(t[cut]) {
		cut--
	}
	return strings.TrimSpace(t[:cut])
}

// postedAt is the UTC day the row was posted: the day of seen less days_ago
// whole days. A missing, negative, non-finite or over-a-year days_ago is no
// date.
func (r row) postedAt(seen time.Time) time.Time {
	if r.DaysAgo == nil {
		return time.Time{}
	}
	d := *r.DaysAgo
	if math.IsNaN(d) || math.IsInf(d, 0) || d < 0 || d > maxDaysAgo {
		return time.Time{}
	}
	y, m, day := seen.UTC().Date()
	return time.Date(y, m, day, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -int(math.Floor(d)))
}

// description is the row's lead lines only: the pay line and the role line.
// A row has no description text of its own.
func (r row) description(currency string) string {
	var pay string
	if r.Salary != nil {
		pay = srcutil.PayLine(currency, r.Salary.MinAnnual, r.Salary.MaxAnnual, "year")
	}
	var flags []string
	if r.JobType != nil {
		for _, part := range strings.Split(*r.JobType, "·") {
			if f := strings.ToLower(srcutil.EmploymentType(part)); f != "" {
				flags = append(flags, f)
			}
		}
	}
	if r.IsRemote {
		flags = append(flags, "remote")
	}
	slices.Sort(flags)
	flags = slices.Compact(flags)
	lead, _ := srcutil.Lead(pay, srcutil.RoleLine(flags...))
	return strings.TrimRight(lead, "\n")
}
