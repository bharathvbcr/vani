// Package teamtailor lists postings from a Teamtailor board
// (GET https://{token}.teamtailor.com/jobs.json).
//
// The answer is a JSON Feed 1.1. Polestar (GET 2026-11-09) answered 200
// application/feed+json with the whole board and next_url null. A short page
// (jobs.json?per_page=5) set next_url to the same host, path /jobs.json, and
// query page and per_page only; the following page had different ids. This
// adapter does not send per_page itself. It follows next_url only while that
// shape holds, and a board that does not finish within the page cap or the
// posting cap fails whole: a partial list would age the jobs that were not
// read. An empty feed with no next_url is a board of zero postings.
//
// robots.txt of polestar.teamtailor.com (GET 2026-11-09) disallows /app/,
// /messages/, /messenger/, /facebook/tab/ and /jobs/internal/. /jobs.json is
// not disallowed. The file also carries "Content-Signal: search=no,
// ai-train=no, ai-input=no", which is not a robots Disallow; Vani reads the
// public listing and does not train on it. aihitdata is disallowed entirely.
//
// Each item's id (a UUID on the Polestar board) is the key. The numeric
// identifier.value is the id in the public /jobs/{id}-slug URL, not the key.
// The description is _jobposting.description when it is present, otherwise
// content_html; the two repeat each other.
package teamtailor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// ErrBoardNotFound is returned when Teamtailor has no such board.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

// maxFeedPages is the most pages one board may take: a legal board of
// srcutil.MaxPostings at the default 100 jobs a page, with slack. A further
// next_url after that fails the board rather than cutting it.
const maxFeedPages = srcutil.MaxPostings/100 + 10

// maxPageURL bounds a next_url before it is fetched.
const maxPageURL = 2048

const feedVersionPrefix = "https://jsonfeed.org/version/1"

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the Teamtailor source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (*source) ATS() posting.ATS { return posting.Teamtailor }

type feed struct {
	Version string  `json:"version"`
	Title   string  `json:"title"`
	Items   *[]item `json:"items"`
	NextURL string  `json:"next_url"`
}

type item struct {
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	URL           string     `json:"url"`
	ContentHTML   string     `json:"content_html"`
	DatePublished string     `json:"date_published"`
	Job           jobPosting `json:"_jobposting"`
}

type jobPosting struct {
	DatePosted  string     `json:"datePosted"`
	Description string     `json:"description"`
	Employment  employment `json:"employmentType"`
	Hiring      org        `json:"hiringOrganization"`
	Locations   places     `json:"jobLocation"`
}

type org struct {
	Name   string `json:"name"`
	SameAs string `json:"sameAs"`
}

// employment is schema.org employmentType: a string ("FULL_TIME" on the
// Polestar board, 2026-11-09) or an array of strings.
type employment []string

func (e *employment) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		return nil
	}
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		if strings.TrimSpace(s) != "" {
			*e = employment{s}
		}
		return nil
	}
	if len(raw) > 0 && raw[0] == '[' {
		var many []string
		if err := json.Unmarshal(raw, &many); err != nil {
			return fmt.Errorf("employmentType: %w", err)
		}
		*e = many
		return nil
	}
	return errors.New("employmentType is not a string or an array")
}

// places is schema.org jobLocation: one Place, an array of them, or a string.
type places []place

func (p *places) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		return nil
	}
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		if name := strings.TrimSpace(s); name != "" {
			*p = places{{Name: name}}
		}
		return nil
	}
	if len(raw) > 0 && raw[0] == '{' {
		var one place
		if err := json.Unmarshal(raw, &one); err != nil {
			return fmt.Errorf("jobLocation: %w", err)
		}
		*p = places{one}
		return nil
	}
	if len(raw) > 0 && raw[0] == '[' {
		var many []place
		if err := json.Unmarshal(raw, &many); err != nil {
			return fmt.Errorf("jobLocation: %w", err)
		}
		*p = many
		return nil
	}
	return errors.New("jobLocation is not a place, an array, or a string")
}

type place struct {
	Name    string   `json:"name"`
	Address *address `json:"address"`
}

type address struct {
	Locality string `json:"addressLocality"`
	Region   string `json:"addressRegion"`
}

