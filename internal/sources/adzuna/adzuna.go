// Package adzuna lists postings from Adzuna's job-search REST API,
// GET https://api.adzuna.com/v1/api/jobs/{country}/search/{page} (OpenAPI
// spec read 2026-11-24 at developer.adzuna.com/swagger/spec/test2.json).
// Adzuna is an aggregator, not an employer's ATS: one search lists many
// employers, so each posting's company is its row's own, and a row that names
// none is a bad row rather than one filed under the board's name.
//
// The contract this adapter is built on, as documented on 2026-11-24:
//   - country is one of Countries; page numbers start at 1. A board's token is
//     its country, so a watchlist may hold one Adzuna board per country.
//   - app_id and app_key are required query parameters; the API has no header
//     form. JSON is asked for with Accept: application/json.
//   - The answer is {count, mean, results:[job]}: count is the search's total
//     matches; a job carries id (a string of digits), title, description
//     (truncated by Adzuna to 500 characters), created (ISO 8601),
//     redirect_url (the link Adzuna's terms require users to be sent through),
//     location.display_name, category {tag, label}, company.display_name,
//     salary_min/salary_max, salary_is_predicted ("0"/"1", read as a number
//     too), contract_time and contract_type. Adzuna marks search matches with
//     <strong> in title and description; both are reduced as HTML.
//   - A bad key answered HTTP 401 with {"exception":"AUTH_FAIL",...} when tried
//     live without a valid key; the spec also lists 400, 404, 410 (auth) and
//     500.
//
// NOT VERIFIED LIVE WITH A KEY: no request with a real app id and key was
// possible when this was written, so the 200 shape, paging past the last
// page, whether count is exact, and whether redirect_url's v= value is stable
// across calls are from the documentation only. Every doubt fails the board.
//
// Terms (developer.adzuna.com/docs/terms_of_service, 2026-11-24): permitted use
// includes personal job search; the key is free; the limits are shown in the
// account (historically about 25 requests a minute, 250 a day, 1,000 a week,
// 2,500 a month); Adzuna is to be acknowledged as the source, and users sent
// to jobs through redirect_url. Vani stores redirect_url (utm_* parameters
// removed, so the stored link is stable) as both the posting URL and the apply
// URL, spaces requests at least MinGap apart, and sends at most MaxRequests a
// board listing.
//
// Key handling: the app id and key come from Options.Credentials, supplied by
// the run wiring (this package reads no environment or keychain). They never
// enter a URL this package hands to srcutil: the adapter wraps the egress.Doer
// it is given in one that adds them to a clone of each request, sends only to
// https://api.adzuna.com/v1/api/jobs/, and replaces every occurrence of either
// value (raw, query-escaped and path-escaped) with REDACTED in any error it
// returns, keeping the error's chain (a *url.Error is rebuilt with the keyless
// URL and its cause) so srcutil's transient and rate-limit handling still
// work. A redirect answer's Location is dropped, and nothing is re-sent with
// the key by this package. Every error List returns is scrubbed once more.
//
// A board needs at least one search term (each is Adzuna's "what"); a
// country's whole listing is far over the cap. A search, or the union of the
// board's searches, matching more than the cap (MaxPostings) fails the board
// before any further page rather than being truncated. The listing is
// Windowed (see Stats) exactly when the max_days_old filter is set: postings
// older than the window drop out of the listing by design, not because they
// closed.
package adzuna

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	// Host is the API's one host; it must be in allowed_hosts.
	Host = "api.adzuna.com"
	// searchPath is the path every request is under; the key is sent nowhere
	// else.
	searchPath = "/v1/api/jobs/"
	// MaxPostings is a board's default cap, the union of its searches. At 50
	// rows a page it is 10 requests a search.
	MaxPostings = 500
	// MaxPostingsCeiling bounds a board's own max_postings.
	MaxPostingsCeiling = 1000
	// MaxRequests bounds the HTTP requests one List sends, retries included,
	// against the free tier's daily allowance (historically 250).
	MaxRequests = 100
	// MinGap is the least time between two requests this source sends, across
	// all its boards: the free tier allowed about 25 a minute.
	MinGap = 3 * time.Second
	// pageSize is results_per_page, sent on every request.
	pageSize = 50
	// maxCredential bounds an app id or key in bytes.
	maxCredential = 128
	// maxFilterText bounds a free-text filter value in bytes.
	maxFilterText = 100
	// maxErrBody is how much of a non-2xx answer is read for its exception.
	maxErrBody = 16 << 10
	// redacted replaces an app id or key in error text.
	redacted = "REDACTED"
)

