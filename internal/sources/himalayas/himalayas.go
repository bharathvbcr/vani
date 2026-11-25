// Package himalayas lists postings from Himalayas' public remote-jobs search
// API, GET https://himalayas.app/jobs/api/search (documented at
// https://himalayas.app/docs/remote-jobs-api, read 2026-11-17). Himalayas is a
// job board, not an employer's ATS: one search lists many employers, so each
// posting's company is its row's own, and a row that names none is a bad row
// rather than one filed under the board's name.
//
// Terms, from the same page: the API is free to use without a key; a client
// shows Himalayas as the source and links back to himalayas.app; apply
// actions go to the row's applicationLink; the jobs are not passed on to other
// job boards, aggregators, Google Jobs or LinkedIn Jobs. Vani keeps the job's
// himalayas.app page as the posting URL and applicationLink as the apply URL,
// and republishes nothing. robots.txt (2026-11-17) disallows only /apply and
// paged HTML listings, not /jobs/api. The data refreshes once a day, so a
// daily run reads it as often as it changes.
//
// Search serves 20 rows a page, pages numbered from 1, with a totalCount; it
// has no cursor. Every row carries the full description, so there is no
// detail request. A board needs at least one search term (the unsearched feed
// is the whole remote market), and a board whose terms together match more
// than MaxPostings fails rather than being truncated: a truncated listing
// would let the reconciler age out the postings it did not reach. The
// country, seniority and employment_type filters narrow a term.
package himalayas

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	// Token is the one board token: the API is one site.
	Token     = "himalayas"
	site      = "https://himalayas.app"
	searchURL = site + "/jobs/api/search"
	// MaxPostings is the default board limit when Board.MaxPostings is zero,
	// the union of its terms. At 20 rows a page it is 50 requests a term; a
	// board may raise or lower it up to sources.MaxPostingsCeiling.
	MaxPostings = 1000
	// pageSize is the rows search serves a page; the API does not take a
	// limit on search (its own note, 2026-11-17).
	pageSize = 20
	// worldwide is the location of a row with no location restrictions,
	// which the docs define as open worldwide.
	worldwide = "Worldwide"
	// paceEvery spaces Himalayas search requests below its ~35 req/min burst
	// limit observed live (2026-11-24: 38 calls at 1/s hit HTTP 429 Retry-After 36s).
	paceEvery  = 2 * time.Second
	paceBudget = 45 * time.Minute
)

// capFor is the board's listing limit: its max_postings, or MaxPostings.
func capFor(b sources.Board) (int, error) {
	switch {
	case b.MaxPostings == 0:
		return MaxPostings, nil
	case b.MaxPostings < 1 || b.MaxPostings > sources.MaxPostingsCeiling:
		return 0, fmt.Errorf("max_postings %d outside 1..%d", b.MaxPostings, sources.MaxPostingsCeiling)
	}
	return b.MaxPostings, nil
}

// ErrShape is a response whose layout is not the one this source knows.
var ErrShape = errors.New("himalayas: unexpected search response")

// slug is one path segment of a job page: lower-case letters, digits and
// '-', as every guid seen on 2026-11-17 was.
const slug = `[a-z0-9](?:[a-z0-9-]{0,298}[a-z0-9])?`

// guidRE is a job's page, the guid every row carries.
var guidRE = regexp.MustCompile(`^https://himalayas\.app/companies/(` + slug + `)/jobs/(` + slug + `)$`)

type source struct {
	d    egress.Doer
	now  func() time.Time
	wait func(ctx context.Context, d time.Duration) error
}

// New returns the Himalayas source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now, wait: srcutil.Sleep} }

func (*source) ATS() posting.ATS { return posting.Himalayas }

type response struct {
	TotalCount *int  `json:"totalCount"`
	Jobs       []job `json:"jobs"`
}

