// Package workday lists postings from a Workday candidate-experience site
// through the public JSON endpoints behind it:
//
//	POST https://{host}/wday/cxs/{tenant}/{site}/jobs        (paged listing)
//	GET  https://{host}/wday/cxs/{tenant}/{site}{externalPath} (one posting)
//
// The listing carries no description, so every posting costs one detail
// request. Workday refuses a page larger than 20 rows, reports its total only
// on the first page, and has no stable cursor, so the adapter caps a board at
// MaxPostings and asks the user to narrow a larger board with `search`.
package workday

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// ErrBoardNotFound is returned when Workday has no such tenant/site.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

const (
	// MaxPostings is the most postings one board may list. Each posting costs a
	// detail request, so the cap is far below the other adapters'.
	MaxPostings = 200
	// pageSize is Workday's largest page: a larger limit returns zero rows.
	pageSize = 20
	// maxPages bounds the listing loop even when total is wrong: the cap plus
	// the one empty page that ends a listing, plus one spare.
	maxPages = MaxPostings/pageSize + 2
)

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the Workday source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (s *source) ATS() posting.ATS { return posting.Workday }

type listRequest struct {
	AppliedFacets map[string]any `json:"appliedFacets"`
	Limit         int            `json:"limit"`
	Offset        int            `json:"offset"`
	SearchText    string         `json:"searchText"`
}

type listResponse struct {
	Total       int         `json:"total"`
	JobPostings []listedJob `json:"jobPostings"`
}

type listedJob struct {
	Title         string `json:"title"`
	ExternalPath  string `json:"externalPath"`
	LocationsText string `json:"locationsText"`
	PostedOn      string `json:"postedOn"`
}

type detailResponse struct {
	JobPostingInfo struct {
		JobReqID       string `json:"jobReqId"`
		Title          string `json:"title"`
		Location       string `json:"location"`
		JobDescription string `json:"jobDescription"`
		StartDate      string `json:"startDate"`
		PostedOn       string `json:"postedOn"`
		ExternalURL    string `json:"externalUrl"`
		RemoteType     string `json:"remoteType"`
	} `json:"jobPostingInfo"`
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := srcutil.CheckBoard(b, posting.Workday); err != nil {
		return nil, err
	}
	tenant, site, err := parseToken(b.Token)
	if err != nil {
		return nil, fmt.Errorf("workday %q: %w", b.Token, err)
	}
	host, err := checkHost(b.Host, tenant)
	if err != nil {
		return nil, fmt.Errorf("workday %q: %w", b.Token, err)
	}
	if len(b.Filters) > 0 {
		return nil, fmt.Errorf("workday %q: filters are not supported (Workday facets are tenant-specific ids)", b.Token)
	}
	base := "https://" + host + "/wday/cxs/" + url.PathEscape(tenant) + "/" + url.PathEscape(site)

	// Each search term is listed in full; the board is their union, each
	// posting once, under the same cap as a single listing (every posting
	// costs a detail request).
	terms := b.SearchTerms()
	if len(terms) == 0 {
		terms = []string{""}
	}
	var rows []listedJob
	paths := map[string]bool{}
	for _, term := range terms {
		got, err := s.list(ctx, base+"/jobs", term)
		if err != nil {
			return nil, fmt.Errorf("workday %q: search %q: %w", b.Token, term, err)
		}
		for _, j := range got {
			if !paths[j.ExternalPath] {
				paths[j.ExternalPath] = true
				rows = append(rows, j)
			}
		}
		if len(rows) > MaxPostings {
			return nil, fmt.Errorf("workday %q: %w: the board's searches together list more than %d postings; narrow them",
				b.Token, srcutil.ErrTooManyPostings, MaxPostings)
		}
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(rows))
	keys := make(map[posting.Key]bool, len(rows))
	for _, row := range rows {
		p, err := s.detail(ctx, base, host, tenant, site, row, b, seen)
		if err != nil {
			// A partial listing would make the reconciler age healthy postings
			// as absent, so one bad posting fails the whole board.
			return nil, fmt.Errorf("workday %q: posting %q: %w", b.Token, row.ExternalPath, err)
		}
		if keys[p.Key] {
			continue
		}
		keys[p.Key] = true
		out = append(out, p)
	}
	return out, nil
}

// list pages the listing endpoint for one search term. Workday reports total
// only on the first page (later pages say 0), so total is read once; an empty
// page always ends the loop, and the page count is bounded either way.
func (s *source) list(ctx context.Context, endpoint, term string) ([]listedJob, error) {
	var rows []listedJob
	paths := map[string]bool{}
	total, offset := 0, 0
	for page := 0; page < maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp listResponse
		req := listRequest{AppliedFacets: map[string]any{}, Limit: pageSize, Offset: offset, SearchText: term}
		if err := s.postJSON(ctx, endpoint, req, &resp); err != nil {
			return nil, err
		}
		// A search with no match still answers "jobPostings":[]; a reply
		// without the array is an error page or a new shape, never an empty board.
		if resp.JobPostings == nil {
			return nil, fmt.Errorf("listing at offset %d has no jobPostings array", offset)
		}
		if page == 0 {
			total = resp.Total
			if total > MaxPostings {
				return nil, fmt.Errorf("%w: board lists %d postings, over the %d limit; set the board's search to narrow it",
					srcutil.ErrTooManyPostings, total, MaxPostings)
			}
		}
		if len(resp.JobPostings) == 0 {
			return rows, nil
		}
		for _, j := range resp.JobPostings {
			// Paging shifts under a board that changes mid-listing; a repeat
			// is the same posting, not a second one.
			if paths[j.ExternalPath] {
				continue
			}
			paths[j.ExternalPath] = true
			rows = append(rows, j)
		}
		if len(rows) > MaxPostings {
			return nil, fmt.Errorf("%w: board lists more than %d postings; set the board's search to narrow it",
				srcutil.ErrTooManyPostings, MaxPostings)
		}
		offset += len(resp.JobPostings)
		if total > 0 && offset >= total {
			return rows, nil
		}
	}
	return nil, fmt.Errorf("listing did not end within %d pages", maxPages)
}

