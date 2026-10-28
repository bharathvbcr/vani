// Package feed lists postings from one https XML job feed the owner names
// (a <source> of <job> elements, as Shopify publishes). The adapter fetches
// that URL and no other: an apply link inside the document is stored, never
// requested. Go's encoding/xml does not load external entities.
package feed

import (
	"context"
	"encoding/xml"
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

// ErrBoardNotFound is returned when the feed URL answers 404.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

const feedAccept = "application/xml, text/xml"

// maxFeedBody caps one feed. Shopify's public feed is under a megabyte;
// 8 MiB leaves room for a few thousand jobs and still refuses a document
// that would take the whole-board cap.
var maxFeedBody int64 = 8 << 20

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the feed source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (*source) ATS() posting.ATS { return posting.Feed }

type feedDoc struct {
	XMLName xml.Name  `xml:"source"`
	Jobs    []feedJob `xml:"job"`
}

type feedJob struct {
	PartnerJobID   string `xml:"partnerJobId"`
	Company        string `xml:"company"`
	Title          string `xml:"title"`
	Description    string `xml:"description"`
	ApplyURL       string `xml:"applyUrl"`
	Location       string `xml:"location"`
	WorkplaceTypes string `xml:"workplaceTypes"`
	ListDate       string `xml:"listDate"`
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckBoard(b, posting.Feed); err != nil {
		return nil, err
	}
	rawURL, err := feedURL(b.URL)
	if err != nil {
		return nil, err
	}
	raw, err := srcutil.GetBodyLimit(ctx, s.d, rawURL, feedAccept, maxFeedBody)
	if err != nil {
		return nil, fmt.Errorf("feed %q: %w", b.Token, err)
	}
	var doc feedDoc
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("feed %q: %w", b.Token, err)
	}
	if doc.XMLName.Local != "source" {
		return nil, fmt.Errorf("feed %q: root %q is not source", b.Token, doc.XMLName.Local)
	}
	if err := srcutil.CheckCount(len(doc.Jobs)); err != nil {
		return nil, fmt.Errorf("feed %q: %w", b.Token, err)
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(doc.Jobs))
	seenID := map[string]bool{}
	for i, j := range doc.Jobs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p, err := j.posting(b, seen)
		if err != nil {
			return nil, fmt.Errorf("feed %q: job %d: %w", b.Token, i, err)
		}
		id := strings.ToLower(strings.TrimSpace(j.PartnerJobID))
		if seenID[id] {
			return nil, fmt.Errorf("feed %q: job %d: duplicate partnerJobId %q", b.Token, i, strings.TrimSpace(j.PartnerJobID))
		}
		seenID[id] = true
		out = append(out, p)
	}
	return out, nil
}

func feedURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return "", fmt.Errorf("feed: board url must be https with a host and no userinfo")
	}
	return raw, nil
}

func (j feedJob) posting(b sources.Board, seen time.Time) (posting.Posting, error) {
	id := strings.TrimSpace(j.PartnerJobID)
	key, err := posting.FeedKey(b.Token, id)
	if err != nil {
		return posting.Posting{}, err
	}
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := strings.TrimSpace(j.Title)
	if title == "" {
		return posting.Posting{}, errors.New("empty title")
	}
	rawURL := strings.TrimSpace(j.ApplyURL)
	if rawURL == "" {
		return posting.Posting{}, errors.New("empty applyUrl")
	}
	if err := srcutil.CheckURL("applyUrl", rawURL); err != nil {
		return posting.Posting{}, err
	}
	posted, err := listDate(j.ListDate)
	if err != nil {
		return posting.Posting{}, err
	}
	company := strings.TrimSpace(j.Company)
	if company == "" {
		company = b.Company
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.Feed,
		Company:     company,
		Title:       srcutil.Title(title),
		Location:    strings.TrimSpace(j.Location),
		Remote:      remoteWord(j.WorkplaceTypes),
		URL:         rawURL,
		ApplyURL:    rawURL,
		PostedAt:    posted,
		Description: htmltext.Text(j.Description, srcutil.MaxDescription),
		SeenAt:      seen,
	}, nil
}

func listDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse("01/02/2006", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("listDate: %w", err)
	}
	return t.UTC(), nil
}

func remoteWord(v string) bool {
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' }) {
		if strings.EqualFold(strings.TrimSpace(p), "remote") {
			return true
		}
	}
	return false
}
