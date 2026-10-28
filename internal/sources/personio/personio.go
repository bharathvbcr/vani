// Package personio lists postings from a Personio career site through its XML
// feed (GET https://{token}.jobs.personio.com/xml?language=en).
//
// One request lists the whole board with each position's body, as a
// <workzag-jobs> document of <position> elements, so there is no detail
// request. The bare feed is locale-gated and leaves the body of a position
// that is not published in the site's default language empty; language=en asks
// for the English text, and a position with no English body is listed with its
// role line only. The feed carries no URL, so the posting URL is the position's
// own page, https://{token}.jobs.personio.com/job/{id}. A tenant can switch
// its XML feed off, and it then answers 404 like a board that does not exist.
// The feed has no remote flag: the offices' names ("Remote - Germany",
// "Hybrid - London") are what says where the work is done.
//
// robots.txt of deskbird.jobs.personio.com (GET 2026-10-27) is "User-agent: *"
// and "Allow: /". The document is read with encoding/xml, which loads no
// external entity or DTD.
package personio

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// ErrBoardNotFound is returned when Personio has no such site, or the site has
// its XML feed switched off.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

const feedAccept = "application/xml, text/xml"

// maxFeedBody caps one feed. A feed of a few hundred positions with full
// bodies is a few megabytes; 16 MiB still refuses a document that would take
// the whole-board cap.
var maxFeedBody int64 = 16 << 20

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the Personio source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (*source) ATS() posting.ATS { return posting.Personio }

type feedDoc struct {
	XMLName   xml.Name   `xml:"workzag-jobs"`
	Positions []position `xml:"position"`
}

type position struct {
	ID                string        `xml:"id"`
	Subcompany        string        `xml:"subcompany"`
	Office            string        `xml:"office"`
	AdditionalOffices []string      `xml:"additionalOffices>office"`
	Department        string        `xml:"department"`
	Name              string        `xml:"name"`
	Descriptions      []description `xml:"jobDescriptions>jobDescription"`
	EmploymentType    string        `xml:"employmentType"`
	Seniority         string        `xml:"seniority"`
	Schedule          string        `xml:"schedule"`
	CreatedAt         string        `xml:"createdAt"`
}

type description struct {
	Name  string `xml:"name"`
	Value string `xml:"value"`
}

func host(token string) string { return token + ".jobs.personio.com" }

func feedURL(token string) string { return "https://" + host(token) + "/xml?language=en" }

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckTenant(b, posting.Personio); err != nil {
		return nil, err
	}
	raw, err := srcutil.GetBodyLimit(ctx, s.d, feedURL(b.Token), feedAccept, maxFeedBody)
	if err != nil {
		if errors.Is(err, ErrBoardNotFound) {
			err = fmt.Errorf("%w (no such site, or its XML feed is switched off)", err)
		}
		return nil, fmt.Errorf("personio %q: %w", b.Token, err)
	}
	var doc feedDoc
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("personio %q: %w", b.Token, err)
	}
	if err := srcutil.CheckCount(len(doc.Positions)); err != nil {
		return nil, fmt.Errorf("personio %q: %w", b.Token, err)
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(doc.Positions))
	keys := make(map[posting.Key]int, len(doc.Positions))
	for i, pos := range doc.Positions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p, err := pos.posting(b, seen)
		if err != nil {
			return nil, fmt.Errorf("personio %q: position %d: %w", b.Token, i, err)
		}
		if j, dup := keys[p.Key]; dup {
			return nil, fmt.Errorf("personio %q: positions %d and %d share key %s", b.Token, j, i, p.Key)
		}
		keys[p.Key] = i
		out = append(out, p)
	}
	return out, nil
}

func (pos position) posting(b sources.Board, seen time.Time) (posting.Posting, error) {
	id := strings.TrimSpace(pos.ID)
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return posting.Posting{}, fmt.Errorf("id %q is not a number", id)
	}
	key, err := posting.PersonioKey(b.Token, id)
	if err != nil {
		return posting.Posting{}, err
	}
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := srcutil.Title(pos.Name)
	if title == "" {
		return posting.Posting{}, errors.New("empty name")
	}
	posted, err := srcutil.ParseTime("createdAt", strings.TrimSpace(pos.CreatedAt))
	if err != nil {
		return posting.Posting{}, err
	}
	company := strings.TrimSpace(b.Company)
	if company == "" {
		company = srcutil.Title(pos.Subcompany)
	}
	offices := append([]string{pos.Office}, pos.AdditionalOffices...)
	remote, hybrid := officeSite(offices)
	return posting.Posting{
		Key:         key,
		ATS:         posting.Personio,
		Company:     company,
		Title:       title,
		Location:    srcutil.JoinDistinct("; ", offices...),
		Remote:      remote,
		Department:  srcutil.Title(pos.Department),
		URL:         "https://" + host(b.Token) + "/job/" + id,
		PostedAt:    posted.UTC(),
		Description: pos.description(remote, hybrid),
		SeenAt:      seen,
	}, nil
}

// officeSite reads where the work is done from the offices' names: a word
// "remote" or "hybrid" in any of them. Remote wins over hybrid, as a position
// that names a remote office can be worked from anywhere that office covers.
func officeSite(offices []string) (remote, hybrid bool) {
	for _, o := range offices {
		for _, w := range strings.FieldsFunc(o, func(r rune) bool { return !unicode.IsLetter(r) }) {
			switch {
			case strings.EqualFold(w, "remote"):
				remote = true
			case strings.EqualFold(w, "hybrid"):
				hybrid = true
			}
		}
	}
	return remote, hybrid && !remote
}

// description is the position's parts as text, each under the name the feed
// gives it, led by a srcutil.RoleLine of its schedule, employment type,
// seniority and work site (srcutil.Lead, so the description cap never cuts it).
func (pos position) description(remote, hybrid bool) string {
	site := ""
	switch {
	case remote:
		site = "remote"
	case hybrid:
		site = "hybrid"
	}
	lead, room := srcutil.Lead(srcutil.RoleLine(srcutil.EmploymentType(pos.Schedule), srcutil.EmploymentType(pos.EmploymentType), pos.Seniority, site))
	parts := make([]srcutil.Section, len(pos.Descriptions))
	for i, d := range pos.Descriptions {
		parts[i] = srcutil.Section{Heading: d.Name, HTML: d.Value}
	}
	return lead + srcutil.Sections(room, parts...)
}
