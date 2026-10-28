// Package gem lists postings from a Gem public job board
// (GET https://api.gem.com/job_board/v0/{vanity}/job_posts/).
//
// The list is a JSON array and already carries each posting's text, so there
// is no second request. Groq (vanity "groq") and Modular (vanity "modular")
// are boards of this shape. The token is that vanity path.
package gem

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

// ErrBoardNotFound is returned when Gem has no such board.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

// ErrSuspectCap is returned when a listing's length is a round number a
// server would stop a page at. The endpoint takes no paging parameters
// (page, per_page, limit and offset all returned the same whole listing,
// checked 2026-10-27 on groq, 37 posts, and retool, 27), and it documents no
// page size. A board that is exactly 50, 100, ... may be cut off, and the
// reconciler would age out every post past the cut. It is an error rather
// than a listing that may be partial; a board that really holds that many
// posts reads again as soon as it gains or loses one.
var ErrSuspectCap = errors.New("gem: listing is a round number that may be a server cap")

const baseURL = "https://api.gem.com/job_board/v0/"

// suspectCaps are the page sizes a listing endpoint commonly stops at, from
// 50 up. Below 50 is cleared: the live boards returned 37 and 27 whole.
var suspectCaps = map[int]bool{50: true, 100: true, 200: true, 250: true, 500: true, 1000: true}

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the Gem source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (*source) ATS() posting.ATS { return posting.Gem }

// job is one element of the job_posts array. Only the fields a posting uses
// are decoded; a numeric id on a sibling object must not fail this one, so
// unused ids are left out.
type job struct {
	ID               string  `json:"id"`
	Title            string  `json:"title"`
	AbsoluteURL      string  `json:"absolute_url"`
	Content          string  `json:"content"`
	ContentPlain     string  `json:"content_plain"`
	FirstPublishedAt *string `json:"first_published_at"`
	UpdatedAt        *string `json:"updated_at"`
	// LocationType is "in_office", "hybrid" or "remote".
	LocationType string `json:"location_type"`
	// EmploymentType is Gem's word, such as "full_time".
	EmploymentType string  `json:"employment_type"`
	Location       *place  `json:"location"`
	Departments    []place `json:"departments"`
	Offices        []struct {
		Name     string `json:"name"`
		Location *place `json:"location"`
	} `json:"offices"`
}

type place struct {
	Name string `json:"name"`
}

func listURL(token string) string {
	return baseURL + url.PathEscape(token) + "/job_posts/"
}

