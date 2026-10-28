// Package freehire asks freehire.me's public search API which companies have
// roles matching a search, so the owner can add those companies' boards to the
// watchlist. It is board discovery, not a posting source: nothing here becomes
// a posting. Vani reads every posting from the employer's own board.
//
// freehire.me's terms allow keyless use for personal, non-commercial purposes
// and forbid scraping beyond the documented API. So a search is one request,
// at most MaxPages of PageSize are ever asked for, the pages are spaced, and a
// 429 is given up on quickly rather than retried for long. The API's own limit
// (x-ratelimit-limit: 600, window not stated) is far above what this makes.
//
// Everything in a response is untrusted text. Search validates each result
// (source name, posting URL, external id) and cleans the company name before
// anything is printed; a result that fails is counted in Result.Skipped, never
// silently dropped.
package freehire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// Host is the one host this package asks. The owner opts in by adding it to
// allowed_hosts (`vani watchlist discover --allow-host`).
const Host = "freehire.me"

// Bounds. All are ceilings.
const (
	// SearchURL is the documented search endpoint (docs/API.md, "Filtering jobs").
	SearchURL = "https://" + Host + "/api/v1/jobs/search"
	// PageSize is the results asked for per request: the endpoint's page size.
	PageSize = 100
	// DefaultPages and MaxPages bound how many pages one search may fetch.
	DefaultPages = 1
	MaxPages     = 3
	// PageGap is the wait before each page after the first.
	PageGap = 3 * time.Second
	// MaxBody bounds one response (a live page of 100 results was 394 KB, with
	// the descriptions this package never reads).
	MaxBody = 4 << 20
	// MaxSearch bounds the search text, in bytes.
	MaxSearch = 200

	maxFilterValues = 10
	maxURL          = 2048
	maxExternalID   = 512
	maxName         = 120 // runes
	// retryBudget is all the time a search may spend waiting to retry a 429 or
	// 503: a rate-limited discovery lookup is not worth a long wait.
	retryBudget = 15 * time.Second
)

// ErrNotFound is the search endpoint answering 404: the API has moved.
var ErrNotFound = errors.New("freehire: the search endpoint was not found (the API may have changed)")

// Query is one search. Country, WorkMode and Seniority each take one value or
// a comma-separated list (the API ORs them); empty leaves the filter off.
type Query struct {
	// Search is the full-text query ("ml engineer"); required.
	Search string
	// Country is ISO 3166-1 alpha-2 code(s), "US" or "US,CA".
	Country string
	// WorkMode is remote, hybrid or onsite.
	WorkMode string
	// Seniority is intern, junior, middle, senior, lead, staff, principal or c_level.
	Seniority string
}

