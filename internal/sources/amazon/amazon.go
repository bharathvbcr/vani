// Package amazon lists postings from amazon.jobs's own search API,
// GET https://www.amazon.jobs/en/search.json. Each result carries the full
// description and qualifications, so a listing costs one request per page of
// results and no detail requests. robots.txt disallows only /internal paths
// (checked 2026-10-19).
//
// A term matching more postings than the board cap fails the board rather
// than being truncated; the normalized_country_code[] and category[] filters
// and specific terms narrow it.
package amazon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
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
	site      = "https://www.amazon.jobs"
	searchURL = site + "/en/search.json"
	// MaxPostings bounds one board (the union of its terms).
	MaxPostings = 500
	// pageSize is the result_limit asked for; 100 was verified on 2026-10-19.
	pageSize = 100
	maxPages = MaxPostings/pageSize + 2
)

// ErrShape is a response whose layout is not the one this source knows.
var ErrShape = errors.New("amazon: unexpected search response")

var (
	idRE   = regexp.MustCompile(`^[0-9]{1,15}$`)
	pathRE = regexp.MustCompile(`^/en/jobs/([0-9]{1,15})/[a-z0-9-]{1,300}$`)
)

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the Amazon jobs source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (*source) ATS() posting.ATS { return posting.Amazon }

type response struct {
	Error *string `json:"error"`
	Hits  *int    `json:"hits"`
	Jobs  []job   `json:"jobs"`
}

type job struct {
	IDIcims                 string   `json:"id_icims"`
	Title                   string   `json:"title"`
	CompanyName             string   `json:"company_name"`
	JobPath                 string   `json:"job_path"`
	URLNextStep             string   `json:"url_next_step"`
	PostedDate              string   `json:"posted_date"`
	NormalizedLocation      string   `json:"normalized_location"`
	Locations               []string `json:"locations"` // each a JSON object, encoded as a string
	JobCategory             string   `json:"job_category"`
	Description             string   `json:"description"`
	BasicQualifications     string   `json:"basic_qualifications"`
	PreferredQualifications string   `json:"preferred_qualifications"`
	IsIntern                *bool    `json:"is_intern"`
	IsManager               *bool    `json:"is_manager"`
	JobScheduleType         string   `json:"job_schedule_type"`
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := srcutil.CheckBoard(b, posting.Amazon); err != nil {
		return nil, err
	}
	terms := b.SearchTerms()
	if len(terms) == 0 {
		return nil, fmt.Errorf("amazon %q: the board needs at least one search", b.Token)
	}
	if err := sources.CheckFilters(posting.Amazon, b.Filters); err != nil {
		return nil, fmt.Errorf("amazon %q: %w", b.Token, err)
	}
	seen := s.now()
	var out []posting.Posting
	keys := map[posting.Key]bool{}
	for _, term := range terms {
		jobs, err := s.search(ctx, term, b.Filters)
		if err != nil {
			return nil, fmt.Errorf("amazon %q: search %q: %w", b.Token, term, err)
		}
		for _, j := range jobs {
			p, err := j.posting(b, seen)
			if err != nil {
				return nil, fmt.Errorf("amazon %q: search %q: job %q: %w", b.Token, term, j.IDIcims, err)
			}
			if !keys[p.Key] {
				keys[p.Key] = true
				out = append(out, p)
			}
		}
		if len(out) > MaxPostings {
			return nil, fmt.Errorf("amazon %q: %w: the board's searches together list more than %d postings; narrow them",
				b.Token, srcutil.ErrTooManyPostings, MaxPostings)
		}
	}
	return out, nil
}

func pageURL(term string, filters map[string][]string, offset int) string {
	q := url.Values{}
	q.Set("base_query", term)
	q.Set("offset", strconv.Itoa(offset))
	q.Set("result_limit", strconv.Itoa(pageSize))
	for k, vs := range filters {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	return searchURL + "?" + q.Encode()
}

// search pages one term to its hit count. hits over the cap fails before any
// paging; a page that adds nothing before the count is reached is ErrShape.
func (s *source) search(ctx context.Context, term string, filters map[string][]string) ([]job, error) {
	var out []job
	ids := map[string]bool{}
	hits := -1
	for page := 0; page < maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp response
		if err := srcutil.GetJSON(ctx, s.d, pageURL(term, filters, len(out)), &resp); err != nil {
			return nil, err
		}
		if resp.Error != nil && strings.TrimSpace(*resp.Error) != "" {
			return nil, fmt.Errorf("%w: error %q", ErrShape, *resp.Error)
		}
		if resp.Hits == nil || *resp.Hits < 0 || resp.Jobs == nil && *resp.Hits > 0 {
			return nil, fmt.Errorf("%w: no hits count or jobs list", ErrShape)
		}
		if hits < 0 {
			hits = *resp.Hits
			if hits > MaxPostings {
				return nil, fmt.Errorf("%w: search matches %d postings, over the %d limit; use a narrower term or the normalized_country_code[]/category[] filters",
					srcutil.ErrTooManyPostings, hits, MaxPostings)
			}
		}
		added := 0
		for _, j := range resp.Jobs {
			if ids[j.IDIcims] {
				continue // paging shifted under a changing listing
			}
			ids[j.IDIcims] = true
			out = append(out, j)
			added++
		}
		if len(out) >= hits {
			return out, nil
		}
		if added == 0 {
			return nil, fmt.Errorf("%w: page at offset %d added nothing with %d of %d read", ErrShape, len(out), len(out), hits)
		}
	}
	return nil, fmt.Errorf("%w: listing did not end within %d pages", ErrShape, maxPages)
}