// currencies maps each country the API serves to the ISO currency its salary
// figures are in. It is also the country list ValidCountry reads.
var currencies = map[string]string{
	"at": "EUR", "au": "AUD", "be": "EUR", "br": "BRL", "ca": "CAD",
	"ch": "CHF", "de": "EUR", "es": "EUR", "fr": "EUR", "gb": "GBP",
	"in": "INR", "it": "EUR", "mx": "MXN", "nl": "EUR", "nz": "NZD",
	"pl": "PLN", "sg": "SGD", "us": "USD", "za": "ZAR",
}

// Countries are the country codes the API's {country} takes (spec enum,
// 2026-11-24), lower case. A board's token is one of them.
var Countries = []string{"at", "au", "be", "br", "ca", "ch", "de", "es", "fr", "gb", "in", "it", "mx", "nl", "nz", "pl", "sg", "us", "za"}

// ValidCountry reports whether c is a country the API serves, exactly as the
// token must be written (lower case).
func ValidCountry(c string) bool {
	_, ok := currencies[c]
	return ok
}

// FilterKeys are the board filters this source maps onto the API's query
// parameters. Each takes exactly one value:
//   - where: a place name or postcode (where)
//   - distance_km: 1..500, the radius around where (distance); needs where
//   - max_days_old: 1..365 (max_days_old); makes the listing Windowed
//   - category: an Adzuna category tag, [a-z0-9-]{1,64} (category)
//   - salary_min: a whole amount 1..10000000 in the country's currency (salary_min)
//   - full_time, part_time, permanent, contract: "true" (sent as =1);
//     full_time with part_time, or permanent with contract, is refused
//   - what_exclude: words to exclude (what_exclude)
//   - company: a company name (company)
var FilterKeys = []string{"where", "distance_km", "max_days_old", "category", "salary_min",
	"full_time", "part_time", "permanent", "contract", "what_exclude", "company"}

var (
	// ErrNoCredentials is a List with no app id or key: no request is sent.
	ErrNoCredentials = errors.New("adzuna: no app id and key are configured")
	// ErrBadCredentials is an app id or key that cannot be one: whitespace or a
	// control character inside, invalid UTF-8, or over 128 bytes. The value is
	// never echoed.
	ErrBadCredentials = errors.New("adzuna: the app id or key is malformed")
	// ErrAuth is Adzuna refusing the credentials (HTTP 401 or 410, or an
	// AUTH_FAIL exception).
	ErrAuth = errors.New("Adzuna refused the app id and key (AUTH_FAIL)")
	// ErrShape is a response whose layout is not the documented one.
	ErrShape = errors.New("adzuna: unexpected search response")
	// ErrRequestBudget is a listing that would send more than MaxRequests.
	ErrRequestBudget = errors.New("adzuna: the board's request budget is spent")
)

// Stats is one board listing's counts, for Options.Report.
type Stats struct {
	// Listed is the distinct postings the listing produced (so far, when it
	// failed after listing).
	Listed int `json:"listed"`
	// Requests is the HTTP requests sent, retries included.
	Requests int `json:"requests"`
	// Matched is the sum of each search's count on its first page: what Adzuna
	// says the searches match, before the union.
	Matched int `json:"matched"`
	// Windowed is set when the board has max_days_old: the listing is by design
	// not the country's whole word, and a posting missing from it may only
	// have aged out of the window.
	Windowed bool `json:"windowed,omitempty"`
}

// Options configure the source (NewWith).
type Options struct {
	// Credentials returns the app id and key, called once per List before any
	// request. Nil, or an empty value, is ErrNoCredentials.
	Credentials func(ctx context.Context) (appID, appKey string, err error)
	// Report, when set, receives a board's counts once per List that sent a
	// request, whether the board then succeeded or failed.
	Report func(sources.Board, Stats)
	// Wait spaces requests (MinGap); nil is srcutil.Sleep. A field only so
	// tests need not wait in real time.
	Wait func(ctx context.Context, d time.Duration) error
}

type source struct {
	d    egress.Doer
	opt  Options
	now  func() time.Time
	gate chan struct{} // one slot: held while a request is spaced and recorded
	last time.Time     // when the last request was sent; guarded by gate
}

