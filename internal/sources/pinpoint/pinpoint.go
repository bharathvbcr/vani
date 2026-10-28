// Package pinpoint lists postings from a Pinpoint board
// (GET https://{token}.pinpointhq.com/postings.json).
//
// One request lists the whole board and each posting carries its body, split
// over four HTML parts (description, key responsibilities, skills, benefits),
// so there is no detail request. The answer has no paging fields: workwithus
// (5 postings) and sunking (180, 1.4 MB) each came back whole in one response
// (GET 2026-10-27). The feed carries no publish date, so PostedAt is zero.
//
// robots.txt is per tenant. workwithus.pinpointhq.com and sunking.pinpointhq.com
// (GET 2026-10-27) disallow /mydata, /admin, /companies and some single
// /postings/{uuid} pages, and allow /postings.json. pinpoint.pinpointhq.com,
// Pinpoint's own tenant, disallows everything ("Disallow: /"): a tenant that does
// so is not a board to add.
package pinpoint

import (
	"bytes"
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

// ErrBoardNotFound is returned when Pinpoint has no such board.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the Pinpoint source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (*source) ATS() posting.ATS { return posting.Pinpoint }

type response struct {
	Data []item `json:"data"`
}

type item struct {
	ID                       string `json:"id"`
	Title                    string `json:"title"`
	URL                      string `json:"url"`
	Description              string `json:"description"`
	KeyResponsibilities      string `json:"key_responsibilities"`
	KeyResponsibilitiesTitle string `json:"key_responsibilities_header"`
	Skills                   string `json:"skills_knowledge_expertise"`
	SkillsTitle              string `json:"skills_knowledge_expertise_header"`
	Benefits                 string `json:"benefits"`
	BenefitsTitle            string `json:"benefits_header"`
	WorkplaceType            string `json:"workplace_type"`
	EmploymentType           string `json:"employment_type"`
	EmploymentTypeText       string `json:"employment_type_text"`
	Compensation             string `json:"compensation"`
	CompensationVisible      bool   `json:"compensation_visible"`
	CompensationMin          number `json:"compensation_minimum"`
	CompensationMax          number `json:"compensation_maximum"`
	CompensationCurrency     string `json:"compensation_currency"`
	CompensationFrequency    string `json:"compensation_frequency"`
	Job                      struct {
		Department *struct {
			Name string `json:"name"`
		} `json:"department"`
	} `json:"job"`
	Location *struct {
		City     string `json:"city"`
		Province string `json:"province"`
		Name     string `json:"name"`
	} `json:"location"`
}

// number is a pay amount: a JSON number or numeric string, and nothing at
// all when it is anything else, so a pay field that does not parse costs the
// posting its pay line, not the board.
type number struct{ v *float64 }

func (n *number) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		n.v = &f
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			n.v = &f
		}
	}
	return nil
}

func boardURL(token string) string { return "https://" + token + ".pinpointhq.com/postings.json" }

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckTenant(b, posting.Pinpoint); err != nil {
		return nil, err
	}
	var resp response
	if err := srcutil.GetJSON(ctx, s.d, boardURL(b.Token), &resp); err != nil {
		return nil, fmt.Errorf("pinpoint %q: %w", b.Token, err)
	}
	if resp.Data == nil {
		return nil, fmt.Errorf("pinpoint %q: payload has no data array", b.Token)
	}
	if err := srcutil.CheckCount(len(resp.Data)); err != nil {
		return nil, fmt.Errorf("pinpoint %q: %w", b.Token, err)
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(resp.Data))
	keys := make(map[posting.Key]int, len(resp.Data))
	for i, it := range resp.Data {
		p, err := it.posting(b, seen)
		if err != nil {
			return nil, fmt.Errorf("pinpoint %q: posting %d: %w", b.Token, i, err)
		}
		if j, dup := keys[p.Key]; dup {
			return nil, fmt.Errorf("pinpoint %q: postings %d and %d share key %s", b.Token, j, i, p.Key)
		}
		keys[p.Key] = i
		out = append(out, p)
	}
	return out, nil
}

func (it item) posting(b sources.Board, seen time.Time) (posting.Posting, error) {
	key, err := posting.PinpointKey(b.Token, it.ID)
	if err != nil {
		return posting.Posting{}, err
	}
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := srcutil.Title(it.Title)
	if title == "" {
		return posting.Posting{}, errors.New("empty title")
	}
	rawURL := strings.TrimSpace(it.URL)
	if rawURL == "" {
		return posting.Posting{}, errors.New("empty url")
	}
	if err := srcutil.CheckURL("url", rawURL); err != nil {
		return posting.Posting{}, err
	}
	p := posting.Posting{
		Key:         key,
		ATS:         posting.Pinpoint,
		Company:     b.Company,
		Title:       title,
		Location:    it.where(),
		Remote:      it.site() == "remote",
		URL:         rawURL,
		Description: it.description(),
		SeenAt:      seen,
	}
	if it.Job.Department != nil {
		p.Department = srcutil.Title(it.Job.Department.Name)
	}
	return p, nil
}

// where is the structured city and province; some boards leave both empty and
// fill only the name ("Remote", "Pittsburgh, PA"), which is then the location.
func (it item) where() string {
	if it.Location == nil {
		return ""
	}
	if loc := srcutil.JoinDistinct(", ", it.Location.City, it.Location.Province); loc != "" {
		return loc
	}
	return srcutil.JoinDistinct(", ", it.Location.Name)
}

// site is the role-line word for workplace_type; one Pinpoint does not name
// here is left out rather than passed through.
func (it item) site() string {
	switch strings.ToLower(strings.TrimSpace(it.WorkplaceType)) {
	case "remote":
		return "remote"
	case "hybrid":
		return "hybrid"
	case "onsite", "on_site", "on-site":
		return "on-site"
	}
	return ""
}

// employment is the posting's employment type in the shared wording. The text
// ("Full Time") is read first; the code ("permanent_full_time") is the
// fallback, its underscores read as spaces.
func (it item) employment() string {
	if strings.TrimSpace(it.EmploymentTypeText) != "" {
		return srcutil.EmploymentType(it.EmploymentTypeText)
	}
	return srcutil.EmploymentType(strings.ReplaceAll(it.EmploymentType, "_", " "))
}

// description is the posting's four body parts as text, each under the title
// the board gives it, led by the pay line (when the board shows pay) and a
// srcutil.RoleLine of the employment type and work site (srcutil.Lead, so the
// description cap never cuts them).
func (it item) description() string {
	lead, room := srcutil.Lead(it.payLine(), srcutil.RoleLine(it.employment(), it.site()))
	return lead + srcutil.Sections(room,
		srcutil.Section{HTML: it.Description},
		srcutil.Section{Heading: it.KeyResponsibilitiesTitle, HTML: it.KeyResponsibilities},
		srcutil.Section{Heading: it.SkillsTitle, HTML: it.Skills},
		srcutil.Section{Heading: it.BenefitsTitle, HTML: it.Benefits},
	)
}

// payLine is the board's pay when it shows it (compensation_visible): the
// structured range when it reads, else the board's own summary text. A hidden
// range is never read, whatever the fields hold.
func (it item) payLine() string {
	if !it.CompensationVisible {
		return ""
	}
	if line := srcutil.PayLine(it.CompensationCurrency, it.CompensationMin.v, it.CompensationMax.v, it.CompensationFrequency); line != "" {
		return line
	}
	return srcutil.CompensationLine(it.Compensation)
}