func boardURL(token string) string { return "https://" + token + ".teamtailor.com/jobs.json" }

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckTenant(b, posting.Teamtailor); err != nil {
		return nil, err
	}
	next := boardURL(b.Token)
	seen := map[string]bool{}
	out := make([]posting.Posting, 0)
	keys := map[posting.Key]int{}
	rows := srcutil.NewRows(ctx, b, 0)
	for page := 1; page <= maxFeedPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		canon, err := pageURL(b.Token, next)
		if err != nil {
			return nil, fmt.Errorf("teamtailor %q: %w", b.Token, err)
		}
		if seen[canon] {
			return nil, fmt.Errorf("teamtailor %q: next_url repeats a page already read", b.Token)
		}
		seen[canon] = true
		var doc feed
		if err := srcutil.GetJSON(ctx, s.d, next, &doc); err != nil {
			return nil, fmt.Errorf("teamtailor %q: %w", b.Token, err)
		}
		if !strings.HasPrefix(strings.TrimSpace(doc.Version), feedVersionPrefix) {
			return nil, fmt.Errorf("teamtailor %q: not a JSON Feed", b.Token)
		}
		if doc.Items == nil {
			return nil, fmt.Errorf("teamtailor %q: payload has no items array", b.Token)
		}
		items := *doc.Items
		if err := srcutil.CheckCount(len(items)); err != nil {
			return nil, fmt.Errorf("teamtailor %q: %w", b.Token, err)
		}
		if err := srcutil.CheckCount(len(out) + rows.Skipped() + len(items)); err != nil {
			return nil, fmt.Errorf("teamtailor %q: %w", b.Token, err)
		}
		rows.Add(len(items))
		now := s.now()
		for i, it := range items {
			p, err := it.posting(b, doc.Title, now)
			if err != nil {
				if ferr := rows.Bad(err); ferr != nil {
					return nil, fmt.Errorf("teamtailor %q: page %d item %d: %w", b.Token, page, i, ferr)
				}
				continue
			}
			if prev, dup := keys[p.Key]; dup {
				return nil, fmt.Errorf("teamtailor %q: items %d and %d share key %s", b.Token, prev, len(keys), p.Key)
			}
			keys[p.Key] = len(keys)
			out = append(out, p)
		}
		next = strings.TrimSpace(doc.NextURL)
		if next == "" {
			if err := rows.Done(); err != nil {
				return nil, fmt.Errorf("teamtailor %q: %w", b.Token, err)
			}
			return out, nil
		}
		if len(out) >= srcutil.MaxPostings {
			return nil, fmt.Errorf("teamtailor %q: %w", b.Token, srcutil.ErrTooManyPostings)
		}
		if page == maxFeedPages {
			return nil, fmt.Errorf("teamtailor %q: more than %d pages", b.Token, maxFeedPages)
		}
	}
	return nil, fmt.Errorf("teamtailor %q: more than %d pages", b.Token, maxFeedPages)
}

func (it item) posting(b sources.Board, feedTitle string, seen time.Time) (posting.Posting, error) {
	if err := sameCompany(b.Token, it.Job.Hiring.SameAs); err != nil {
		return posting.Posting{}, err
	}
	key, err := posting.TeamtailorKey(b.Token, it.ID)
	if err != nil {
		return posting.Posting{}, err
	}
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := srcutil.Title(htmltext.Text(it.Title, srcutil.MaxTitle))
	if title == "" {
		return posting.Posting{}, errors.New("empty title")
	}
	rawURL := strings.TrimSpace(it.URL)
	if err := onTenant(b.Token, "url", rawURL); err != nil {
		return posting.Posting{}, err
	}
	posted, err := parseFeedTime(it.Job.DatePosted, it.DatePublished)
	if err != nil {
		return posting.Posting{}, err
	}
	where, remote := it.where()
	return posting.Posting{
		Key:         key,
		ATS:         posting.Teamtailor,
		Company:     companyName(b, feedTitle, it.Job.Hiring.Name),
		Title:       title,
		Location:    where,
		Remote:      remote,
		URL:         rawURL,
		PostedAt:    posted,
		Description: it.description(),
		SeenAt:      seen,
	}, nil
}

