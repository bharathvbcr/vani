// Package eightfold lists postings from an Eightfold "PCSX" careers site,
// the system behind apply.careers.microsoft.com and careers.qualcomm.com
// (verified 2026-10-19; Netflix's explore.jobs.netflix.net answered 403).
//
//	GET https://{host}/api/pcsx/search?domain={domain}&query={term}&start={n}&num=10
//	GET https://{host}/api/pcsx/position_details?position_id={id}&domain={domain}&hl=en
//
// The board token is the company's domain and Host its careers site.
// Microsoft's robots.txt disallows everything but /careers and /api/pcsx
// (among a few others), which is exactly what this source reads. Search
// results carry no description, so each posting costs a details request;
// a term over the board cap fails the board rather than being truncated.
package eightfold

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
	// MaxPostings bounds one board: each posting costs a details request.
	MaxPostings = 200
	// pageSize is what the search returns at most per request (asking for
	// 100 still returned 10 on 2026-10-19).
	pageSize = 10
	maxPages = MaxPostings/pageSize + 2
)

// ErrShape is a response whose layout is not the one this source knows.
var ErrShape = errors.New("eightfold: unexpected response")

var (
	hostRE   = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)
	idRE     = regexp.MustCompile(`^[0-9]{1,20}$`)
	domainRE = hostRE
)

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the Eightfold source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (*source) ATS() posting.ATS { return posting.Eightfold }

type envelope struct {
	Status *int            `json:"status"`
	Error  json.RawMessage `json:"error"`
	Data   json.RawMessage `json:"data"`
}

type searchData struct {
	Count     *int       `json:"count"`
	Positions []position `json:"positions"`
}

type position struct {
	ID   json.Number `json:"id"`
	Name string      `json:"name"`
}

