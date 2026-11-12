// Package hibob lists postings from a HiBob career site
// (GET https://{token}.careers.hibob.com/api/job-ad).
//
// Bond Brand Loyalty, Synpulse, Zepz, Corlytics, WBCSD and GenesisCare
// (GET 2026-11-09) answered 200 application/json with the whole board in
// jobAdDetails when the request sent Referer
// https://{token}.careers.hibob.com/jobs. The same URL without that header,
// or with a different Referer, answered 401, and so did a host that is not a
// board. A 401 is therefore an error, not a missing board: treating it as
// gone would drop a watchlist when the header or the edge changes. A 404
// stays a missing board. robots.txt of corlytics.careers.hibob.com is the
// careers page itself (200 text/html, no Disallow); the jobs page is what
// loads /api/job-ad.
//
// The key id is the ad's id, a UUID on the boards above. The posting URL is
// https://{token}.careers.hibob.com/jobs/{id}, which is the public job path.
// The body is description, responsibilities, requirements and benefits, each
// kept only when it has text. Pay numbers on the probed boards were JSON
// null; a number that is present is rendered, and a non-number fails the board.
package hibob

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// ErrBoardNotFound is returned when HiBob has no such board.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the HiBob source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (*source) ATS() posting.ATS { return posting.HiBob }

type payload struct {
	Ads *[]ad `json:"jobAdDetails"`
}

type ad struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Department       string   `json:"department"`
	Employment       string   `json:"employmentType"`
	Site             string   `json:"site"`
	Country          string   `json:"country"`
	Description      string   `json:"description"`
	Requirements     string   `json:"requirements"`
	Responsibilities string   `json:"responsibilities"`
	Benefits         string   `json:"benefits"`
	Published        string   `json:"publishedAt"`
	WorkspaceID      string   `json:"workspaceTypeId"`
	Workspace        string   `json:"workspaceType"`
	PayMin           *float64 `json:"payTransparencyMinSalary"`
	PayMax           *float64 `json:"payTransparencyMaxSalary"`
	PayCurrency      string   `json:"payTransparencySalaryCurrency"`
	PayPeriod        string   `json:"payTransparencySalaryPayPeriod"`
	PayNote          string   `json:"payTransparencySalaryAdditionalInfo"`
	Places           []place  `json:"postingLocations"`
}

type place struct {
	City    string `json:"city"`
	Region  string `json:"region"`
	Country string `json:"country"`
}

func boardURL(token string) string { return "https://" + token + ".careers.hibob.com/api/job-ad" }

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckTenant(b, posting.HiBob); err != nil {
		return nil, err
	}
	header := http.Header{"Referer": {"https://" + b.Token + ".careers.hibob.com/jobs"}}
	var doc payload
	if err := srcutil.DoJSON(ctx, s.d, http.MethodGet, boardURL(b.Token), nil, header, &doc); err != nil {
		return nil, fmt.Errorf("hibob %q: %w", b.Token, err)
	}
	if doc.Ads == nil {
		return nil, fmt.Errorf("hibob %q: payload has no jobAdDetails array", b.Token)
	}
	ads := *doc.Ads
	if err := srcutil.CheckCount(len(ads)); err != nil {
		return nil, fmt.Errorf("hibob %q: %w", b.Token, err)
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(ads))
	keys := make(map[posting.Key]int, len(ads))
	for i, ad := range ads {
		p, err := ad.posting(b, seen)
		if err != nil {
			return nil, fmt.Errorf("hibob %q: posting %d: %w", b.Token, i, err)
		}
		if prev, dup := keys[p.Key]; dup {
			return nil, fmt.Errorf("hibob %q: postings %d and %d share key %s", b.Token, prev, i, p.Key)
		}
		keys[p.Key] = i
		out = append(out, p)
	}
	return out, nil
}

func (a ad) posting(b sources.Board, seen time.Time) (posting.Posting, error) {
	id := strings.TrimSpace(a.ID)
	if !idOK(id) {
		return posting.Posting{}, fmt.Errorf("id %q is not a posting id", a.ID)
	}
	key, err := posting.HiBobKey(b.Token, id)
	if err != nil {
		return posting.Posting{}, err
	}
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := srcutil.Title(htmltext.Text(a.Title, srcutil.MaxTitle))
	if title == "" {
		return posting.Posting{}, errors.New("empty title")
	}
	rawURL := "https://" + b.Token + ".careers.hibob.com/jobs/" + id
	if err := onTenant(b.Token, rawURL); err != nil {
		return posting.Posting{}, err
	}
	posted, err := parsePublished(a.Published)
	if err != nil {
		return posting.Posting{}, err
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.HiBob,
		Company:     companyName(b),
		Title:       title,
		Location:    a.where(),
		Remote:      saysRemote(a.Workspace) || saysRemote(a.WorkspaceID),
		Department:  strings.TrimSpace(a.Department),
		URL:         rawURL,
		PostedAt:    posted,
		Description: a.description(),
		SeenAt:      seen,
	}, nil
}

// idOK is one path segment. Live ids are UUIDs; a slash or colon would leave
// the /jobs/{id} path or split the key.
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

func onTenant(token, raw string) error {
	if err := srcutil.CheckURL("url", raw); err != nil {
		return err
	}
	u, err := url.Parse(raw)
	host := token + ".careers.hibob.com"
	if err != nil || !strings.EqualFold(u.Scheme, "https") || !strings.EqualFold(u.Hostname(), host) || u.Port() != "" {
		return fmt.Errorf("url %q is not on https://%s", raw, host)
	}
	return nil
}

func companyName(b sources.Board) string {
	if name := strings.TrimSpace(b.Company); name != "" {
		return name
	}
	return b.Token
}

func (a ad) where() string {
	parts := []string{a.Site, a.Country}
	for _, p := range a.Places {
		parts = append(parts, p.City, p.Region, p.Country)
	}
	return srcutil.JoinDistinct(", ", parts...)
}

func saysRemote(s string) bool {
	for _, f := range strings.Fields(strings.ToLower(s)) {
		if strings.Trim(f, ",.") == "remote" {
			return true
		}
	}
	return false
}

func (a ad) description() string {
	lead, room := srcutil.Lead(
		srcutil.PayLine(a.PayCurrency, a.PayMin, a.PayMax, a.PayPeriod),
		srcutil.CompensationLine(a.PayNote),
		srcutil.RoleLine(srcutil.EmploymentType(a.Employment), strings.TrimSpace(a.Workspace)),
	)
	return lead + srcutil.Sections(room,
		srcutil.Section{HTML: a.Description},
		srcutil.Section{Heading: "Responsibilities", HTML: a.Responsibilities},
		srcutil.Section{Heading: "Requirements", HTML: a.Requirements},
		srcutil.Section{Heading: "Benefits", HTML: a.Benefits},
	)
}

// parsePublished reads publishedAt. An empty one is the zero time; a value
// that is not RFC3339 fails the posting, and so the board. Live values carry
// a fractional second (2026-09-24T13:45:03.660569Z).
func parsePublished(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("publishedAt: %w", err)
	}
	return t, nil
}