var (
	workModes   = []string{"remote", "hybrid", "onsite"}
	seniorities = []string{"intern", "junior", "middle", "senior", "lead", "staff", "principal", "c_level"}
	countryRE   = regexp.MustCompile(`^[A-Za-z]{2}$`)
	sourceRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

// Check refuses a query the API would misread, before any request is made.
func (q Query) Check() error {
	_, err := q.values()
	return err
}

// values is the query string for one page, without the paging parameters.
func (q Query) values() (url.Values, error) {
	s := strings.TrimSpace(q.Search)
	switch {
	case s == "":
		return nil, errors.New("freehire: a search is required")
	case len(s) > MaxSearch:
		return nil, fmt.Errorf("freehire: the search is over %d bytes", MaxSearch)
	case !utf8.ValidString(s) || strings.ContainsFunc(s, unicode.IsControl):
		return nil, errors.New("freehire: the search holds control characters or invalid UTF-8")
	}
	v := url.Values{"q": {s}}
	for _, f := range []struct {
		name, raw string
		ok        func(string) bool
		want      string
		upper     bool
	}{
		{"countries", q.Country, countryRE.MatchString, "two-letter country codes such as US", true},
		{"work_mode", q.WorkMode, func(x string) bool { return slices.Contains(workModes, x) }, "one of " + strings.Join(workModes, ", "), false},
		{"seniority", q.Seniority, func(x string) bool { return slices.Contains(seniorities, x) }, "one of " + strings.Join(seniorities, ", "), false},
	} {
		if strings.TrimSpace(f.raw) == "" {
			continue
		}
		parts := strings.Split(f.raw, ",")
		if len(parts) > maxFilterValues {
			return nil, fmt.Errorf("freehire: %s takes at most %d values", f.name, maxFilterValues)
		}
		for i, p := range parts {
			p = strings.TrimSpace(p)
			if f.upper {
				p = strings.ToUpper(p)
			} else {
				p = strings.ToLower(p)
			}
			if !f.ok(p) {
				return nil, fmt.Errorf("freehire: %s %q must be %s", f.name, strings.TrimSpace(parts[i]), f.want)
			}
			parts[i] = p
		}
		v.Set(f.name, strings.Join(parts, ","))
	}
	return v, nil
}

// Job is one validated search result: the posting freehire found, on the ATS it
// names. Only the fields discovery reads are kept.
type Job struct {
	// Source is freehire's name for the posting's ATS or site ("greenhouse").
	Source string
	// ExternalID is freehire's id for the posting; on the ATSs that have boards
	// its part before the first ':' is the board's token.
	ExternalID string
	// URL is the posting's original https URL.
	URL string
	// Company is the cleaned company name, "" when the result had none.
	Company string
}

// Result is what a search found. Total, Fetched and Capped carry both numbers
// a capped sample needs: what freehire holds and what this read.
type Result struct {
	Jobs []Job
	// Total is the matches freehire reports for the search.
	Total int
	// Fetched is the results read, malformed and repeated ones included.
	Fetched int
	// Pages is the requests answered.
	Pages int
	// Skipped is the fetched results dropped as malformed; Repeated those seen
	// twice (the API ignoring the offset would otherwise double every count).
	Skipped, Repeated int
	// Capped is true when Fetched is below Total: the search holds more matches
	// than were read, whether the page cap or Stopped ended it.
	Capped bool
	// Stopped is why paging ended before the page cap, when a page after the
	// first failed: the jobs read so far are kept, and say so here. Empty when
	// every page asked for was read.
	Stopped string
}

// Client runs searches through Doer.
type Client struct {
	// Doer sends the requests; it enforces the host allowlist, https-only, the
	// private-address block and the redirect rules on every hop.
	Doer egress.Doer
	// Pages is how many pages to read, 1..MaxPages; zero means DefaultPages.
	Pages int
	// Gap is the wait between pages; zero or less means PageGap.
	Gap time.Duration
	// Sleep waits between pages; nil is the real wait. A field so tests need
	// not wait in real time.
	Sleep func(ctx context.Context, d time.Duration) error
}

// pageResponse is the search envelope. Each result is decoded on its own so
// one malformed result costs one result, not the page.
type pageResponse struct {
	Data *[]json.RawMessage `json:"data"`
	Meta *struct {
		Total *int `json:"total"`
	} `json:"meta"`
}

type wireJob struct {
	Source      string `json:"source"`
	ExternalID  string `json:"external_id"`
	URL         string `json:"url"`
	Company     string `json:"company"`
	CompanySlug string `json:"company_slug"`
}

// Search reads up to c.Pages pages of q's results. An error is returned only
// when nothing could be read (the first page failed, the query or the client is
// invalid, ctx ended); a later page's failure is Result.Stopped.
func (c Client) Search(ctx context.Context, q Query) (Result, error) {
	if c.Doer == nil {
		return Result{}, errors.New("freehire: no http doer configured")
	}
	base, err := q.values()
	if err != nil {
		return Result{}, err
	}
	pages := c.Pages
	if pages == 0 {
		pages = DefaultPages
	}
	if pages < 1 || pages > MaxPages {
		return Result{}, fmt.Errorf("freehire: pages %d must be within 1..%d", pages, MaxPages)
	}
	gap := c.Gap
	if gap <= 0 {
		gap = PageGap
	}
	pacer := srcutil.Pacer{Every: gap, Budget: gap * MaxPages, Sleep: c.Sleep}
	ctx = srcutil.WithRetryBudget(ctx, retryBudget)

	var res Result
	seen := map[string]bool{}
	for page := 0; page < pages; page++ {
		if err := pacer.Wait(ctx); err != nil {
			err = c.stop(&res, page, err)
			return res, err
		}
		raw, total, err := c.fetch(ctx, base, page*PageSize)
		if err != nil {
			err = c.stop(&res, page, err)
			return res, err
		}
		res.Pages++
		res.Total = total
		res.Fetched += len(raw)
		for _, r := range raw {
			j, ok := validJob(r)
			if !ok {
				res.Skipped++
				continue
			}
			key := j.Source + "\x00" + j.ExternalID + "\x00" + j.URL
			if seen[key] {
				res.Repeated++
				continue
			}
			seen[key] = true
			res.Jobs = append(res.Jobs, j)
		}
		if len(raw) == 0 || res.Fetched >= total {
			break
		}
	}
	res.Capped = res.Fetched < res.Total
	return res, nil
}

// stop ends a search at page with err: the whole search failed when page is
// the first, or ctx ended; otherwise what was read stands, with the reason.
func (c Client) stop(res *Result, page int, err error) error {
	if page == 0 || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	res.Stopped = fmt.Sprintf("page %d was not read: %v", page+1, err)
	res.Capped = res.Fetched < res.Total
	return nil
}

// fetch reads one page: its raw results and the match total.
func (c Client) fetch(ctx context.Context, base url.Values, offset int) ([]json.RawMessage, int, error) {
	v := url.Values{}
	for k, vs := range base {
		v[k] = vs
	}
	v.Set("limit", strconv.Itoa(PageSize))
	v.Set("offset", strconv.Itoa(offset))
	body, err := srcutil.GetBodyLimit(ctx, c.Doer, SearchURL+"?"+v.Encode(), "application/json", MaxBody)
	switch {
	case errors.Is(err, srcutil.ErrBoardNotFound):
		return nil, 0, ErrNotFound
	case err != nil:
		return nil, 0, err
	}
	var resp pageResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, 0, fmt.Errorf("freehire: the response is not the search envelope: %w", err)
	}
	if resp.Data == nil || resp.Meta == nil || resp.Meta.Total == nil || *resp.Meta.Total < 0 {
		return nil, 0, errors.New("freehire: the response has no data list or no match total")
	}
	if len(*resp.Data) > PageSize {
		return nil, 0, fmt.Errorf("freehire: a page of %d results is over the %d asked for", len(*resp.Data), PageSize)
	}
	return *resp.Data, *resp.Meta.Total, nil
}