// pageURL accepts the first list URL and a next_url: https on the tenant
// host, path /jobs.json, and query keys limited to page and per_page.
func pageURL(token, raw string) (string, error) {
	if len(raw) == 0 || len(raw) > maxPageURL {
		return "", fmt.Errorf("page url length %d is outside 1..%d", len(raw), maxPageURL)
	}
	u, err := url.Parse(raw)
	host := token + ".teamtailor.com"
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil || u.Port() != "" ||
		!strings.EqualFold(u.Hostname(), host) || (u.Path != "/jobs.json" && u.Path != "/jobs.json/") || u.Fragment != "" {
		return "", fmt.Errorf("page url %q is not https://%s/jobs.json", redact(raw), host)
	}
	q := u.Query()
	for k := range q {
		if k != "page" && k != "per_page" {
			return "", fmt.Errorf("page url %q has query %q", redact(raw), k)
		}
	}
	canon := "https://" + strings.ToLower(u.Hostname()) + u.Path
	if enc := q.Encode(); enc != "" {
		canon += "?" + enc
	}
	return canon, nil
}

func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "unparsed"
	}
	return u.Redacted()
}

// onTenant accepts an https posting URL on the tenant host.
func onTenant(token, field, raw string) error {
	if raw == "" {
		return fmt.Errorf("empty %s", field)
	}
	if err := srcutil.CheckURL(field, raw); err != nil {
		return err
	}
	u, err := url.Parse(raw)
	host := token + ".teamtailor.com"
	if err != nil || !strings.EqualFold(u.Scheme, "https") || !strings.EqualFold(u.Hostname(), host) || u.Port() != "" {
		return fmt.Errorf("%s %q is not on https://%s", field, raw, host)
	}
	return nil
}

// sameCompany fails the board when hiringOrganization.sameAs names another host.
func sameCompany(token, sameAs string) error {
	sameAs = strings.TrimSpace(sameAs)
	if sameAs == "" {
		return nil
	}
	u, err := url.Parse(sameAs)
	host := token + ".teamtailor.com"
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil || u.Port() != "" || !strings.EqualFold(u.Hostname(), host) {
		return fmt.Errorf("hiringOrganization.sameAs %q is not https://%s", redact(sameAs), host)
	}
	return nil
}

func companyName(b sources.Board, feedTitle, orgName string) string {
	if name := strings.TrimSpace(b.Company); name != "" {
		return name
	}
	if name := strings.TrimSpace(feedTitle); name != "" {
		return name
	}
	if name := strings.TrimSpace(orgName); name != "" {
		return name
	}
	return b.Token
}

func (it item) where() (string, bool) {
	parts := make([]string, 0, len(it.Job.Locations))
	remote := false
	for _, p := range it.Job.Locations {
		if p.Address != nil {
			parts = append(parts, p.Address.Locality)
		}
		if saysRemote(p.Name) || (p.Address != nil && (saysRemote(p.Address.Locality) || saysRemote(p.Address.Region))) {
			remote = true
		}
	}
	return srcutil.JoinDistinct(", ", parts...), remote
}

func saysRemote(s string) bool {
	for _, f := range strings.Fields(strings.ToLower(s)) {
		if strings.Trim(f, ",.") == "remote" {
			return true
		}
	}
	return false
}

// description prefers the JobPosting body. content_html is the same text on
// the Polestar feed, so it is used only when the JobPosting body is empty.
func (it item) description() string {
	body := it.Job.Description
	if strings.TrimSpace(body) == "" {
		body = it.ContentHTML
	}
	var flags []string
	for _, e := range it.Job.Employment {
		if t := srcutil.EmploymentType(e); t != "" {
			flags = append(flags, t)
		}
	}
	lead, room := srcutil.Lead(srcutil.RoleLine(flags...))
	return lead + srcutil.Sections(room, srcutil.Section{HTML: body})
}

// parseFeedTime prefers datePosted, then date_published. An empty pair is the
// zero time; a value that is not RFC3339 fails the posting, and so the board.
func parseFeedTime(posted, published string) (time.Time, error) {
	for _, pair := range []struct{ field, s string }{
		{"datePosted", posted},
		{"date_published", published},
	} {
		s := strings.TrimSpace(pair.s)
		if s == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Time{}, fmt.Errorf("%s: %w", pair.field, err)
		}
		return t, nil
	}
	return time.Time{}, nil
}
