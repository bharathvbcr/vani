// Package hirehive lists postings from a HireHive board
// (GET https://{token}.hirehive.com/api/v2/jobs?page=N&page_size=100).
//
// AMCS, KSN, Patagonia and Catalyx (GET 2026-11-09) answered 200
// application/json. page_size=100 is honoured: KSN's 7 postings and AMCS's
// 55 each came back on page 1 with has_next_page false, and a page past the
// end came back empty with has_next_page false. A missing company is 404
// {"message":"Company not found"}. v1 ignores page_size and returns up to 30,
// so this adapter asks v2. robots.txt of amcsgroup.hirehive.com allows every
// path (User-agent: * / Disallow empty).
//
// The page's links.next is a relative path the board chose. It is not
// followed: the next request is the next page number on this host, and a
// board that does not finish within the page cap, or whose stated total does
// not match what was read, fails whole. An empty items array with
// has_next_page false and total_items 0 is a board of zero postings.
//
// The key id is the posting's id (job_… on the boards above), not the slug
// in hosted_url. The description is description.html, or description.text
// when the HTML is empty. Pay numbers on the probed boards were JSON null;
// a tier that does carry a number is rendered, and a non-number fails the
// board.
package hirehive

import (
	"context"
	"errors"
	"fmt"
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

// ErrBoardNotFound is returned when HireHive has no such board.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

// pageSize is the page the list asks for. 100 was honoured on 2026-11-09.
const pageSize = 100

// maxPages is the most pages one board may take: a legal board of
// srcutil.MaxPostings at 100 jobs a page, with slack. A further page after
// that fails the board rather than cutting it.
const maxPages = srcutil.MaxPostings/pageSize + 10

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the HireHive source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (*source) ATS() posting.ATS { return posting.HireHive }

type pageDoc struct {
	Meta  *meta   `json:"meta"`
	Items *[]item `json:"items"`
}

type meta struct {
	TotalItems *int  `json:"total_items"`
	HasNext    *bool `json:"has_next_page"`
}

type item struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Location    string  `json:"location"`
	State       string  `json:"state_code"`
	Country     country `json:"country"`
	Salary      string  `json:"salary"`
	Description body    `json:"description"`
	Type        named   `json:"type"`
	Experience  named   `json:"experience"`
	Published   string  `json:"published_date"`
	URL         string  `json:"hosted_url"`
	Tiers       []tier  `json:"compensation_tiers"`
}

type country struct {
	Name string `json:"name"`
	Code string `json:"code"`
}

type named struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type body struct {
	HTML string `json:"html"`
	Text string `json:"text"`
}

type tier struct {
	Interval string   `json:"interval"`
	Currency string   `json:"currency_code"`
	Min      *float64 `json:"min_value"`
	Max      *float64 `json:"max_value"`
}

func listURL(token string, page int) string {
	return "https://" + token + ".hirehive.com/api/v2/jobs?page=" + strconv.Itoa(page) + "&page_size=" + strconv.Itoa(pageSize)
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckTenant(b, posting.HireHive); err != nil {
		return nil, err
	}
	out := make([]posting.Posting, 0)
	keys := map[posting.Key]int{}
	for page := 1; page <= maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var doc pageDoc
		if err := srcutil.GetJSON(ctx, s.d, listURL(b.Token, page), &doc); err != nil {
			return nil, fmt.Errorf("hirehive %q: %w", b.Token, err)
		}
		items, total, hasNext, err := doc.checked()
		if err != nil {
			return nil, fmt.Errorf("hirehive %q: %w", b.Token, err)
		}
		if err := srcutil.CheckCount(len(items)); err != nil {
			return nil, fmt.Errorf("hirehive %q: %w", b.Token, err)
		}
		if err := srcutil.CheckCount(total); err != nil {
			return nil, fmt.Errorf("hirehive %q: %w", b.Token, err)
		}
		if err := srcutil.CheckCount(len(out) + len(items)); err != nil {
			return nil, fmt.Errorf("hirehive %q: %w", b.Token, err)
		}
		now := s.now()
		for i, it := range items {
			p, err := it.posting(b, now)
			if err != nil {
				return nil, fmt.Errorf("hirehive %q: page %d item %d: %w", b.Token, page, i, err)
			}
			if prev, dup := keys[p.Key]; dup {
				return nil, fmt.Errorf("hirehive %q: postings %d and %d share key %s", b.Token, prev, len(keys), p.Key)
			}
			keys[p.Key] = len(keys)
			out = append(out, p)
		}
		if hasNext && len(items) == 0 {
			return nil, fmt.Errorf("hirehive %q: empty page before the list ended", b.Token)
		}
		if len(out) > total || (hasNext && len(out) >= total) || (!hasNext && len(out) != total) {
			return nil, fmt.Errorf("hirehive %q: read %d postings, board says %d (more pages: %v)", b.Token, len(out), total, hasNext)
		}
		if !hasNext {
			return out, nil
		}
		if page == maxPages {
			return nil, fmt.Errorf("hirehive %q: more than %d pages", b.Token, maxPages)
		}
	}
	return nil, fmt.Errorf("hirehive %q: more than %d pages", b.Token, maxPages)
}