func (j job) posting(b sources.Board, seen time.Time) (posting.Posting, error) {
	if !idRE.MatchString(j.IDIcims) {
		return posting.Posting{}, fmt.Errorf("%w: id_icims %q", ErrShape, j.IDIcims)
	}
	m := pathRE.FindStringSubmatch(j.JobPath)
	if m == nil || m[1] != j.IDIcims {
		return posting.Posting{}, fmt.Errorf("%w: job_path %q does not name job %s", ErrShape, j.JobPath, j.IDIcims)
	}
	key, err := posting.AmazonKey(b.Token, j.IDIcims)
	if err != nil {
		return posting.Posting{}, err
	}
	title := strings.TrimSpace(html.UnescapeString(j.Title))
	if title == "" {
		return posting.Posting{}, fmt.Errorf("%w: empty title", ErrShape)
	}
	var posted time.Time
	if s := strings.TrimSpace(j.PostedDate); s != "" {
		if posted, err = time.Parse("January 2, 2006", s); err != nil {
			return posting.Posting{}, fmt.Errorf("%w: posted_date %q", ErrShape, s)
		}
	}
	company := b.Company
	if company == "" {
		company = "Amazon"
	}
	p := posting.Posting{
		Key:         key,
		ATS:         posting.Amazon,
		Company:     company,
		Title:       title,
		Location:    location(j),
		Department:  strings.TrimSpace(j.JobCategory),
		URL:         site + j.JobPath,
		PostedAt:    posted,
		Description: description(j),
		SeenAt:      seen,
	}
	if err := srcutil.CheckURL("url_next_step", j.URLNextStep); err == nil && strings.HasPrefix(j.URLNextStep, "https://") {
		p.ApplyURL = j.URLNextStep
	}
	p.Remote = remote(j)
	return p, nil
}

type place struct {
	NormalizedLocation string `json:"normalizedLocation"`
	Type               string `json:"type"`
}

func places(j job) []place {
	var out []place
	for _, raw := range j.Locations {
		var pl place
		if json.Unmarshal([]byte(raw), &pl) == nil {
			out = append(out, pl)
		}
	}
	return out
}

// location joins every normalized location, falling back to the primary one.
func location(j job) string {
	var locs []string
	for _, pl := range places(j) {
		if s := strings.TrimSpace(pl.NormalizedLocation); s != "" && !slices.Contains(locs, s) {
			locs = append(locs, s)
		}
	}
	if len(locs) == 0 {
		return strings.TrimSpace(j.NormalizedLocation)
	}
	return strings.Join(locs, "; ")
}

func remote(j job) bool {
	for _, pl := range places(j) {
		if strings.EqualFold(pl.Type, "REMOTE") || strings.EqualFold(pl.Type, "VIRTUAL") {
			return true
		}
	}
	return false
}

// description is the posting's sections as text, led by Amazon's own role
// flags (intern, manager, schedule) because seniority decides fit.
func description(j job) string {
	var flags []string
	if j.IsIntern != nil && *j.IsIntern {
		flags = append(flags, "internship")
	}
	if j.IsManager != nil && *j.IsManager {
		flags = append(flags, "people manager")
	}
	if s := strings.TrimSpace(j.JobScheduleType); s != "" {
		flags = append(flags, s)
	}
	var parts []string
	if len(flags) > 0 {
		parts = append(parts, "<p>Role: "+html.EscapeString(strings.Join(flags, ", "))+"</p>")
	}
	for _, sec := range []struct{ head, body string }{
		{"Description", j.Description},
		{"Basic Qualifications", j.BasicQualifications},
		{"Preferred Qualifications", j.PreferredQualifications},
	} {
		if strings.TrimSpace(sec.body) != "" {
			parts = append(parts, "<h3>"+sec.head+"</h3>"+sec.body)
		}
	}
	return htmltext.Text(strings.Join(parts, ""), srcutil.MaxDescription)
}