// New returns the Adzuna source with no credentials: every List is
// ErrNoCredentials until the run wiring uses NewWith.
func New(d egress.Doer) sources.Source {
	s, _ := NewWith(d, Options{})
	return s
}

// NewWith returns the Adzuna source sending requests through d.
func NewWith(d egress.Doer, o Options) (sources.Source, error) {
	if o.Wait == nil {
		o.Wait = srcutil.Sleep
	}
	return &source{d: d, opt: o, now: time.Now, gate: make(chan struct{}, 1)}, nil
}

func (*source) ATS() posting.ATS { return posting.Adzuna }

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.d == nil {
		return nil, errors.New("adzuna: no http doer configured")
	}
	if err := srcutil.CheckBoard(b, posting.Adzuna); err != nil {
		return nil, err
	}
	if !ValidCountry(b.Token) {
		return nil, fmt.Errorf("adzuna: board token %q is not one of the API's countries (%s)", b.Token, strings.Join(Countries, ", "))
	}
	country := b.Token
	terms, err := checkTerms(b.SearchTerms())
	if err != nil {
		return nil, fmt.Errorf("adzuna %q: %w", country, err)
	}
	params, windowed, err := filterParams(b.Filters)
	if err != nil {
		return nil, fmt.Errorf("adzuna %q: %w", country, err)
	}
	limit, err := capOf(b)
	if err != nil {
		return nil, fmt.Errorf("adzuna %q: %w", country, err)
	}
	id, key, err := s.credentials(ctx)
	if err != nil {
		return nil, fmt.Errorf("adzuna %q: %w", country, err)
	}
	sec := newSecrets(id, key)
	st := Stats{Windowed: windowed}
	kd := &keyDoer{inner: s.d, src: s, id: id, key: key, sec: sec, st: &st}
	if s.opt.Report != nil {
		defer func() {
			if st.Requests > 0 {
				s.opt.Report(b, st)
			}
		}()
	}
	out, err := s.list(ctx, kd, b, terms, params, limit, &st)
	if err != nil {
		return nil, sec.scrub(err)
	}
	return out, nil
}

func (s *source) list(ctx context.Context, kd *keyDoer, b sources.Board, terms []string, params url.Values, limit int, st *Stats) ([]posting.Posting, error) {
	seen := s.now()
	var out []posting.Posting
	keys := map[posting.Key]bool{}
	rows := srcutil.NewRows(ctx, b, 0)
	for _, term := range terms {
		got, count, err := search(ctx, kd, b.Token, term, params, limit)
		st.Matched += count
		if err != nil {
			return nil, fmt.Errorf("adzuna %q: search %q: %w", b.Token, term, kd.explain(err))
		}
		rows.Add(len(got))
		for _, r := range got {
			p, err := r.posting(b, seen, kd.sec)
			if err != nil {
				if ferr := rows.Bad(err); ferr != nil {
					return nil, fmt.Errorf("adzuna %q: search %q: %w", b.Token, term, ferr)
				}
				continue
			}
			if !keys[p.Key] {
				keys[p.Key] = true
				out = append(out, p)
				st.Listed = len(out)
			}
		}
		if len(out) > limit {
			return nil, fmt.Errorf("adzuna %q: %w: the board's searches together list more than %d postings; narrow them with where, max_days_old or category",
				b.Token, srcutil.ErrTooManyPostings, limit)
		}
	}
	if err := rows.Done(); err != nil {
		return nil, fmt.Errorf("adzuna %q: %w", b.Token, err)
	}
	return out, nil
}

// credentials reads and checks the app id and key.
func (s *source) credentials(ctx context.Context) (id, key string, err error) {
	if s.opt.Credentials == nil {
		return "", "", ErrNoCredentials
	}
	id, key, err = s.opt.Credentials(ctx)
	if err != nil {
		return "", "", fmt.Errorf("reading the app id and key: %w", err)
	}
	if id, err = credential("app id", id); err != nil {
		return "", "", err
	}
	if key, err = credential("app key", key); err != nil {
		return "", "", err
	}
	return id, key, nil
}

func credential(name, v string) (string, error) {
	v = strings.TrimSpace(v)
	switch {
	case v == "":
		return "", ErrNoCredentials
	case len(v) > maxCredential:
		return "", fmt.Errorf("%w: the %s is over %d bytes", ErrBadCredentials, name, maxCredential)
	case !utf8.ValidString(v):
		return "", fmt.Errorf("%w: the %s is not valid UTF-8", ErrBadCredentials, name)
	case strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		return "", fmt.Errorf("%w: the %s holds whitespace or a control character", ErrBadCredentials, name)
	}
	return v, nil
}