type details struct {
	ID                 json.Number `json:"id"`
	Name               string      `json:"name"`
	Locations          []string    `json:"locations"`
	PostedTs           *int64      `json:"postedTs"`
	Department         string      `json:"department"`
	JobDescription     string      `json:"jobDescription"`
	PublicURL          string      `json:"publicUrl"`
	WorkLocationOption string      `json:"workLocationOption"`
	RoleType           []string    `json:"efcustomTextRoletype"`
	EmploymentType     []string    `json:"efcustomTextEmploymentType"`
	WorkSite           []string    `json:"efcustomTextWorkSite"`
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := srcutil.CheckBoard(b, posting.Eightfold); err != nil {
		return nil, err
	}
	domain := strings.ToLower(strings.TrimSpace(b.Token))
	host := strings.ToLower(strings.TrimSpace(b.Host))
	if !domainRE.MatchString(domain) {
		return nil, fmt.Errorf("eightfold %q: token must be the company domain", b.Token)
	}
	if !hostRE.MatchString(host) {
		return nil, fmt.Errorf("eightfold %q: host %q is not a hostname", b.Token, b.Host)
	}
	terms := b.SearchTerms()
	if len(terms) == 0 {
		return nil, fmt.Errorf("eightfold %q: the board needs at least one search", b.Token)
	}
	if err := checkFilters(b.Filters); err != nil {
		return nil, fmt.Errorf("eightfold %q: %w", b.Token, err)
	}
	base := "https://" + host + "/api/pcsx/"
	var ids []string
	have := map[string]bool{}
	for _, term := range terms {
		got, err := s.search(ctx, base, domain, term, b.Filters)
		if err != nil {
			return nil, fmt.Errorf("eightfold %q: search %q: %w", b.Token, term, err)
		}
		for _, id := range got {
			if !have[id] {
				have[id] = true
				ids = append(ids, id)
			}
		}
		if len(ids) > MaxPostings {
			return nil, fmt.Errorf("eightfold %q: %w: the board's searches together list more than %d postings; narrow them",
				b.Token, srcutil.ErrTooManyPostings, MaxPostings)
		}
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(ids))
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p, err := s.detail(ctx, base, host, domain, id, b, seen)
		if err != nil {
			// A partial listing would age healthy postings as absent.
			return nil, fmt.Errorf("eightfold %q: position %s: %w", b.Token, id, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func checkFilters(f map[string][]string) error {
	allowed := sources.FilterKeys[posting.Eightfold]
	for k, vs := range f {
		if !slices.Contains(allowed, k) {
			return fmt.Errorf("filter %q is not one of %s", k, strings.Join(allowed, ", "))
		}
		if len(vs) == 0 {
			return fmt.Errorf("filter %q has no value", k)
		}
	}
	return nil
}

// get fetches one PCSX endpoint and returns its data, checking the envelope.
func (s *source) get(ctx context.Context, u string, v any) error {
	var env envelope
	if err := srcutil.GetJSON(ctx, s.d, u, &env); err != nil {
		return err
	}
	if env.Status == nil || *env.Status != 200 {
		return fmt.Errorf("%w: envelope status %v, error %s", ErrShape, env.Status, env.Error)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return fmt.Errorf("%w: no data", ErrShape)
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		return fmt.Errorf("%w: data: %v", ErrShape, err)
	}
	return nil
}

func searchURL(base, domain, term string, filters map[string][]string, start int) string {
	q := url.Values{}
	q.Set("domain", domain)
	q.Set("query", term)
	q.Set("start", strconv.Itoa(start))
	q.Set("num", strconv.Itoa(pageSize))
	for k, vs := range filters {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	return base + "search?" + q.Encode()
}

// search pages one term to its count; a count over the cap fails first.
func (s *source) search(ctx context.Context, base, domain, term string, filters map[string][]string) ([]string, error) {
	var ids []string
	have := map[string]bool{}
	count := -1
	for page := 0; page < maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var sd searchData
		if err := s.get(ctx, searchURL(base, domain, term, filters, len(ids)), &sd); err != nil {
			return nil, err
		}
		if sd.Count == nil || *sd.Count < 0 {
			return nil, fmt.Errorf("%w: no result count", ErrShape)
		}
		if count < 0 {
			count = *sd.Count
			if count > MaxPostings {
				return nil, fmt.Errorf("%w: search matches %d postings, over the %d limit; use a narrower term or the location filter",
					srcutil.ErrTooManyPostings, count, MaxPostings)
			}
		}
		added := 0
		for _, p := range sd.Positions {
			id := p.ID.String()
			if !idRE.MatchString(id) {
				return nil, fmt.Errorf("%w: position id %q", ErrShape, id)
			}
			if !have[id] {
				have[id] = true
				ids = append(ids, id)
				added++
			}
		}
		if len(ids) >= count {
			return ids, nil
		}
		if added == 0 {
			return nil, fmt.Errorf("%w: page at %d added nothing with %d of %d read", ErrShape, len(ids), len(ids), count)
		}
	}
	return nil, fmt.Errorf("%w: listing did not end within %d pages", ErrShape, maxPages)
}

func (s *source) detail(ctx context.Context, base, host, domain, id string, b sources.Board, seen time.Time) (posting.Posting, error) {
	q := url.Values{}
	q.Set("position_id", id)
	q.Set("domain", domain)
	q.Set("hl", "en")
	var d details
	if err := s.get(ctx, base+"position_details?"+q.Encode(), &d); err != nil {
		return posting.Posting{}, err
	}
	if d.ID.String() != id {
		return posting.Posting{}, fmt.Errorf("%w: details are for position %q", ErrShape, d.ID.String())
	}
	key, err := posting.EightfoldKey(domain, id)
	if err != nil {
		return posting.Posting{}, err
	}
	title := strings.TrimSpace(html.UnescapeString(d.Name))
	if title == "" {
		return posting.Posting{}, fmt.Errorf("%w: empty name", ErrShape)
	}
	// The posting URL must be the careers site's own page for this position.
	u := "https://" + host + "/careers/job/" + id
	if d.PublicURL != "" && d.PublicURL != u {
		return posting.Posting{}, fmt.Errorf("%w: publicUrl %q is not %s", ErrShape, d.PublicURL, u)
	}
	company := b.Company
	if company == "" {
		company = domain
	}
	p := posting.Posting{
		Key:         key,
		ATS:         posting.Eightfold,
		Company:     company,
		Title:       title,
		Location:    joinUnique(d.Locations),
		Department:  strings.TrimSpace(d.Department),
		Remote:      strings.EqualFold(d.WorkLocationOption, "remote"),
		URL:         u,
		Description: description(d),
		SeenAt:      seen,
	}
	if d.PostedTs != nil && *d.PostedTs > 0 {
		p.PostedAt = time.Unix(*d.PostedTs, 0).UTC()
	}
	return p, nil
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

// description is the job description as text, led by the tenant's role
// fields where it has them (Microsoft: role type, employment type, work site).
func description(d details) string {
	var flags []string
	for _, vs := range [][]string{d.RoleType, d.EmploymentType, d.WorkSite} {
		for _, v := range vs {
			if v = strings.TrimSpace(v); v != "" && !slices.Contains(flags, v) {
				flags = append(flags, v)
			}
		}
	}
	lead := ""
	if len(flags) > 0 {
		lead = "<p>Role: " + html.EscapeString(strings.Join(flags, ", ")) + "</p>"
	}
	return htmltext.Text(lead+d.JobDescription, srcutil.MaxDescription)
}
