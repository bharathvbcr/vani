// Package remoteok lists postings from Remote OK's public API,
// GET https://remoteok.com/api. Remote OK is a job board, not an employer's
// ATS: its one listing names many employers, so each posting's company is its
// row's own, and a row that names none is a bad row rather than one filed
// under the board's name.
//
// The reply is a JSON array whose first element is a notice, not a job:
// {"last_updated":…,"legal":"API Terms of Service: Please link back … to the
// URL on Remote OK and mention Remote OK as a source …"} (read 2026-11-17).
// Vani keeps each job's remoteok.com page as the posting URL and its
// apply_url as the apply URL, and republishes nothing. robots.txt
// (2026-11-17) allows /api to agents it does not name (Crawl-delay 1),
// disallows named AI-training and SEO crawlers, and states
// "Content-Signal: search=yes,ai-train=no,use=reference" (docs/LEGAL.md).
//
// One request lists the board, about a hundred recent jobs with their
// descriptions (98 rows, 650 KB on 2026-11-17), so there is no paging, no
// detail request and no search: the API takes none that is documented.
package remoteok

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	// Token is the one board token: the API is one site.
	Token  = "remoteok"
	apiURL = "https://remoteok.com/api"
	// maxBody caps the reply: 650 KB was seen, and four times the rows
	// still fit.
	maxBody int64 = 4 << 20
)

// ErrShape is a reply whose layout is not the one this source knows.
var ErrShape = errors.New("remoteok: unexpected api response")

var idRE = regexp.MustCompile(`^[0-9]{1,15}$`)

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the Remote OK source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (*source) ATS() posting.ATS { return posting.RemoteOK }

type job struct {
	// ID is a string ("1137796") in every row seen; a number is read too.
	ID          json.RawMessage `json:"id"`
	Epoch       *int64          `json:"epoch"`
	Date        string          `json:"date"`
	Company     string          `json:"company"`
	Position    string          `json:"position"`
	Description string          `json:"description"`
	Location    string          `json:"location"`
	ApplyURL    string          `json:"apply_url"`
	URL         string          `json:"url"`
	// Legal is set only on the notice that opens the array.
	Legal *string `json:"legal"`
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckBoard(b, posting.RemoteOK); err != nil {
		return nil, err
	}
	if b.Token != Token {
		return nil, fmt.Errorf("remoteok: board token %q is not %q", b.Token, Token)
	}
	if len(b.SearchTerms()) > 0 || len(b.Filters) > 0 {
		return nil, fmt.Errorf("remoteok %q: the API takes no search or filters", b.Token)
	}
	var raw []json.RawMessage
	if err := srcutil.GetJSONLimit(ctx, s.d, apiURL, maxBody, &raw); err != nil {
		return nil, fmt.Errorf("remoteok %q: %w", b.Token, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("remoteok %q: %w: the reply is not an array", b.Token, ErrShape)
	}
	if err := srcutil.CheckCount(len(raw)); err != nil {
		return nil, fmt.Errorf("remoteok %q: %w", b.Token, err)
	}
	// The notice is not a row: it neither counts toward the listing nor is a
	// bad row. Only the first element may be one.
	type element struct {
		j   job
		err error
	}
	els := make([]element, len(raw))
	for i, el := range raw {
		if err := json.Unmarshal(el, &els[i].j); err != nil {
			els[i].err = fmt.Errorf("%w: row is not a job object: %w", ErrShape, err)
		}
	}
	if len(els) > 0 && els[0].err == nil && els[0].j.notice() {
		els = els[1:]
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(els))
	keys := make(map[posting.Key]int, len(els))
	rows := srcutil.NewRows(ctx, b, len(els))
	for i, el := range els {
		var p posting.Posting
		err := el.err
		if err == nil && el.j.notice() {
			err = fmt.Errorf("%w: a notice element after the first", ErrShape)
		}
		if err == nil {
			p, err = el.j.posting(b, seen)
		}
		if err != nil {
			if ferr := rows.Bad(err); ferr != nil {
				return nil, fmt.Errorf("remoteok %q: row %d: %w", b.Token, i, ferr)
			}
			continue
		}
		if k, dup := keys[p.Key]; dup {
			return nil, fmt.Errorf("remoteok %q: rows %d and %d share key %s", b.Token, k, i, p.Key)
		}
		keys[p.Key] = i
		out = append(out, p)
	}
	if err := rows.Done(); err != nil {
		return nil, fmt.Errorf("remoteok %q: %w", b.Token, err)
	}
	return out, nil
}

// notice reports whether the element is the terms notice that opens the
// array: a legal text and no job id.
func (j job) notice() bool {
	return j.Legal != nil && len(bytes.TrimSpace(j.ID)) == 0
}

// id is the row's job id, written as a JSON string or number.
func (j job) id() (string, error) {
	raw := bytes.TrimSpace(j.ID)
	var s string
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", fmt.Errorf("%w: id %s", ErrShape, raw)
		}
	} else {
		s = string(raw)
	}
	if !idRE.MatchString(s) {
		return "", fmt.Errorf("%w: id %q is not a number", ErrShape, s)
	}
	return s, nil
}