func checkTerms(terms []string) ([]string, error) {
	switch {
	case len(terms) == 0:
		return nil, errors.New("the board needs at least one search; a country's whole listing is far over the cap")
	case len(terms) > sources.MaxSearches:
		return nil, fmt.Errorf("the board has %d searches, over the %d allowed", len(terms), sources.MaxSearches)
	}
	for _, t := range terms {
		if err := checkText(t, sources.MaxSearchLen); err != nil {
			return nil, fmt.Errorf("search %q: %w", t, err)
		}
	}
	return terms, nil
}

// checkText is a free-text value sent as a query parameter.
func checkText(v string, max int) error {
	switch {
	case v == "":
		return errors.New("is empty")
	case len(v) > max:
		return fmt.Errorf("is over %d bytes", max)
	case !utf8.ValidString(v):
		return errors.New("is not valid UTF-8")
	case strings.IndexFunc(v, unicode.IsControl) >= 0:
		return errors.New("holds a control character")
	}
	return nil
}

var (
	digitsRE   = regexp.MustCompile(`^[0-9]{1,9}$`)
	categoryRE = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
)

// filterParams checks a board's filters strictly and returns them as the API's
// query parameters, and whether the listing is windowed (max_days_old).
// CheckFilters is the filter rules List applies, for config validation: the
// keys are FilterKeys, each takes one value, and the values are checked as
// List checks them before any request.
func CheckFilters(f map[string][]string) error {
	_, _, err := filterParams(f)
	return err
}

func filterParams(f map[string][]string) (url.Values, bool, error) {
	q := url.Values{}
	one := map[string]string{}
	for k, vs := range f {
		if !slices.Contains(FilterKeys, k) {
			return nil, false, fmt.Errorf("filter %q is not one of %s", k, strings.Join(FilterKeys, ", "))
		}
		if len(vs) != 1 || strings.TrimSpace(vs[0]) == "" {
			return nil, false, fmt.Errorf("filter %q takes exactly one value", k)
		}
		one[k] = strings.TrimSpace(vs[0])
	}
	integer := func(k string, lo, hi int) (string, error) {
		v := one[k]
		if !digitsRE.MatchString(v) {
			return "", fmt.Errorf("filter %q must be a whole number %d..%d", k, lo, hi)
		}
		n, _ := strconv.Atoi(v)
		if n < lo || n > hi {
			return "", fmt.Errorf("filter %q must be a whole number %d..%d", k, lo, hi)
		}
		return strconv.Itoa(n), nil
	}
	for _, k := range []string{"where", "what_exclude", "company"} {
		if v, ok := one[k]; ok {
			if err := checkText(v, maxFilterText); err != nil {
				return nil, false, fmt.Errorf("filter %q %w", k, err)
			}
			q.Set(k, v)
		}
	}
	if _, ok := one["distance_km"]; ok {
		if _, ok := one["where"]; !ok {
			return nil, false, errors.New(`filter "distance_km" needs "where"`)
		}
		n, err := integer("distance_km", 1, 500)
		if err != nil {
			return nil, false, err
		}
		q.Set("distance", n)
	}
	windowed := false
	if _, ok := one["max_days_old"]; ok {
		n, err := integer("max_days_old", 1, 365)
		if err != nil {
			return nil, false, err
		}
		q.Set("max_days_old", n)
		windowed = true
	}
	if _, ok := one["salary_min"]; ok {
		n, err := integer("salary_min", 1, 10_000_000)
		if err != nil {
			return nil, false, err
		}
		q.Set("salary_min", n)
	}
	if v, ok := one["category"]; ok {
		if !categoryRE.MatchString(v) {
			return nil, false, errors.New(`filter "category" must be an Adzuna category tag: lower-case letters, digits and '-', at most 64`)
		}
		q.Set("category", v)
	}
	for _, k := range []string{"full_time", "part_time", "permanent", "contract"} {
		if v, ok := one[k]; ok {
			if v != "true" {
				return nil, false, fmt.Errorf("filter %q takes only the value \"true\"", k)
			}
			q.Set(k, "1")
		}
	}
	for _, pair := range [][2]string{{"full_time", "part_time"}, {"permanent", "contract"}} {
		if q.Has(pair[0]) && q.Has(pair[1]) {
			return nil, false, fmt.Errorf("filters %q and %q contradict each other", pair[0], pair[1])
		}
	}
	return q, windowed, nil
}