type job struct {
	Title                string   `json:"title"`
	CompanyName          string   `json:"companyName"`
	CompanySlug          string   `json:"companySlug"`
	EmploymentType       string   `json:"employmentType"`
	MinSalary            *float64 `json:"minSalary"`
	MaxSalary            *float64 `json:"maxSalary"`
	SalaryPeriod         string   `json:"salaryPeriod"`
	Currency             string   `json:"currency"`
	Seniority            []string `json:"seniority"`
	LocationRestrictions []string `json:"locationRestrictions"`
	ParentCategories     []string `json:"parentCategories"`
	PubDate              *int64   `json:"pubDate"`
	ExpiryDate           *int64   `json:"expiryDate"`
	Description          string   `json:"description"`
	ApplicationLink      string   `json:"applicationLink"`
	GUID                 string   `json:"guid"`
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckBoard(b, posting.Himalayas); err != nil {
		return nil, err
	}
	if b.Token != Token {
		return nil, fmt.Errorf("himalayas: board token %q is not %q", b.Token, Token)
	}
	terms := b.SearchTerms()
	if len(terms) == 0 {
		return nil, fmt.Errorf("himalayas %q: the board needs at least one search", b.Token)
	}
	if err := checkFilters(b.Filters); err != nil {
		return nil, fmt.Errorf("himalayas %q: %w", b.Token, err)
	}
	limit, err := capFor(b)
	if err != nil {
		return nil, fmt.Errorf("himalayas %q: %w", b.Token, err)
	}
	seen := s.now()
	pace := &srcutil.Pacer{Every: paceEvery, Budget: paceBudget, Sleep: s.wait}
	var out []posting.Posting
	keys := map[posting.Key]bool{}
	rows := srcutil.NewRows(ctx, b, 0)
	for _, term := range terms {
		jobs, err := s.search(ctx, pace, term, b.Filters, limit)
		if err != nil {
			return nil, fmt.Errorf("himalayas %q: search %q: %w", b.Token, term, err)
		}
		rows.Add(len(jobs))
		for _, j := range jobs {
			p, keep, err := j.posting(b, seen)
			if err != nil {
				if ferr := rows.Bad(err); ferr != nil {
					return nil, fmt.Errorf("himalayas %q: search %q: job %q: %w", b.Token, term, j.GUID, ferr)
				}
				continue
			}
			if keep && !keys[p.Key] {
				keys[p.Key] = true
				out = append(out, p)
			}
		}
		if len(out) > limit {
			return nil, fmt.Errorf("himalayas %q: %w: the board's searches together list more than %d postings; narrow them",
				b.Token, srcutil.ErrTooManyPostings, limit)
		}
	}
	if err := rows.Done(); err != nil {
		return nil, fmt.Errorf("himalayas %q: %w", b.Token, err)
	}
	return out, nil
}

// checkFilters is sources.CheckFilters plus the one-value rule: the API reads
// one value of each filter, so a second would be dropped, not added.
func checkFilters(f map[string][]string) error {
	if err := sources.CheckFilters(posting.Himalayas, f); err != nil {
		return err
	}
	for k, vs := range f {
		if len(vs) != 1 || strings.TrimSpace(vs[0]) == "" {
			return fmt.Errorf("filter %q takes exactly one value; the API does not combine values", k)
		}
	}
	return nil
}

func pageURL(term string, filters map[string][]string, page int) string {
	q := url.Values{}
	q.Set("q", term)
	q.Set("page", strconv.Itoa(page))
	for k, vs := range filters {
		q.Set(k, strings.TrimSpace(vs[0]))
	}
	return searchURL + "?" + q.Encode()
}