func jobPage(token, id string) string {
	return "https://jobs.gem.com/" + url.PathEscape(token) + "/" + url.PathEscape(id)
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckBoard(b, posting.Gem); err != nil {
		return nil, err
	}
	// url.PathEscape leaves dots alone, so "." or ".." would be a path
	// segment the client folds away. config.ValidBoard refuses ".."; the
	// adapter does not rely on that, and it also refuses a lone ".".
	if b.Token == "." || b.Token == ".." {
		return nil, fmt.Errorf("gem %q: token is a path segment, not a board name", b.Token)
	}
	var jobs []job
	if err := srcutil.GetJSON(ctx, s.d, listURL(b.Token), &jobs); err != nil {
		return nil, fmt.Errorf("gem %q: %w", b.Token, err)
	}
	// null is not a list. An empty array decodes to a non-nil slice and is
	// an empty board.
	if jobs == nil {
		return nil, fmt.Errorf("gem %q: payload is not a job list", b.Token)
	}
	if err := srcutil.CheckCount(len(jobs)); err != nil {
		return nil, fmt.Errorf("gem %q: %w", b.Token, err)
	}
	if suspectCaps[len(jobs)] {
		return nil, fmt.Errorf("gem %q: %w (%d posts)", b.Token, ErrSuspectCap, len(jobs))
	}
	seenAt := s.now()
	seenKey := map[string]struct{}{}
	out := make([]posting.Posting, 0, len(jobs))
	for i, j := range jobs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := strings.TrimSpace(j.ID)
		key, err := posting.GemKey(b.Token, id)
		if err != nil {
			return nil, fmt.Errorf("gem %q: job %d: %w", b.Token, i, err)
		}
		if !strings.HasPrefix(string(key), b.KeyPrefix()) {
			return nil, fmt.Errorf("gem %q: job %d: key %q is outside %q", b.Token, i, key, b.KeyPrefix())
		}
		if _, ok := seenKey[string(key)]; ok {
			return nil, fmt.Errorf("gem %q: job %d: duplicate id %q", b.Token, i, id)
		}
		seenKey[string(key)] = struct{}{}
		p, err := j.posting(b, id, key, seenAt)
		if err != nil {
			return nil, fmt.Errorf("gem %q: job %d: %w", b.Token, i, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func (j job) posting(b sources.Board, id string, key posting.Key, seen time.Time) (posting.Posting, error) {
	title := srcutil.Title(j.Title)
	if title == "" {
		return posting.Posting{}, errors.New("empty title")
	}
	page := jobPage(b.Token, id)
	if got := strings.TrimSpace(j.AbsoluteURL); got != "" && got != page {
		return posting.Posting{}, fmt.Errorf("absolute_url %q is not %s", got, page)
	}
	if err := srcutil.CheckURL("url", page); err != nil {
		return posting.Posting{}, err
	}
	posted, err := srcutil.ParseTime("first_published_at", field(j.FirstPublishedAt))
	if err != nil {
		return posting.Posting{}, err
	}
	updated, err := srcutil.ParseTime("updated_at", field(j.UpdatedAt))
	if err != nil {
		return posting.Posting{}, err
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.Gem,
		Company:     b.Company,
		Title:       title,
		Location:    j.where(),
		Remote:      j.remote(),
		Department:  departmentNames(j.Departments),
		URL:         page,
		PostedAt:    posted,
		UpdatedAt:   updated,
		Description: j.description(),
		SeenAt:      seen,
	}, nil
}

func (j job) remote() bool {
	return strings.EqualFold(strings.TrimSpace(j.LocationType), "remote")
}

// siteWord is the role-line word for a location_type. A value Gem does not
// name is left out rather than passed through.
func siteWord(locationType string) string {
	switch strings.ToLower(strings.TrimSpace(locationType)) {
	case "remote":
		return "remote"
	case "hybrid":
		return "hybrid"
	case "in_office":
		return "on-site"
	default:
		return ""
	}
}

// description is the post's text, led by a role line of its employment type
// and work site (srcutil.Lead, so the description cap never cuts the line).
// The text is content_plain, or content reduced to text when that is blank.
// Gem has no structured pay: a range sits in the text, where the pay parser
// reads it.
func (j job) description() string {
	lead, room := srcutil.Lead(srcutil.RoleLine(srcutil.EmploymentType(j.EmploymentType), siteWord(j.LocationType)))
	if strings.TrimSpace(j.ContentPlain) != "" {
		return lead + htmltext.Plain(j.ContentPlain, room)
	}
	return lead + htmltext.Text(j.Content, room)
}

func field(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// where prefers the posting's own location, then an office location, then an
// office name. A board lists several offices on one role; the posting's
// location is the one Gem shows for it.
func (j job) where() string {
	if j.Location != nil {
		if n := strings.TrimSpace(j.Location.Name); n != "" {
			return n
		}
	}
	for _, o := range j.Offices {
		if o.Location != nil {
			if n := strings.TrimSpace(o.Location.Name); n != "" {
				return n
			}
		}
	}
	for _, o := range j.Offices {
		if n := strings.TrimSpace(o.Name); n != "" {
			return n
		}
	}
	return ""
}

func departmentNames(ds []place) string {
	var parts []string
	for _, d := range ds {
		if n := strings.TrimSpace(d.Name); n != "" {
			parts = append(parts, n)
		}
	}
	return strings.Join(parts, ", ")
}