func capOf(b sources.Board) (int, error) {
	switch {
	case b.MaxPostings == 0:
		return MaxPostings, nil
	case b.MaxPostings < 0 || b.MaxPostings > MaxPostingsCeiling:
		return 0, fmt.Errorf("max_postings %d is outside 1..%d", b.MaxPostings, MaxPostingsCeiling)
	}
	return b.MaxPostings, nil
}

// pageURL is one search page's URL, without credentials (keyDoer adds them).
func pageURL(country, term string, params url.Values, page int) string {
	q := url.Values{}
	for k, vs := range params {
		q[k] = append([]string(nil), vs...)
	}
	q.Set("what", term)
	q.Set("sort_by", "date")
	q.Set("results_per_page", strconv.Itoa(pageSize))
	return "https://" + Host + searchPath + country + "/search/" + strconv.Itoa(page) + "?" + q.Encode()
}

type response struct {
	Count     *int              `json:"count"`
	Results   []json.RawMessage `json:"results"`
	Exception string            `json:"exception"`
}

type job struct {
	ID          json.RawMessage `json:"id"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Created     string          `json:"created"`
	RedirectURL string          `json:"redirect_url"`
	Location    struct {
		DisplayName string `json:"display_name"`
	} `json:"location"`
	Category struct {
		Label string `json:"label"`
	} `json:"category"`
	Company struct {
		DisplayName string `json:"display_name"`
	} `json:"company"`
	SalaryMin         *float64        `json:"salary_min"`
	SalaryMax         *float64        `json:"salary_max"`
	SalaryIsPredicted json.RawMessage `json:"salary_is_predicted"`
	ContractTime      string          `json:"contract_time"`
	ContractType      string          `json:"contract_type"`
}

// row is one result: decoded, or the reason it could not be.
type row struct {
	j   job
	id  string
	err error
}

// search pages one term to its count and returns its rows and its first
// page's count. A count over limit fails before any further page. Each page's
// count is the latest word on a listing that changes while it is read: rows
// shifted onto a page already read are dropped as repeats, and a page that
// adds nothing while the count is unmet is ErrShape.
func search(ctx context.Context, d egress.Doer, country, term string, params url.Values, limit int) ([]row, int, error) {
	var out []row
	ids := map[string]bool{}
	matched := 0
	maxPages := limit/pageSize + 2
	for page := 1; page <= maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, matched, err
		}
		var resp response
		if err := srcutil.GetJSON(ctx, d, pageURL(country, term, params, page), &resp); err != nil {
			return nil, matched, err
		}
		if resp.Exception != "" {
			return nil, matched, exceptionError(resp.Exception)
		}
		if resp.Count == nil || *resp.Count < 0 || resp.Results == nil {
			return nil, matched, fmt.Errorf("%w: page %d has no count or results list", ErrShape, page)
		}
		count := *resp.Count
		if page == 1 {
			matched = count
		}
		if count > limit {
			return nil, matched, fmt.Errorf("%w: search matches %d postings, over the %d limit; narrow it with where, max_days_old or category",
				srcutil.ErrTooManyPostings, count, limit)
		}
		added := 0
		for _, raw := range resp.Results {
			r := decodeRow(raw)
			if r.err == nil {
				if ids[r.id] {
					continue // paging shifted under a changing listing
				}
				ids[r.id] = true
			}
			out = append(out, r)
			added++
		}
		if page == 1 {
			if err := srcutil.TotalCoversRows(count, len(out)); err != nil {
				return nil, matched, fmt.Errorf("%w: %w", ErrShape, err)
			}
		}
		if len(out) >= count {
			return out, matched, nil
		}
		if added == 0 {
			return nil, matched, fmt.Errorf("%w: page %d added nothing with %d of %d read", ErrShape, page, len(out), count)
		}
	}
	return nil, matched, fmt.Errorf("%w: listing did not end within %d pages", ErrShape, maxPages)
}

var idRE = regexp.MustCompile(`^[0-9]{1,20}$`)

func decodeRow(raw json.RawMessage) row {
	var j job
	if err := json.Unmarshal(raw, &j); err != nil {
		return row{err: fmt.Errorf("%w: a result is not a job object", ErrShape)}
	}
	id := strings.TrimSpace(string(j.ID))
	if strings.HasPrefix(id, `"`) {
		var s string
		if err := json.Unmarshal(j.ID, &s); err != nil {
			return row{err: fmt.Errorf("%w: a result's id is malformed", ErrShape)}
		}
		id = strings.TrimSpace(s)
	}
	if !idRE.MatchString(id) {
		return row{err: fmt.Errorf("%w: a result's id is not up to 20 digits", ErrShape)}
	}
	return row{j: j, id: id}
}

