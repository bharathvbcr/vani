// Package gem lists postings from a Gem job board (jobs.gem.com/{board}) through
// Gem's public job board API, GET https://api.gem.com/job_board/v0/{board}/job_posts/.
//
// The API needs no key and answers a board with one JSON array that holds every
// post and its description. It takes no paging parameters: page, per_page,
// limit and offset all returned the same whole listing (checked 2026-10-27 on
// groq, 37 posts, and retool, 27). So one request lists a board.
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

// ErrBoardNotFound is returned when Gem has no job board by that name.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

// ErrSuspectCap is returned when a listing is exactly a round number a server
// would cap a page at. The API documents no page size and the two boards
// checked (37 and 27 posts) are under every cap that was tried, so a board
// that is exactly 50, 100, ... may be a truncated listing, and the
// reconciler would age out every post past the cut. It is an error rather
// than a listing that may be partial; a board that really holds that many
// posts reads again as soon as it gains or loses one.
var ErrSuspectCap = errors.New("gem: listing is a round number that may be a server cap")

const baseURL = "https://api.gem.com/job_board/v0/"

// suspectCaps are the page sizes a listing endpoint commonly stops at, from 50
// up. Below 50 is cleared: the live boards returned 37 and 27 whole.
var suspectCaps = map[int]bool{50: true, 100: true, 200: true, 250: true, 500: true, 1000: true}

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the Gem source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (s *source) ATS() posting.ATS { return posting.Gem }

// gpost is one element of the job_posts array.
type gpost struct {
	// ID is the post's id and the last segment of its jobs.gem.com link. It
	// is the post, not the job: internal_job_id is shared by one job posted
	// per location (two "Staff Kubernetes Platform Engineer" posts on groq),
	// and each of those is its own link and posting.
	ID          string `json:"id"`
	Title       string `json:"title"`
	AbsoluteURL string `json:"absolute_url"`
	// ContentPlain is the description as text; Content is the same as HTML.
	ContentPlain string `json:"content_plain"`
	Content      string `json:"content"`
	Location     *struct {
		Name string `json:"name"`
	} `json:"location"`
	// LocationType is "in_office", "hybrid" or "remote".
	LocationType   string `json:"location_type"`
	EmploymentType string `json:"employment_type"`
	Departments    []struct {
		Name string `json:"name"`
	} `json:"departments"`
	// FirstPublishedAt is when the post first went live; UpdatedAt moves on
	// every edit, and CreatedAt predates publication (a draft).
	FirstPublishedAt string `json:"first_published_at"`
	UpdatedAt        string `json:"updated_at"`
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := srcutil.CheckBoard(b, posting.Gem); err != nil {
		return nil, err
	}
	// url.PathEscape leaves dots alone, so ".." would be a path segment the
	// client folds away, asking for /job_board/job_posts/ instead of a board.
	// config.ValidBoard refuses it; the adapter does not rely on that.
	if b.Token == "." || b.Token == ".." {
		return nil, fmt.Errorf("gem %q: token is a path segment, not a board name", b.Token)
	}
	var posts []gpost
	if err := srcutil.GetJSON(ctx, s.d, baseURL+url.PathEscape(b.Token)+"/job_posts/", &posts); err != nil {
		return nil, fmt.Errorf("gem %q: %w", b.Token, err)
	}
	if posts == nil {
		return nil, fmt.Errorf("gem %q: payload is null, not a job_posts array", b.Token)
	}
	if err := srcutil.CheckCount(len(posts)); err != nil {
		return nil, fmt.Errorf("gem %q: %w", b.Token, err)
	}
	if suspectCaps[len(posts)] {
		return nil, fmt.Errorf("gem %q: %w (%d posts)", b.Token, ErrSuspectCap, len(posts))
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(posts))
	keys := make(map[posting.Key]int, len(posts))
	for i, gp := range posts {
		p, err := gp.posting(b, seen)
		if err != nil {
			return nil, fmt.Errorf("gem %q: post %d: %w", b.Token, i, err)
		}
		// Keys are lower-cased, and Gem's ids are case-sensitive base64, so a
		// repeat is a repeated post or two ids that would share a key; either
		// way one posting would silently stand for both.
		if j, dup := keys[p.Key]; dup {
			return nil, fmt.Errorf("gem %q: posts %d and %d share key %s", b.Token, j, i, p.Key)
		}
		keys[p.Key] = i
		out = append(out, p)
	}
	return out, nil
}

func (gp gpost) posting(b sources.Board, seen time.Time) (posting.Posting, error) {
	key, err := posting.GemKey(b.Token, gp.ID)
	if err != nil {
		return posting.Posting{}, err
	}
	if strings.TrimSpace(gp.Title) == "" {
		return posting.Posting{}, errors.New("empty title")
	}
	if gp.AbsoluteURL == "" {
		return posting.Posting{}, errors.New("empty absolute_url")
	}
	if err := srcutil.CheckURL("absolute_url", gp.AbsoluteURL); err != nil {
		return posting.Posting{}, err
	}
	updated, err := srcutil.ParseTime("updated_at", gp.UpdatedAt)
	if err != nil {
		return posting.Posting{}, err
	}
	posted, err := srcutil.ParseTime("first_published_at", gp.FirstPublishedAt)
	if err != nil {
		return posting.Posting{}, err
	}
	p := posting.Posting{
		Key:         key,
		ATS:         posting.Gem,
		Company:     b.Company,
		Title:       srcutil.Title(gp.Title),
		Remote:      gp.LocationType == "remote",
		URL:         gp.AbsoluteURL,
		Description: gp.description(),
		UpdatedAt:   updated,
		PostedAt:    posted,
		SeenAt:      seen,
	}
	if gp.Location != nil {
		p.Location = strings.TrimSpace(gp.Location.Name)
	}
	if len(gp.Departments) > 0 {
		p.Department = strings.TrimSpace(gp.Departments[0].Name)
	}
	return p, nil
}

// workSite is the role-line word for a location_type; one Gem does not name
// here is left out rather than passed through.
var workSite = map[string]string{"remote": "remote", "hybrid": "hybrid", "in_office": "on-site"}

// description is the post's text, led by a srcutil.RoleLine of its employment
// type and work site (srcutil.Lead, so the description cap never cuts it). The
// text is content_plain, or content reduced to text when that is blank. Gem
// has no structured pay: a range is in the text itself (groq states it in a
// closing paragraph), where the pay parser reads it.
func (gp gpost) description() string {
	lead, room := srcutil.Lead(srcutil.RoleLine(srcutil.EmploymentType(gp.EmploymentType), workSite[gp.LocationType]))
	if strings.TrimSpace(gp.ContentPlain) != "" {
		return lead + htmltext.Plain(gp.ContentPlain, room)
	}
	return lead + htmltext.Text(gp.Content, room)
}