func (s *source) detail(ctx context.Context, base, host, tenant, site string, row listedJob, b sources.Board, seen time.Time) (posting.Posting, error) {
	path, err := escapePath(row.ExternalPath)
	if err != nil {
		return posting.Posting{}, err
	}
	var d detailResponse
	if err := srcutil.GetJSON(ctx, s.d, base+path, &d); err != nil {
		if errors.Is(err, srcutil.ErrBoardNotFound) {
			// The board exists (its listing answered); only this posting is gone.
			return posting.Posting{}, fmt.Errorf("detail is gone (HTTP 404)")
		}
		return posting.Posting{}, err
	}
	info := d.JobPostingInfo
	key, err := posting.WorkdayKey(tenant, site, info.JobReqID)
	if err != nil {
		return posting.Posting{}, err
	}
	title := strings.TrimSpace(info.Title)
	if title == "" {
		title = strings.TrimSpace(row.Title)
	}
	if title == "" {
		return posting.Posting{}, errors.New("empty title")
	}
	pubURL := info.ExternalURL
	if pubURL == "" {
		pubURL = "https://" + host + "/" + url.PathEscape(site) + path
	}
	if err := srcutil.CheckURL("externalUrl", pubURL); err != nil {
		return posting.Posting{}, err
	}
	location := strings.TrimSpace(info.Location)
	if location == "" {
		location = strings.TrimSpace(row.LocationsText)
	}
	company := strings.TrimSpace(b.Company)
	if company == "" {
		company = tenant
	}
	phrase := row.PostedOn
	if phrase == "" {
		phrase = info.PostedOn
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.Workday,
		Company:     company,
		Title:       title,
		Location:    location,
		Remote:      strings.EqualFold(strings.TrimSpace(info.RemoteType), "remote"),
		URL:         pubURL,
		PostedAt:    postedAt(info.StartDate, phrase, seen),
		Description: htmltext.Text(info.JobDescription, srcutil.MaxDescription),
		SeenAt:      seen,
	}, nil
}

// postedAt prefers the detail's startDate (an exact day). Otherwise it reads
// Workday's relative phrase against now. A phrase that is not an exact day
// count ("Posted 30+ Days Ago" is only a lower bound) yields the zero time:
// a guessed date would be worse than none.
func postedAt(startDate, phrase string, now time.Time) time.Time {
	if t, err := time.Parse(time.DateOnly, strings.TrimSpace(startDate)); err == nil {
		return t
	}
	day := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	f := strings.Fields(strings.ToLower(phrase))
	switch {
	case len(f) == 2 && f[0] == "posted" && f[1] == "today":
		return day
	case len(f) == 2 && f[0] == "posted" && f[1] == "yesterday":
		return day.AddDate(0, 0, -1)
	case len(f) == 4 && f[0] == "posted" && (f[2] == "days" || f[2] == "day") && f[3] == "ago":
		n, err := strconv.Atoi(f[1])
		if err != nil || n < 1 || n > 365 {
			return time.Time{}
		}
		return day.AddDate(0, 0, -n)
	}
	return time.Time{}
}

// postJSON POSTs the listing search. Workday wants Accept-Language; status,
// body-cap and retry rules are srcutil's, shared with every adapter's GET.
func (s *source) postJSON(ctx context.Context, endpoint string, body, v any) error {
	return srcutil.PostJSON(ctx, s.d, endpoint, body, http.Header{"Accept-Language": {"en-US"}}, v)
}

// parseToken splits "tenant/site" and refuses anything that could change the
// request path.
func parseToken(token string) (tenant, site string, err error) {
	tenant, site, ok := strings.Cut(strings.TrimSpace(token), "/")
	if !ok || !validName(tenant) || !validName(site) {
		return "", "", errors.New(`token must be "tenant/site"`)
	}
	return tenant, site, nil
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// checkHost requires a bare hostname whose first label is the tenant, so a
// board cannot send its requests to a host its token does not name.
func checkHost(host, tenant string) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "", errors.New("board host is empty")
	}
	for _, r := range host {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return "", fmt.Errorf("board host %q is not a bare hostname", host)
		}
	}
	first, _, _ := strings.Cut(host, ".")
	if first != strings.ToLower(tenant) {
		return "", fmt.Errorf("board host %q does not start with tenant %q", host, tenant)
	}
	return host, nil
}

// escapePath validates an externalPath from the (untrusted) listing and
// escapes each segment, so it can only extend the site path.
func escapePath(p string) (string, error) {
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("externalPath %q is not absolute", p)
	}
	segs := strings.Split(p[1:], "/")
	for i, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("externalPath %q has an invalid segment", p)
		}
		segs[i] = url.PathEscape(seg)
	}
	return "/" + strings.Join(segs, "/"), nil
}