// undoLatin1 repairs text that was UTF-8 read as Latin-1 and encoded again,
// as 75 of 98 Remote OK descriptions were on 2026-11-17 ("DoiT’s" served as
// "DoiTâ\u0080\u0099s"). Each run of runes all below U+0100 that holds one
// at or above U+0080 is taken back to its bytes, and the bytes replace the
// run only when they are valid UTF-8 that differs from it. Genuine Latin-1
// text ("café") does not survive that test (a lone 0xE9 byte is not
// UTF-8), and runes outside the range (an emoji, CJK) end a run.
func undoLatin1(s string) string {
	var b strings.Builder
	run := make([]byte, 0, 64)
	high := false
	flush := func() {
		if high && utf8.Valid(run) {
			b.Write(run)
		} else {
			for _, c := range run {
				b.WriteRune(rune(c))
			}
		}
		run, high = run[:0], false
	}
	for _, r := range s {
		if r < 0x100 {
			run = append(run, byte(r))
			high = high || r >= 0x80
			continue
		}
		flush()
		b.WriteRune(r)
	}
	flush()
	return b.String()
}

func (j job) posting(b sources.Board, seen time.Time) (posting.Posting, error) {
	j.Position, j.Company, j.Location, j.Description = undoLatin1(j.Position), undoLatin1(j.Company), undoLatin1(j.Location), undoLatin1(j.Description)
	id, err := j.id()
	if err != nil {
		return posting.Posting{}, err
	}
	key, err := posting.RemoteOKKey(b.Token, id)
	if err != nil {
		return posting.Posting{}, err
	}
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := srcutil.Title(j.Position)
	if title == "" {
		return posting.Posting{}, fmt.Errorf("%w: empty position", ErrShape)
	}
	company := strings.Join(strings.Fields(j.Company), " ")
	if company == "" {
		return posting.Posting{}, fmt.Errorf("%w: empty company", ErrShape)
	}
	page := strings.TrimSpace(j.URL)
	if page == "" {
		return posting.Posting{}, fmt.Errorf("%w: empty url", ErrShape)
	}
	if err := srcutil.CheckURL("url", page); err != nil {
		return posting.Posting{}, err
	}
	apply := strings.TrimSpace(j.ApplyURL)
	if err := srcutil.CheckURL("apply_url", apply); err != nil {
		return posting.Posting{}, err
	}
	if apply == "" {
		apply = page
	}
	posted, err := j.posted()
	if err != nil {
		return posting.Posting{}, err
	}
	lead, room := srcutil.Lead(srcutil.RoleLine("remote"))
	return posting.Posting{
		Key:         key,
		ATS:         posting.RemoteOK,
		Company:     company,
		Title:       title,
		Location:    strings.Join(strings.Fields(j.Location), " "),
		Remote:      true,
		URL:         page,
		ApplyURL:    apply,
		PostedAt:    posted,
		Description: lead + srcutil.Sections(room, srcutil.Section{HTML: j.Description}),
		SeenAt:      seen,
	}, nil
}

// posted is the row's epoch, else its RFC 3339 date, else the zero time.
func (j job) posted() (time.Time, error) {
	if j.Epoch != nil && *j.Epoch > 0 {
		return time.Unix(*j.Epoch, 0).UTC(), nil
	}
	t, err := srcutil.ParseTime("date", strings.TrimSpace(j.Date))
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}
