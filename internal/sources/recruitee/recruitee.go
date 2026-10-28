// Package recruitee lists postings from a Recruitee board
// (GET https://{token}.recruitee.com/api/offers/).
//
// The tenant's own host is used, never api.recruitee.com, whose robots.txt
// disallows every path ("User-agent: *", "Disallow: /"; GET 2026-10-27). One request lists the whole board and carries each
// offer's description and requirements HTML, so there is no detail request.
// robots.txt of syrve.recruitee.com (GET 2026-10-27) disallows only /v/; the
// offers path is allowed. Recruitee names pay as strings in a salary object,
// and the offer's dates as "2026-10-14 12:31:53 UTC".
package recruitee

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// ErrBoardNotFound is returned when Recruitee has no such board.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the Recruitee source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (*source) ATS() posting.ATS { return posting.Recruitee }

type response struct {
	Offers []offer `json:"offers"`
}

type offer struct {
	ID           json.Number `json:"id"`
	Title        string      `json:"title"`
	Status       string      `json:"status"`
	Department   string      `json:"department"`
	CareersURL   string      `json:"careers_url"`
	ApplyURL     string      `json:"careers_apply_url"`
	Location     string      `json:"location"`
	City         string      `json:"city"`
	Country      string      `json:"country"`
	Remote       bool        `json:"remote"`
	Hybrid       bool        `json:"hybrid"`
	OnSite       bool        `json:"on_site"`
	EmploymentTC string      `json:"employment_type_code"`
	PublishedAt  string      `json:"published_at"`
	CreatedAt    string      `json:"created_at"`
	UpdatedAt    string      `json:"updated_at"`
	CompanyName  string      `json:"company_name"`
	Description  string      `json:"description"`
	Requirements string      `json:"requirements"`
	Salary       *salary     `json:"salary"`
}

// salary is Recruitee's pay object: every field is present and null when
// unstated, and the amounts are JSON strings, not numbers.
type salary struct {
	Min      *string `json:"min"`
	Max      *string `json:"max"`
	Period   *string `json:"period"`
	Currency *string `json:"currency"`
}

func boardURL(token string) string { return "https://" + token + ".recruitee.com/api/offers/" }

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckTenant(b, posting.Recruitee); err != nil {
		return nil, err
	}
	var resp response
	if err := srcutil.GetJSON(ctx, s.d, boardURL(b.Token), &resp); err != nil {
		return nil, fmt.Errorf("recruitee %q: %w", b.Token, err)
	}
	if resp.Offers == nil {
		return nil, fmt.Errorf("recruitee %q: payload has no offers array", b.Token)
	}
	if err := srcutil.CheckCount(len(resp.Offers)); err != nil {
		return nil, fmt.Errorf("recruitee %q: %w", b.Token, err)
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(resp.Offers))
	keys := make(map[posting.Key]int, len(resp.Offers))
	for i, o := range resp.Offers {
		p, keep, err := o.posting(b, seen)
		if err != nil {
			return nil, fmt.Errorf("recruitee %q: offer %d: %w", b.Token, i, err)
		}
		if !keep {
			continue
		}
		if j, dup := keys[p.Key]; dup {
			return nil, fmt.Errorf("recruitee %q: offers %d and %d share key %s", b.Token, j, i, p.Key)
		}
		keys[p.Key] = i
		out = append(out, p)
	}
	return out, nil
}