// exceptionError is an exception named in a 200 body.
func exceptionError(ex string) error {
	if ex == "AUTH_FAIL" {
		return ErrAuth
	}
	return fmt.Errorf("%w: Adzuna answered exception %s", ErrShape, exceptionName(ex))
}

var exceptionRE = regexp.MustCompile(`^[A-Z0-9_]{1,40}$`)

// exceptionName is Adzuna's exception code if it looks like one; the text is
// untrusted, so anything else is not echoed.
func exceptionName(ex string) string {
	if exceptionRE.MatchString(ex) {
		return ex
	}
	return "(unrecognised)"
}

func (r row) posting(b sources.Board, seen time.Time, sec secrets) (posting.Posting, error) {
	if r.err != nil {
		return posting.Posting{}, r.err
	}
	j := r.j
	key, err := posting.AdzunaKey(b.Token, r.id)
	if err != nil {
		return posting.Posting{}, err
	}
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := srcutil.Title(htmltext.Text(j.Title, srcutil.MaxTitle))
	if title == "" {
		return posting.Posting{}, fmt.Errorf("%w: job %s has an empty title", ErrShape, r.id)
	}
	company := htmltext.Text(j.Company.DisplayName, srcutil.MaxCompany)
	if company == "" {
		return posting.Posting{}, fmt.Errorf("%w: job %s names no company", ErrShape, r.id)
	}
	link, err := stableLink(j.RedirectURL, sec)
	if err != nil {
		return posting.Posting{}, fmt.Errorf("job %s: %w", r.id, err)
	}
	posted, err := srcutil.ParseTime("created", strings.TrimSpace(j.Created))
	if err != nil {
		return posting.Posting{}, fmt.Errorf("job %s: %w", r.id, err)
	}
	if !posted.IsZero() {
		posted = posted.UTC()
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.Adzuna,
		Company:     company,
		Title:       title,
		Location:    srcutil.RowPlace(htmltext.Text(j.Location.DisplayName, srcutil.MaxRowPlace)),
		URL:         link,
		ApplyURL:    link,
		PostedAt:    posted,
		Description: j.description(currencies[b.Token]),
		SeenAt:      seen,
	}, nil
}

// predicted reports whether salary_is_predicted marks the figures as Adzuna's
// estimate: "1" or 1 (or true). Absent, "0", 0 and false are the employer's
// figures; anything else is read as predicted, so no guess becomes a pay line.
func (j job) predicted() bool {
	v := strings.TrimSpace(string(j.SalaryIsPredicted))
	switch v {
	case "", "null", "0", `"0"`, "false", `"false"`:
		return false
	}
	return true
}

// description is a pay line (when the figures are the employer's), a role
// line, then the row's description text.
func (j job) description(currency string) string {
	pay := ""
	if !j.predicted() {
		pay = srcutil.PayLine(currency, j.SalaryMin, j.SalaryMax, "year")
	}
	role := srcutil.RoleLine(
		srcutil.EmploymentType(j.ContractTime),
		srcutil.EmploymentType(j.ContractType),
		htmltext.Text(j.Category.Label, 80),
	)
	lead, room := srcutil.Lead(pay, role)
	return lead + srcutil.Sections(room, srcutil.Section{HTML: j.Description})
}

// adzunaDomains are the sites redirect_url links through, one per country.
var adzunaDomains = []string{"adzuna.com", "adzuna.co.uk", "adzuna.at", "adzuna.com.au", "adzuna.be",
	"adzuna.com.br", "adzuna.ca", "adzuna.ch", "adzuna.de", "adzuna.es", "adzuna.fr", "adzuna.in",
	"adzuna.it", "adzuna.com.mx", "adzuna.nl", "adzuna.co.nz", "adzuna.pl", "adzuna.sg", "adzuna.co.za"}

