// Package greenhouse lists postings from a Greenhouse job board
// (https://developers.greenhouse.io/job-board.html).
package greenhouse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// ErrBoardNotFound is returned when Greenhouse has no such board token.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

const baseURL = "https://boards-api.greenhouse.io/v1/boards/"

type source struct {
	d   egress.Doer
	now func() time.Time
	// pay, when set, fetches structured pay for new and changed postings.
	pay *payer
}

// New returns the Greenhouse source, sending requests through d. It reads
// only the board list, so its postings carry the pay their text states;
// NewWithPay adds Greenhouse's structured pay.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (s *source) ATS() posting.ATS { return posting.Greenhouse }

type response struct {
	Jobs []job `json:"jobs"`
}

type job struct {
	ID          json.Number `json:"id"`
	Title       string      `json:"title"`
	AbsoluteURL string      `json:"absolute_url"`
	UpdatedAt   string      `json:"updated_at"`
	// FirstPublished is when the posting first went live; updated_at moves on
	// every edit, so it is no posted date.
	FirstPublished string `json:"first_published"`
	CompanyName    string `json:"company_name"`
	Content        string `json:"content"`
	Location       struct {
		Name string `json:"name"`
	} `json:"location"`
	Departments []struct {
		Name string `json:"name"`
	} `json:"departments"`
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := srcutil.CheckBoard(b, posting.Greenhouse); err != nil {
		return nil, err
	}
	var resp response
	endpoint := baseURL + url.PathEscape(b.Token) + "/jobs?content=true"
	if err := srcutil.GetJSON(ctx, s.d, endpoint, &resp); err != nil {
		return nil, fmt.Errorf("greenhouse %q: %w", b.Token, err)
	}
	if resp.Jobs == nil {
		return nil, fmt.Errorf("greenhouse %q: payload has no jobs array", b.Token)
	}
	if err := srcutil.CheckCount(len(resp.Jobs)); err != nil {
		return nil, fmt.Errorf("greenhouse %q: %w", b.Token, err)
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(resp.Jobs))
	for i, j := range resp.Jobs {
		p, err := j.posting(b, seen)
		if err != nil {
			return nil, fmt.Errorf("greenhouse %q: job %d: %w", b.Token, i, err)
		}
		out = append(out, p)
	}
	if s.pay != nil {
		if err := s.applyPay(ctx, b, resp.Jobs, out); err != nil {
			return nil, fmt.Errorf("greenhouse %q: %w", b.Token, err)
		}
	}
	return out, nil
}

func (j job) posting(b sources.Board, seen time.Time) (posting.Posting, error) {
	key, err := posting.GreenhouseKey(b.Token, j.ID.String())
	if err != nil {
		return posting.Posting{}, err
	}
	if strings.TrimSpace(j.Title) == "" {
		return posting.Posting{}, errors.New("empty title")
	}
	if j.AbsoluteURL == "" {
		return posting.Posting{}, errors.New("empty absolute_url")
	}
	if err := srcutil.CheckURL("absolute_url", j.AbsoluteURL); err != nil {
		return posting.Posting{}, err
	}
	updated, err := srcutil.ParseTime("updated_at", j.UpdatedAt)
	if err != nil {
		return posting.Posting{}, err
	}
	posted, err := srcutil.ParseTime("first_published", j.FirstPublished)
	if err != nil {
		return posting.Posting{}, err
	}
	company := j.CompanyName
	if company == "" {
		company = b.Company
	}
	p := posting.Posting{
		Key:       key,
		ATS:       posting.Greenhouse,
		Company:   company,
		Title:     srcutil.Title(j.Title),
		Location:  strings.TrimSpace(j.Location.Name),
		URL:       j.AbsoluteURL,
		UpdatedAt: updated,
		PostedAt:  posted,
		// content is HTML that was itself HTML-escaped: undo one level of
		// escaping to get markup, then reduce the markup to text.
		Description: htmltext.Text(html.UnescapeString(j.Content), srcutil.MaxDescription),
		SeenAt:      seen,
	}
	if len(j.Departments) > 0 {
		p.Department = strings.TrimSpace(j.Departments[0].Name)
	}
	return p, nil
}
