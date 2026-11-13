// Package breezy lists postings from a Breezy HR board
// (GET https://{token}.breezy.hr/json).
//
// One request returns the whole board as a JSON array. thirdchannel-inc
// (GET 2026-11-09) answered 200 application/json, 931 objects, and its
// robots.txt allows /json (it disallows /css, /fonts, /stylesheets and
// /javascripts). breezy.hr/robots.txt itself answered 403. The list has no
// description: a job page is an Angular shell, and fetching one per posting
// would be a detail fan-out (that board alone is 931 jobs), so a posting's
// text is the salary line and the employment type. Pay is the salary string
// as written ("$23+ / hour"), not a structured range.
package breezy

import (
	"context"
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

// ErrBoardNotFound is returned when Breezy has no such board.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the Breezy source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (*source) ATS() posting.ATS { return posting.Breezy }

type job struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	URL           string   `json:"url"`
	PublishedDate string   `json:"published_date"`
	Type          jobType  `json:"type"`
	Location      *loc     `json:"location"`
	Locations     []loc    `json:"locations"`
	Department    string   `json:"department"`
	Salary        string   `json:"salary"`
	Company       *company `json:"company"`
}

type jobType struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type loc struct {
	Name     string `json:"name"`
	Primary  bool   `json:"primary"`
	IsRemote bool   `json:"is_remote"`
}

type company struct {
	Name       string `json:"name"`
	FriendlyID string `json:"friendly_id"`
}

func boardURL(token string) string { return "https://" + token + ".breezy.hr/json" }

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckTenant(b, posting.Breezy); err != nil {
		return nil, err
	}
	var jobs []job
	if err := srcutil.GetJSON(ctx, s.d, boardURL(b.Token), &jobs); err != nil {
		return nil, fmt.Errorf("breezy %q: %w", b.Token, err)
	}
	if jobs == nil {
		return nil, fmt.Errorf("breezy %q: payload is not an array", b.Token)
	}
	if err := srcutil.CheckCount(len(jobs)); err != nil {
		return nil, fmt.Errorf("breezy %q: %w", b.Token, err)
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(jobs))
	keys := make(map[posting.Key]int, len(jobs))
	rows := srcutil.NewRows(ctx, b, len(jobs))
	for i, j := range jobs {
		p, err := j.posting(b, seen)
		if err != nil {
			if ferr := rows.Bad(err); ferr != nil {
				return nil, fmt.Errorf("breezy %q: posting %d: %w", b.Token, i, ferr)
			}
			continue
		}
		if prev, dup := keys[p.Key]; dup {
			return nil, fmt.Errorf("breezy %q: postings %d and %d share key %s", b.Token, prev, i, p.Key)
		}
		keys[p.Key] = i
		out = append(out, p)
	}
	if err := rows.Done(); err != nil {
		return nil, fmt.Errorf("breezy %q: %w", b.Token, err)
	}
	return out, nil
}

func (j job) posting(b sources.Board, seen time.Time) (posting.Posting, error) {
	if j.Company != nil {
		id := strings.TrimSpace(j.Company.FriendlyID)
		if id != "" && !strings.EqualFold(id, b.Token) {
			return posting.Posting{}, fmt.Errorf("company.friendly_id %q is not %q", id, b.Token)
		}
	}
	key, err := posting.BreezyKey(b.Token, j.ID)
	if err != nil {
		return posting.Posting{}, err
	}
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := srcutil.Title(htmltext.Text(j.Name, srcutil.MaxTitle))
	if title == "" {
		return posting.Posting{}, errors.New("empty title")
	}
	rawURL := strings.TrimSpace(j.URL)
	if err := onTenant(b.Token, "url", rawURL); err != nil {
		return posting.Posting{}, err
	}
	posted, err := parsePublished(j.PublishedDate)
	if err != nil {
		return posting.Posting{}, err
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.Breezy,
		Company:     companyName(b, j.Company),
		Title:       title,
		Location:    j.where(),
		Remote:      j.remote(),
		Department:  strings.TrimSpace(j.Department),
		URL:         rawURL,
		PostedAt:    posted,
		Description: j.description(),
		SeenAt:      seen,
	}, nil
}

// onTenant accepts an https URL on {token}.breezy.hr with no userinfo and no port.
func onTenant(token, field, raw string) error {
	if raw == "" {
		return fmt.Errorf("empty %s", field)
	}
	if err := srcutil.CheckURL(field, raw); err != nil {
		return err
	}
	u, err := url.Parse(raw)
	host := token + ".breezy.hr"
	if err != nil || !strings.EqualFold(u.Scheme, "https") || !strings.EqualFold(u.Hostname(), host) || u.Port() != "" {
		return fmt.Errorf("%s %q is not on https://%s", field, raw, host)
	}
	return nil
}

func companyName(b sources.Board, c *company) string {
	if name := strings.TrimSpace(b.Company); name != "" {
		return name
	}
	if c != nil {
		if name := strings.TrimSpace(c.Name); name != "" {
			return name
		}
	}
	return b.Token
}

// where is the primary location's name, or the distinct location names when
// the primary name is empty. One posting per id: locations do not split it.
func (j job) where() string {
	if j.Location != nil {
		if name := strings.TrimSpace(j.Location.Name); name != "" {
			return name
		}
	}
	names := make([]string, 0, len(j.Locations))
	for _, loc := range j.Locations {
		names = append(names, loc.Name)
	}
	return srcutil.JoinDistinct(", ", names...)
}

// remote follows the primary location. A mix of remote and on-site locations
// with no single primary is not marked remote: the posting is one job.
func (j job) remote() bool {
	if j.Location != nil {
		return j.Location.IsRemote
	}
	var sawPrimary, primaryRemote, disagree bool
	allRemote := len(j.Locations) > 0
	for _, loc := range j.Locations {
		if !loc.IsRemote {
			allRemote = false
		}
		if !loc.Primary {
			continue
		}
		if sawPrimary && primaryRemote != loc.IsRemote {
			disagree = true
		}
		sawPrimary = true
		primaryRemote = loc.IsRemote
	}
	if disagree {
		return false
	}
	if sawPrimary {
		return primaryRemote
	}
	return allRemote
}

// description is the salary string and the employment type. The list carries
// no job body (checked 2026-11-09).
func (j job) description() string {
	lead, _ := srcutil.Lead(srcutil.CompensationLine(j.Salary), srcutil.RoleLine(srcutil.EmploymentType(j.Type.Name)))
	return lead
}

// parsePublished reads published_date. An empty one is the zero time; a
// value that is not RFC3339 fails the posting, and so the board.
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