func (p pageDoc) checked() ([]item, int, bool, error) {
	if p.Meta == nil || p.Meta.TotalItems == nil || p.Meta.HasNext == nil {
		return nil, 0, false, errors.New("payload is missing meta.total_items or meta.has_next_page")
	}
	if p.Items == nil {
		return nil, 0, false, errors.New("payload has no items array")
	}
	if *p.Meta.TotalItems < 0 {
		return nil, 0, false, fmt.Errorf("total_items %d is negative", *p.Meta.TotalItems)
	}
	return *p.Items, *p.Meta.TotalItems, *p.Meta.HasNext, nil
}

func (it item) posting(b sources.Board, seen time.Time) (posting.Posting, error) {
	if !idOK(strings.TrimSpace(it.ID)) {
		return posting.Posting{}, fmt.Errorf("id %q is not a posting id", it.ID)
	}
	key, err := posting.HireHiveKey(b.Token, it.ID)
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
	if err := onTenant(b.Token, "hosted_url", rawURL); err != nil {
		return posting.Posting{}, err
	}
	posted, err := parsePublished(it.Published)
	if err != nil {
		return posting.Posting{}, err
	}
	where := srcutil.JoinDistinct(", ", it.Location, it.State, it.Country.Name)
	return posting.Posting{
		Key:         key,
		ATS:         posting.HireHive,
		Company:     companyName(b),
		Title:       title,
		Location:    where,
		Remote:      saysRemote(it.Location) || saysRemote(it.State) || saysRemote(it.Country.Name),
		URL:         rawURL,
		PostedAt:    posted,
		Description: it.description(),
		SeenAt:      seen,
	}, nil
}

// idOK is one path-safe id: the live ids are job_ plus letters and digits,
// and a colon, slash or space would split the key or the posting URL.
func idOK(id string) bool {
	if id == "" || len(id) > 80 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// onTenant accepts an https URL on {token}.hirehive.com with no userinfo and no port.
func onTenant(token, field, raw string) error {
	if raw == "" {
		return fmt.Errorf("empty %s", field)
	}
	if err := srcutil.CheckURL(field, raw); err != nil {
		return err
	}
	u, err := url.Parse(raw)
	host := token + ".hirehive.com"
	if err != nil || !strings.EqualFold(u.Scheme, "https") || !strings.EqualFold(u.Hostname(), host) || u.Port() != "" {
		return fmt.Errorf("%s %q is not on https://%s", field, raw, host)
	}
	return nil
}

func companyName(b sources.Board) string {
	if name := strings.TrimSpace(b.Company); name != "" {
		return name
	}
	return b.Token
}

func saysRemote(s string) bool {
	for _, f := range strings.Fields(strings.ToLower(s)) {
		if strings.Trim(f, ",.") == "remote" {
			return true
		}
	}
	return false
}

func (it item) description() string {
	var lines []string
	if s := strings.TrimSpace(it.Salary); s != "" {
		lines = append(lines, srcutil.CompensationLine(s))
	}
	for _, t := range it.Tiers {
		if line := srcutil.PayLine(t.Currency, t.Min, t.Max, t.Interval); line != "" {
			lines = append(lines, line)
		}
	}
	flag := experienceFlag(it.Experience.Name)
	lines = append(lines, srcutil.RoleLine(srcutil.EmploymentType(it.Type.Type), flag))
	lead, room := srcutil.Lead(lines...)
	html := it.Description.HTML
	if strings.TrimSpace(html) == "" {
		html = it.Description.Text
	}
	return lead + srcutil.Sections(room, srcutil.Section{HTML: html})
}

func experienceFlag(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "NA") || strings.EqualFold(name, "N/A") {
		return ""
	}
	return name
}

// parsePublished reads published_date. An empty one is the zero time; a
// value that is not RFC3339 fails the posting, and so the board. Live values
// carry a fractional second (2026-11-09T15:07:56.733Z, and one digit on KSN).
func parsePublished(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("published_date: %w", err)
	}
	return t, nil
}