// search pages one term to its total. A total over limit fails before
// any further page; terms that overlap are judged on their union by List.
// Each page's totalCount is the
// latest word on the size of a listing that changes while it is read: rows
// shifted onto a page already read are dropped as repeats, and a page that
// adds nothing while the total is still unmet is ErrShape.
func (s *source) search(ctx context.Context, pace *srcutil.Pacer, term string, filters map[string][]string, limit int) ([]job, error) {
	var out []job
	ids := map[string]bool{}
	maxPages := limit/pageSize + 2
	for page := 1; page <= maxPages; page++ {
		if err := pace.Wait(ctx); err != nil {
			return nil, err
		}
		var resp response
		if err := srcutil.GetJSON(ctx, s.d, pageURL(term, filters, page), &resp); err != nil {
			return nil, err
		}
		if resp.TotalCount == nil || *resp.TotalCount < 0 || resp.Jobs == nil {
			return nil, fmt.Errorf("%w: page %d has no totalCount or jobs list", ErrShape, page)
		}
		total := *resp.TotalCount
		if total > limit {
			return nil, fmt.Errorf("%w: search matches %d postings, over the %d limit; use a narrower term or the country, seniority and employment_type filters",
				srcutil.ErrTooManyPostings, total, limit)
		}
		added := 0
		for _, j := range resp.Jobs {
			if ids[j.GUID] {
				continue // paging shifted under a changing listing
			}
			ids[j.GUID] = true
			out = append(out, j)
			added++
		}
		if page == 1 {
			// A page cannot list more jobs than the search matches.
			if err := srcutil.TotalCoversRows(total, len(out)); err != nil {
				return nil, fmt.Errorf("%w: %w", ErrShape, err)
			}
		}
		if len(out) >= total {
			return out, nil
		}
		if added == 0 {
			return nil, fmt.Errorf("%w: page %d added nothing with %d of %d read", ErrShape, page, len(out), total)
		}
	}
	return nil, fmt.Errorf("%w: listing did not end within %d pages", ErrShape, maxPages)
}

// posting is the row as a posting. keep is false for a row whose expiryDate
// has passed: Himalayas has closed it, so it is absent like any closed job.
func (j job) posting(b sources.Board, seen time.Time) (posting.Posting, bool, error) {
	guid := strings.TrimSpace(j.GUID)
	m := guidRE.FindStringSubmatch(guid)
	if m == nil {
		return posting.Posting{}, false, fmt.Errorf("%w: guid %q is not a himalayas.app job page", ErrShape, guid)
	}
	if cs := strings.TrimSpace(j.CompanySlug); cs != "" && cs != m[1] {
		return posting.Posting{}, false, fmt.Errorf("%w: companySlug %q is not the guid's %q", ErrShape, cs, m[1])
	}
	key, err := posting.HimalayasKey(b.Token, m[1], m[2])
	if err != nil {
		return posting.Posting{}, false, err
	}
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, false, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := srcutil.Title(j.Title)
	if title == "" {
		return posting.Posting{}, false, fmt.Errorf("%w: empty title", ErrShape)
	}
	company := strings.Join(strings.Fields(j.CompanyName), " ")
	if company == "" {
		return posting.Posting{}, false, fmt.Errorf("%w: empty companyName", ErrShape)
	}
	if j.ExpiryDate != nil && *j.ExpiryDate > 0 && time.Unix(*j.ExpiryDate, 0).Before(seen) {
		return posting.Posting{}, false, nil
	}
	apply := strings.TrimSpace(j.ApplicationLink)
	if err := srcutil.CheckURL("applicationLink", apply); err != nil {
		return posting.Posting{}, false, err
	}
	if apply == "" {
		// The terms send applicants through Himalayas; its job page does.
		apply = guid
	}
	var posted time.Time
	if j.PubDate != nil && *j.PubDate > 0 {
		posted = time.Unix(*j.PubDate, 0).UTC()
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.Himalayas,
		Company:     company,
		Title:       title,
		Location:    j.where(),
		Remote:      true,
		Department:  srcutil.JoinDistinct(", ", j.ParentCategories...),
		URL:         guid,
		ApplyURL:    apply,
		PostedAt:    posted,
		Description: j.description(),
		SeenAt:      seen,
	}, true, nil
}

// where is the countries a remote hire may live in, or worldwide for a row
// that restricts none.
func (j job) where() string {
	if loc := srcutil.JoinDistinct("; ", j.LocationRestrictions...); loc != "" {
		return loc
	}
	return worldwide
}

// description is the row's text, led by a pay line when it names a usable
// range and a role line of its employment type and seniority. Every Himalayas
// job is remote, which the role line says too.
func (j job) description() string {
	flags := append([]string{srcutil.EmploymentType(j.EmploymentType)}, j.Seniority...)
	flags = append(flags, "remote")
	lead, room := srcutil.Lead(srcutil.PayLine(j.Currency, j.MinSalary, j.MaxSalary, j.SalaryPeriod), srcutil.RoleLine(flags...))
	return lead + srcutil.Sections(room, srcutil.Section{HTML: j.Description})
}
