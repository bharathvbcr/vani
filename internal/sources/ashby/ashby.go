// Package ashby lists postings from an Ashby public job board
// (https://developers.ashbyhq.com/docs/public-job-posting-api).
package ashby

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

// ErrBoardNotFound is returned when Ashby has no such job board.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

const baseURL = "https://api.ashbyhq.com/posting-api/job-board/"

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the Ashby source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (s *source) ATS() posting.ATS { return posting.Ashby }

type response struct {
	Jobs []job `json:"jobs"`
}

type job struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Location         string `json:"location"`
	Department       string `json:"department"`
	JobURL           string `json:"jobUrl"`
	ApplyURL         string `json:"applyUrl"`
	PublishedAt      string `json:"publishedAt"`
	IsRemote         bool   `json:"isRemote"`
	IsListed         *bool  `json:"isListed"`
	DescriptionPlain string `json:"descriptionPlain"`
	DescriptionHTML  string `json:"descriptionHtml"`
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := srcutil.CheckBoard(b, posting.Ashby); err != nil {
		return nil, err
	}
	var resp response
	endpoint := baseURL + url.PathEscape(b.Token) + "?includeCompensation=false"
	if err := srcutil.GetJSON(ctx, s.d, endpoint, &resp); err != nil {
		return nil, fmt.Errorf("ashby %q: %w", b.Token, err)
	}
	if resp.Jobs == nil {
		return nil, fmt.Errorf("ashby %q: payload has no jobs array", b.Token)
	}
	if err := srcutil.CheckCount(len(resp.Jobs)); err != nil {
		return nil, fmt.Errorf("ashby %q: %w", b.Token, err)
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(resp.Jobs))
	for i, j := range resp.Jobs {
		// An unlisted job is reachable by link but not published on the board.
		if j.IsListed != nil && !*j.IsListed {
			continue
		}
		p, err := j.posting(b, seen)
		if err != nil {
			return nil, fmt.Errorf("ashby %q: job %d: %w", b.Token, i, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func (j job) posting(b sources.Board, seen time.Time) (posting.Posting, error) {
	key, err := posting.AshbyKey(b.Token, j.ID)
	if err != nil {
		return posting.Posting{}, err
	}
	if strings.TrimSpace(j.Title) == "" {
		return posting.Posting{}, errors.New("empty title")
	}
	if j.JobURL == "" {
		return posting.Posting{}, errors.New("empty jobUrl")
	}
	if err := srcutil.CheckURL("jobUrl", j.JobURL); err != nil {
		return posting.Posting{}, err
	}
	if err := srcutil.CheckURL("applyUrl", j.ApplyURL); err != nil {
		return posting.Posting{}, err
	}
	posted, err := srcutil.ParseTime("publishedAt", j.PublishedAt)
	if err != nil {
		return posting.Posting{}, err
	}
	desc := htmltext.Plain(j.DescriptionPlain, srcutil.MaxDescription)
	if desc == "" {
		desc = htmltext.Text(j.DescriptionHTML, srcutil.MaxDescription)
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.Ashby,
		Company:     b.Company,
		Title:       strings.TrimSpace(j.Title),
		Location:    strings.TrimSpace(j.Location),
		Remote:      j.IsRemote,
		Department:  strings.TrimSpace(j.Department),
		URL:         j.JobURL,
		ApplyURL:    j.ApplyURL,
		PostedAt:    posted,
		Description: desc,
		SeenAt:      seen,
	}, nil
}