func adzunaHost(h string) bool {
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	for _, d := range adzunaDomains {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}

// stableLink is redirect_url as stored: checked, upgraded to https on an
// Adzuna host, without utm_* parameters (and any parameter carrying the app id
// or key, which Adzuna's utm_source has been seen to resemble), the others
// kept in their order.
func stableLink(raw string, sec secrets) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: redirect_url is empty", ErrShape)
	}
	if err := srcutil.CheckURL("redirect_url", raw); err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("sources: redirect_url is not an absolute http(s) url")
	}
	if strings.EqualFold(u.Scheme, "http") && adzunaHost(u.Hostname()) {
		u.Scheme = "https"
	}
	if u.RawQuery != "" {
		var kept []string
		for _, part := range strings.Split(u.RawQuery, "&") {
			if part == "" {
				continue
			}
			name, _, _ := strings.Cut(part, "=")
			if n, err := url.QueryUnescape(name); err == nil {
				name = n
			}
			if strings.HasPrefix(strings.ToLower(name), "utm_") || sec.inURL(part) {
				continue
			}
			kept = append(kept, part)
		}
		u.RawQuery = strings.Join(kept, "&")
	}
	u.ForceQuery = false
	out := u.String()
	if sec.inURL(out) {
		return "", fmt.Errorf("%w: redirect_url carries the app id or key outside its query", ErrShape)
	}
	if len(out) > srcutil.MaxURL {
		return "", fmt.Errorf("%w: redirect_url is over %d bytes", ErrShape, srcutil.MaxURL)
	}
	return out, nil
}

// keyDoer adds the credentials to each request, spaces requests, counts them,
// and keeps the credentials out of everything it returns.
type keyDoer struct {
	inner   egress.Doer
	src     *source
	id, key string
	sec     secrets
	st      *Stats

	status    int    // the last non-2xx status answered
	exception string // that answer's exception, when it named one
}

func (k *keyDoer) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, errors.New("adzuna: nil request")
	}
	u := req.URL
	if u.Scheme != "https" || u.Host != Host || u.User != nil || !strings.HasPrefix(u.Path, searchPath) {
		return nil, fmt.Errorf("adzuna: refusing to send the app key to %s", u.Redacted())
	}
	if q := u.Query(); q.Has("app_id") || q.Has("app_key") {
		return nil, errors.New("adzuna: a request URL already names app_id or app_key")
	}
	if k.st.Requests >= MaxRequests {
		return nil, fmt.Errorf("%w: %d requests sent", ErrRequestBudget, k.st.Requests)
	}
	if err := k.src.space(ctx); err != nil {
		return nil, err
	}
	k.st.Requests++
	keyed := *u
	sep := "&"
	if keyed.RawQuery == "" {
		sep = ""
	}
	keyed.RawQuery += sep + "app_id=" + url.QueryEscape(k.id) + "&app_key=" + url.QueryEscape(k.key)
	r := req.Clone(ctx)
	r.URL = &keyed
	resp, err := k.inner.Do(ctx, r)
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return nil, k.sec.scrub(err)
	}
	if resp == nil {
		return nil, nil
	}
	resp.Request = req // the keyless request
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		k.status, k.exception = 0, ""
		if resp.Body != nil {
			resp.Body = &scrubBody{rc: resp.Body, sec: k.sec}
		}
		return resp, nil
	}
	// A redirect is not followed here, and its target is not kept.
	resp.Header.Del("Location")
	resp.Header.Del("Content-Location")
	k.status, k.exception = resp.StatusCode, ""
	if resp.Body != nil {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		resp.Body.Close()
		var e struct {
			Exception string `json:"exception"`
		}
		if json.Unmarshal(buf, &e) == nil {
			k.exception = e.Exception
		}
		resp.Body = io.NopCloser(bytes.NewReader(buf))
	}
	return resp, nil
}

// explain turns an Adzuna refusal of the credentials into ErrAuth, and names a
// non-2xx answer's exception.
func (k *keyDoer) explain(err error) error {
	var se *egress.StatusError
	isStatus := errors.As(err, &se)
	switch {
	case errors.Is(err, ErrAuth):
		return err
	case isStatus && (se.Code == http.StatusUnauthorized || se.Code == http.StatusGone),
		k.status != 0 && k.exception == "AUTH_FAIL":
		code := k.status
		if isStatus {
			code = se.Code
		}
		return fmt.Errorf("%w (HTTP %d)", ErrAuth, code)
	case isStatus && k.exception != "":
		return fmt.Errorf("Adzuna answered exception %s: %w", exceptionName(k.exception), err)
	}
	return err
}