func (o offer) posting(b sources.Board, seen time.Time) (posting.Posting, bool, error) {
	if st := strings.TrimSpace(o.Status); st != "" && !strings.EqualFold(st, "published") {
		return posting.Posting{}, false, nil
	}
	id := strings.TrimSpace(o.ID.String())
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return posting.Posting{}, false, fmt.Errorf("id %q is not a number", id)
	}
	key, err := posting.RecruiteeKey(b.Token, id)
	if err != nil {
		return posting.Posting{}, false, err
	}
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, false, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := srcutil.Title(o.Title)
	if title == "" {
		return posting.Posting{}, false, errors.New("empty title")
	}
	rawURL := strings.TrimSpace(o.CareersURL)
	if rawURL == "" {
		return posting.Posting{}, false, errors.New("empty careers_url")
	}
	if err := srcutil.CheckURL("careers_url", rawURL); err != nil {
		return posting.Posting{}, false, err
	}
	apply := strings.TrimSpace(o.ApplyURL)
	if err := srcutil.CheckURL("careers_apply_url", apply); err != nil {
		return posting.Posting{}, false, err
	}
	posted, err := parseTime("published_at", o.PublishedAt)
	if err != nil {
		return posting.Posting{}, false, err
	}
	if posted.IsZero() {
		if posted, err = parseTime("created_at", o.CreatedAt); err != nil {
			return posting.Posting{}, false, err
		}
	}
	updated, err := parseTime("updated_at", o.UpdatedAt)
	if err != nil {
		return posting.Posting{}, false, err
	}
	company := strings.TrimSpace(b.Company)
	if company == "" {
		company = strings.TrimSpace(o.CompanyName)
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.Recruitee,
		Company:     company,
		Title:       title,
		Location:    o.where(),
		Remote:      o.Remote,
		Department:  strings.TrimSpace(o.Department),
		URL:         rawURL,
		ApplyURL:    apply,
		PostedAt:    posted,
		UpdatedAt:   updated,
		Description: o.description(),
		SeenAt:      seen,
	}, true, nil
}

func (o offer) where() string {
	if loc := srcutil.JoinDistinct(", ", o.Location); loc != "" {
		return loc
	}
	return srcutil.JoinDistinct(", ", o.City, o.Country)
}

// workSite is the role-line word for the offer's work site. Recruitee keeps
// remote, hybrid and on-site as separate flags; remote wins over hybrid, which
// wins over on-site, and an offer that sets none says nothing.
func (o offer) workSite() string {
	switch {
	case o.Remote:
		return "remote"
	case o.Hybrid:
		return "hybrid"
	case o.OnSite:
		return "on-site"
	}
	return ""
}

// employment is the offer's employment type word: employment_type_code reads
// "fulltime_permanent", so its first part ("fulltime") is the schedule.
func (o offer) employment() string {
	first, _, _ := strings.Cut(strings.TrimSpace(o.EmploymentTC), "_")
	return srcutil.EmploymentType(first)
}

// description is the offer's text, led by a pay line when the salary object
// names a usable range and a srcutil.RoleLine of its employment type and work
// site (srcutil.Lead, so the description cap never cuts them).
func (o offer) description() string {
	lead, room := srcutil.Lead(o.payLine(), srcutil.RoleLine(o.employment(), o.workSite()))
	return lead + srcutil.Sections(room, srcutil.Section{HTML: o.Description}, srcutil.Section{HTML: o.Requirements})
}

// payLine renders the salary object, or "" when it holds no readable range:
// an amount that is not a number, or a missing or unknown currency, is left
// out, never guessed.
func (o offer) payLine() string {
	if o.Salary == nil {
		return ""
	}
	return srcutil.PayLine(deref(o.Salary.Currency), amount(o.Salary.Min), amount(o.Salary.Max), deref(o.Salary.Period))
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// amount reads one salary bound, a JSON string; nil when it is absent or not a number.
func amount(s *string) *float64 {
	if s == nil {
		return nil
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(*s), 64)
	if err != nil {
		return nil
	}
	return &v
}

// layout is Recruitee's timestamp: "2026-10-14 12:31:53 UTC".
const layout = "2006-01-02 15:04:05 UTC"

// parseTime reads one of the offer's timestamps; an empty one is the zero time.
func parseTime(field, s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(layout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s %q is not a %q timestamp", field, s, layout)
	}
	return t, nil
}