// validJob decodes and checks one result.
func validJob(raw json.RawMessage) (Job, bool) {
	var w wireJob
	if err := json.Unmarshal(raw, &w); err != nil {
		return Job{}, false
	}
	w.Source = strings.ToLower(strings.TrimSpace(w.Source))
	if !sourceRE.MatchString(w.Source) || len(w.URL) > maxURL || len(w.ExternalID) > maxExternalID ||
		strings.ContainsFunc(w.ExternalID, unicode.IsControl) {
		return Job{}, false
	}
	u, err := url.Parse(w.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || srcutil.CheckURL("url", w.URL) != nil {
		return Job{}, false
	}
	name := CleanName(w.Company)
	if name == "" {
		name = CleanName(w.CompanySlug)
	}
	return Job{Source: w.Source, ExternalID: w.ExternalID, URL: w.URL, Company: name}, true
}

// CleanName is untrusted text as one printable line of at most maxName runes:
// whitespace runs collapse to one space, and control, format (bidirectional
// overrides, zero-width) and other non-printing characters are dropped, so a
// name cannot reorder or hide what the terminal shows.
func CleanName(s string) string {
	s = strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
	s = strings.Map(func(r rune) rune {
		if r == ' ' || unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxName {
		s = string([]rune(s)[:maxName])
	}
	return strings.TrimSpace(s)
}
