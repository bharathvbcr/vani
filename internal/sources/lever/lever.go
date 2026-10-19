// Package lever lists postings from a Lever postings API
// (https://github.com/lever/postings-api).
package lever

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

// ErrBoardNotFound is returned when Lever has no such company.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

const (
	usBase = "https://api.lever.co/v0/postings/"
	euBase = "https://api.eu.lever.co/v0/postings/"
)

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the Lever source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (s *source) ATS() posting.ATS { return posting.Lever }

type lpost struct {
	ID               string `json:"id"`
	Text             string `json:"text"`
	HostedURL        string `json:"hostedUrl"`
	ApplyURL         string `json:"applyUrl"`
	CreatedAt        int64  `json:"createdAt"`
	WorkplaceType    string `json:"workplaceType"`
	DescriptionPlain string `json:"descriptionPlain"`
	Categories       struct {
		Location string `json:"location"`
		Team     string `json:"team"`
	} `json:"categories"`
	Lists []struct {
		Text    string `json:"text"`
		Content string `json:"content"`
	} `json:"lists"`
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := srcutil.CheckBoard(b, posting.Lever); err != nil {
		return nil, err
	}
	base := usBase
	if b.Region == "eu" {
		base = euBase
	} else if b.Region != "" {
		return nil, fmt.Errorf("lever %q: unknown region %q", b.Token, b.Region)
	}
	var posts []lpost
	if err := srcutil.GetJSON(ctx, s.d, base+url.PathEscape(b.Token)+"?mode=json", &posts); err != nil {
		return nil, fmt.Errorf("lever %q: %w", b.Token, err)
	}
	if posts == nil {
		return nil, fmt.Errorf("lever %q: payload is null, not a postings array", b.Token)
	}
	if err := srcutil.CheckCount(len(posts)); err != nil {
		return nil, fmt.Errorf("lever %q: %w", b.Token, err)
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(posts))
	for i, lp := range posts {
		p, err := lp.posting(b, seen)
		if err != nil {
			return nil, fmt.Errorf("lever %q: posting %d: %w", b.Token, i, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func (lp lpost) posting(b sources.Board, seen time.Time) (posting.Posting, error) {
	key, err := posting.LeverKey(b.Token, lp.ID)
	if err != nil {
		return posting.Posting{}, err
	}
	if strings.TrimSpace(lp.Text) == "" {
		return posting.Posting{}, errors.New("empty text")
	}
	if lp.HostedURL == "" {
		return posting.Posting{}, errors.New("empty hostedUrl")
	}
	if err := srcutil.CheckURL("hostedUrl", lp.HostedURL); err != nil {
		return posting.Posting{}, err
	}
	if err := srcutil.CheckURL("applyUrl", lp.ApplyURL); err != nil {
		return posting.Posting{}, err
	}
	p := posting.Posting{
		Key:         key,
		ATS:         posting.Lever,
		Company:     b.Company,
		Title:       strings.TrimSpace(lp.Text),
		Location:    strings.TrimSpace(lp.Categories.Location),
		Remote:      lp.WorkplaceType == "remote",
		Department:  strings.TrimSpace(lp.Categories.Team),
		URL:         lp.HostedURL,
		ApplyURL:    lp.ApplyURL,
		Description: lp.description(),
		SeenAt:      seen,
	}
	if lp.CreatedAt > 0 {
		p.PostedAt = time.UnixMilli(lp.CreatedAt).UTC()
	}
	return p, nil
}

// description is the plain description followed by each list (requirements,
// benefits, ...) as a heading line and its text. The lists carry HTML.
func (lp lpost) description() string {
	var sb strings.Builder
	sb.WriteString(lp.DescriptionPlain)
	for _, l := range lp.Lists {
		sb.WriteString("\n\n")
		sb.WriteString(l.Text)
		sb.WriteString("\n")
		sb.WriteString(htmltext.Text(l.Content, srcutil.MaxDescription))
	}
	return htmltext.Plain(sb.String(), srcutil.MaxDescription)
}