// space waits until MinGap has passed since the source's last request, and
// records this one. It holds the gate while it waits, so boards listed at
// once take turns.
func (s *source) space(ctx context.Context) error {
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.gate }()
	if !s.last.IsZero() {
		if d := s.last.Add(MinGap).Sub(s.now()); d > 0 {
			if err := s.opt.Wait(ctx, d); err != nil {
				return err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.last = s.now()
	return nil
}

// scrubBody scrubs a read error of a 2xx body.
type scrubBody struct {
	rc  io.ReadCloser
	sec secrets
}

func (b *scrubBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if err != nil && err != io.EOF {
		err = b.sec.scrub(err)
	}
	return n, err
}

func (b *scrubBody) Close() error { return b.sec.scrub(b.rc.Close()) }

// secrets are the forms the app id and key can take in error text, longest
// first.
type secrets struct {
	forms []string
}

var pctRE = regexp.MustCompile(`%[0-9A-F]{2}`)

func newSecrets(vals ...string) secrets {
	set := map[string]bool{}
	var s secrets
	for _, v := range vals {
		if v == "" {
			continue
		}
		q := strconv.Quote(v)
		js, _ := json.Marshal(v)
		for _, f := range []string{
			v,
			url.QueryEscape(v), url.PathEscape(v),
			pctRE.ReplaceAllStringFunc(url.QueryEscape(v), strings.ToLower),
			pctRE.ReplaceAllStringFunc(url.PathEscape(v), strings.ToLower),
			q[1 : len(q)-1], string(js[1 : len(js)-1]),
		} {
			if f != "" && !set[f] {
				set[f] = true
				s.forms = append(s.forms, f)
			}
		}
	}
	slices.SortFunc(s.forms, func(a, b string) int { return len(b) - len(a) })
	return s
}

func (s secrets) in(str string) bool {
	for _, f := range s.forms {
		if strings.Contains(str, f) {
			return true
		}
	}
	return false
}

// inURL is in for a stored link: only values long enough that a match is not
// chance (an app id is 8 hex digits, a key 32).
func (s secrets) inURL(str string) bool {
	for _, f := range s.forms {
		if len(f) >= 4 && strings.Contains(str, f) {
			return true
		}
	}
	return false
}

func (s secrets) clean(str string) string {
	for _, f := range s.forms {
		str = strings.ReplaceAll(str, f, redacted)
	}
	return str
}

// scrub returns err with every form of the secrets replaced in its text. An
// error that holds none is returned as it is, so its identity is kept. A
// *url.Error is rebuilt around its scrubbed cause; anything else becomes a
// scrubbedError, whose chain is the scrubbed chain of the original and whose
// Is and Timeout answer for the original.
func (s secrets) scrub(err error) error {
	if err == nil || len(s.forms) == 0 || !s.in(err.Error()) {
		return err
	}
	if ue, ok := err.(*url.Error); ok {
		out := &url.Error{Op: s.clean(ue.Op), URL: s.clean(ue.URL), Err: s.scrub(ue.Err)}
		if !s.in(out.Error()) {
			return out
		}
	}
	return &scrubbedError{msg: s.clean(err.Error()), orig: err, sec: s}
}

type scrubbedError struct {
	msg  string
	orig error
	sec  secrets
}

func (e *scrubbedError) Error() string { return e.msg }

func (e *scrubbedError) Unwrap() []error {
	var inner []error
	switch u := e.orig.(type) {
	case interface{ Unwrap() error }:
		if w := u.Unwrap(); w != nil {
			inner = []error{w}
		}
	case interface{ Unwrap() []error }:
		inner = u.Unwrap()
	}
	out := make([]error, 0, len(inner))
	for _, w := range inner {
		if w != nil {
			out = append(out, e.sec.scrub(w))
		}
	}
	return out
}

// Is answers for the original chain: a sentinel comparison leaks nothing.
func (e *scrubbedError) Is(target error) bool { return errors.Is(e.orig, target) }

// Timeout answers for the original chain, so a timed-out request still reads
// as one (srcutil.Transient).
func (e *scrubbedError) Timeout() bool { return timedOut(e.orig, 0) }

func timedOut(err error, depth int) bool {
	if err == nil || depth > 32 {
		return false
	}
	if t, ok := err.(interface{ Timeout() bool }); ok && t.Timeout() {
		return true
	}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		return timedOut(u.Unwrap(), depth+1)
	case interface{ Unwrap() []error }:
		for _, c := range u.Unwrap() {
			if timedOut(c, depth+1) {
				return true
			}
		}
	}
	return false
}
