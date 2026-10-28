// Package atlassian lists Atlassian's postings from its own careers endpoint,
// GET https://www.atlassian.com/endpoint/careers/listings: one unauthenticated
// request that returns every posting as a JSON array (372 rows, 353 distinct
// jobs, about 2.5 MB on 2026-10-27), each with its overview, responsibilities
// and qualifications. A listing therefore costs one request and no detail
// requests. The payload names no atlassian.com job URL, only the iCIMS portal
// page of each job (portalJobPost.portalUrl), which is the posting's URL.
// robots.txt does not disallow /endpoint (checked 2026-10-27).
//
// The same job can be listed more than once (19 repeated rows on 2026-10-27,
// all identical to their first listing); identical repeats are one posting, and two
// rows that disagree about one job fail the board. An empty array is an error:
// the reconciler would age out every live posting.
package atlassian

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
	listURL = "https://www.atlassian.com/endpoint/careers/listings"
	// MaxPostings bounds one listing; 372 rows were seen on 2026-10-27. A
	// listing over it fails rather than being cut.
	MaxPostings = 3000
	// maxBody bounds the response (about 2.5 MB on 2026-10-27); srcutil.MaxBody
	// is far larger.
	maxBody = 16 << 20
)

// ErrShape is a response whose layout is not the one this source knows.
var ErrShape = errors.New("atlassian: unexpected careers listing")

var portalPathRE = regexp.MustCompile(`^/jobs/([0-9]{1,15})/[A-Za-z0-9%._~()-]{0,300}/job$`)

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the Atlassian careers source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (*source) ATS() posting.ATS { return posting.Atlassian }

type job struct {
	ID               *int64   `json:"id"`
	Title            string   `json:"title"`
	Type             *string  `json:"type"`
	Category         string   `json:"category"`
	Locations        []string `json:"locations"`
	Overview         string   `json:"overview"`
	Responsibilities string   `json:"responsibilities"`
	Qualifications   string   `json:"qualifications"`
	ApplyURL         string   `json:"applyUrl"`
	PortalJobPost    struct {
		ID        *int64 `json:"id"`
		PortalURL string `json:"portalUrl"`
	} `json:"portalJobPost"`
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := srcutil.CheckBoard(b, posting.Atlassian); err != nil {
		return nil, err
	}
	if len(b.SearchTerms()) > 0 {
		return nil, fmt.Errorf("atlassian %q: the listing is one request; searches do not apply", b.Token)
	}
	if err := sources.CheckFilters(posting.Atlassian, b.Filters); err != nil {
		return nil, fmt.Errorf("atlassian %q: %w", b.Token, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var jobs []job
	if err := srcutil.GetJSONLimit(ctx, s.d, listURL, maxBody, &jobs); err != nil {
		return nil, fmt.Errorf("atlassian %q: %w", b.Token, err)
	}
	if jobs == nil {
		return nil, fmt.Errorf("atlassian %q: %w: the listing is not an array", b.Token, ErrShape)
	}
	if len(jobs) == 0 {
		return nil, fmt.Errorf("atlassian %q: %w: the listing is empty", b.Token, ErrShape)
	}
	if len(jobs) > MaxPostings {
		return nil, fmt.Errorf("atlassian %q: %w: %d rows, over %d", b.Token, srcutil.ErrTooManyPostings, len(jobs), MaxPostings)
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(jobs))
	byKey := map[posting.Key]posting.Posting{}
	for i, j := range jobs {
		p, err := j.posting(b, seen)
		if err != nil {
			return nil, fmt.Errorf("atlassian %q: row %d: %w", b.Token, i, err)
		}
		if prev, dup := byKey[p.Key]; dup {
			if prev != p {
				return nil, fmt.Errorf("atlassian %q: %w: job %s is listed twice with different content", b.Token, ErrShape, p.Key)
			}
			continue
		}
		byKey[p.Key] = p
		out = append(out, p)
	}
	return out, nil
}

func (j job) posting(b sources.Board, seen time.Time) (posting.Posting, error) {
	if j.ID == nil || *j.ID <= 0 {
		return posting.Posting{}, fmt.Errorf("%w: missing or bad id", ErrShape)
	}
	id := strconv.FormatInt(*j.ID, 10)
	if j.PortalJobPost.ID != nil && *j.PortalJobPost.ID != *j.ID {
		return posting.Posting{}, fmt.Errorf("%w: id %s and portalJobPost.id %d differ", ErrShape, id, *j.PortalJobPost.ID)
	}
	portal, err := portalURL(j.PortalJobPost.PortalURL, id)
	if err != nil {
		return posting.Posting{}, fmt.Errorf("job %s: %w", id, err)
	}
	key, err := posting.AtlassianKey(b.Token, id)
	if err != nil {
		return posting.Posting{}, err
	}
	title := srcutil.Title(j.Title)
	if title == "" {
		return posting.Posting{}, fmt.Errorf("job %s: %w: empty title", id, ErrShape)
	}
	company := b.Company
	if company == "" {
		company = "Atlassian"
	}
	p := posting.Posting{
		Key:         key,
		ATS:         posting.Atlassian,
		Company:     company,
		Title:       title,
		Location:    location(j.Locations),
		Remote:      remote(j.Locations),
		Department:  strings.TrimSpace(j.Category),
		URL:         portal,
		Description: description(j),
		SeenAt:      seen,
	}
	// The apply link is the portal page with ?mode=apply (372 of 372); any
	// other shape is dropped, not followed.
	if a, err := url.Parse(j.ApplyURL); err == nil && j.ApplyURL != "" {
		if u, _ := url.Parse(portal); u != nil && a.Scheme == "https" && a.Host == u.Host && a.Path == u.Path && a.RawQuery == "mode=apply" && a.User == nil && a.Fragment == "" {
			p.ApplyURL = j.ApplyURL
		}
	}
	return p, nil
}

// portalURL checks raw is the iCIMS page of job id: https, an icims.com host,
// /jobs/{id}/{slug}/job, no query, fragment or userinfo.
func portalURL(raw, id string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || raw == "" {
		return "", fmt.Errorf("%w: portalUrl %q", ErrShape, raw)
	}
	host := strings.ToLower(u.Hostname())
	m := portalPathRE.FindStringSubmatch(u.EscapedPath())
	if u.Scheme != "https" || u.Port() != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(host != "icims.com" && !strings.HasSuffix(host, ".icims.com")) || m == nil || m[1] != id {
		return "", fmt.Errorf("%w: portalUrl %q is not the iCIMS page of job %s", ErrShape, raw, id)
	}
	return raw, nil
}

// fields splits one location string, "Austin - United States -   Austin, Texas
// 78702 United States", into its " - " fields with whitespace collapsed.
func fields(loc string) []string {
	var out []string
	for _, f := range strings.Split(strings.Join(strings.Fields(loc), " "), " - ") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

var usZipRE = regexp.MustCompile(`(?:^|\s+)[0-9]{5}(-[0-9]{4})?$`)

// place is one location string as one place. The first two fields are the
// office and its country or "Remote", joined with repeats dropped
// ("Sydney, Australia", "Remote, India", "Manila, Remote", "Canada"). The
// third is the full address, "Austin, Texas 78702 United States"; for the
// United States it is kept without its ZIP code and country ("Austin, Texas"),
// since the state is what the office list otherwise lacks. Postal codes of
// other countries are left out with the rest of the address.
func place(loc string) string {
	f := fields(loc)
	if len(f) == 3 && strings.HasSuffix(f[2], " United States") {
		s := strings.TrimSuffix(f[2], " United States")
		if s = strings.Trim(usZipRE.ReplaceAllString(s, ""), ", "); s != "" {
			return s
		}
	}
	var out []string
	for _, x := range f[:min(len(f), 2)] {
		if !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, x) }) {
			out = append(out, x)
		}
	}
	return strings.Join(out, ", ")
}

func location(locs []string) string {
	var out []string
	for _, l := range locs {
		if p := place(l); p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return strings.Join(out, "; ")
}

// remote is whether any location says the job can be done remotely: a field
// that is "Remote" ("Remote - India - Remote", "Manila - Remote") or that ends
// "or Remote" ("United States or Remote").
func remote(locs []string) bool {
	for _, l := range locs {
		for _, f := range fields(l) {
			if strings.EqualFold(f, "Remote") || strings.HasSuffix(strings.ToLower(f), " or remote") {
				return true
			}
		}
	}
	return false
}

// description is the posting's sections as text, led by a srcutil.RoleLine of
// the board's employment type when it names one (89 of 372 rows do).
func description(j job) string {
	var flags []string
	if j.Type != nil {
		flags = append(flags, srcutil.EmploymentType(*j.Type))
	}
	lead, room := srcutil.Lead(srcutil.RoleLine(flags...))
	var parts []string
	for _, sec := range []struct{ head, body string }{
		{"Overview", j.Overview},
		{"Responsibilities", j.Responsibilities},
		{"Qualifications", j.Qualifications},
	} {
		if strings.TrimSpace(sec.body) != "" {
			parts = append(parts, "<h3>"+sec.head+"</h3>"+sec.body)
		}
	}
	return lead + htmltext.Text(strings.Join(parts, ""), room)
}
