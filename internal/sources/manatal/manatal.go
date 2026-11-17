// Package manatal lists postings from a Manatal career page
// (https://www.careers-page.com/{token}) through Manatal's public Open API,
// GET https://api.manatal.com/open/v3/career-page/{token}/jobs/?page=N&page_size=100.
//
// Performacentric (GET 2026-11-17, 40 jobs) answered 200 application/json with
// count, next, previous and results, each carrying id, hash, position_name,
// description (HTML), location_display, city, state, country, is_remote,
// contract_details, is_salary_visible, salary_min, salary_max and
// currency_code. A missing career page answered 404
// {"detail":"No ClientPortalSettings matches the given query."}.
//
// Paging: page_size=100 is honoured; when next is non-empty on a multi-page
// board it points at core.api.manatal.com, so this source advances page=1, 2,
// ... on api.manatal.com instead of following next, and fails the board if the
// stated count and the rows read disagree.
//
// The key id is the job's hash ("RY787VY6"), which is the path segment of the
// public job URL https://www.careers-page.com/{token}/job/{hash} and the id
// freehire's manatal source records. Pay numbers stay present in the JSON when
// is_salary_visible is false; this source emits a pay line only when
// is_salary_visible is true.
package manatal

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// ErrBoardNotFound is returned when Manatal has no career page by that slug.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

const (
	baseURL  = "https://api.manatal.com/open/v3/career-page/"
	pageSize = 100
	maxPages = srcutil.MaxPostings/pageSize + 10
)

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the Manatal source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (*source) ATS() posting.ATS { return posting.Manatal }

type pageDoc struct {
	Count   *int    `json:"count"`
	Next    *string `json:"next"`
	Results *[]item `json:"results"`
}

type item struct {
	Hash          string `json:"hash"`
	Title         string `json:"position_name"`
	Description   string `json:"description"`
	Location      string `json:"location_display"`
	City          string `json:"city"`
	State         string `json:"state"`
	Country       string `json:"country"`
	IsRemote      *bool  `json:"is_remote"`
	Contract      string `json:"contract_details"`
	SalaryVisible *bool  `json:"is_salary_visible"`
	SalaryMin     string `json:"salary_min"`
	SalaryMax     string `json:"salary_max"`
	Currency      string `json:"currency_code"`
}

func listURL(token string, page int) string {
	return baseURL + url.PathEscape(token) + "/jobs/?page=" + strconv.Itoa(page) + "&page_size=" + strconv.Itoa(pageSize)
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckBoard(b, posting.Manatal); err != nil {
		return nil, err
	}
	if b.Token == "." || b.Token == ".." || strings.Contains(b.Token, "..") || strings.ContainsAny(b.Token, " \t\r\n?#/\\") {
		return nil, fmt.Errorf("manatal %q: invalid board token", b.Token)
	}
	out := make([]posting.Posting, 0)
	type where struct{ page, item int }
	keys := map[posting.Key]where{}
	rows := srcutil.NewRows(ctx, b, 0)
	firstTotal := -1
	for page := 1; page <= maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var doc pageDoc
		if err := srcutil.GetJSON(ctx, s.d, listURL(b.Token, page), &doc); err != nil {
			return nil, fmt.Errorf("manatal %q: %w", b.Token, err)
		}
		items, total, hasNext, err := doc.checked()
		if err != nil {
			return nil, fmt.Errorf("manatal %q: %w", b.Token, err)
		}
		if firstTotal < 0 {
			firstTotal = total
		} else if total != firstTotal {
			return nil, fmt.Errorf("manatal %q: count changed from %d to %d on page %d", b.Token, firstTotal, total, page)
		}
		if err := srcutil.CheckCount(len(items)); err != nil {
			return nil, fmt.Errorf("manatal %q: %w", b.Token, err)
		}
		if err := srcutil.CheckCount(total); err != nil {
			return nil, fmt.Errorf("manatal %q: %w", b.Token, err)
		}
		if err := srcutil.CheckCount(len(out) + rows.Skipped() + len(items)); err != nil {
			return nil, fmt.Errorf("manatal %q: %w", b.Token, err)
		}
		rows.Add(len(items))
		seen := s.now()
		for i, it := range items {
			p, err := it.posting(b, seen)
			if err != nil {
				if ferr := rows.Bad(err); ferr != nil {
					return nil, fmt.Errorf("manatal %q: page %d item %d: %w", b.Token, page, i, ferr)
				}
				continue
			}
			if prev, dup := keys[p.Key]; dup {
				return nil, fmt.Errorf("manatal %q: page %d item %d and page %d item %d share key %s", b.Token, prev.page, prev.item, page, i, p.Key)
			}
			keys[p.Key] = where{page, i}
			out = append(out, p)
		}
		if hasNext && len(items) == 0 {
			return nil, fmt.Errorf("manatal %q: empty page before the list ended", b.Token)
		}
		read := len(out) + rows.Skipped()
		if read > total || (hasNext && read >= total) || (!hasNext && read != total) {
			return nil, fmt.Errorf("manatal %q: read %d postings, board says %d (more pages: %v)", b.Token, read, total, hasNext)
		}
		if !hasNext {
			if err := rows.Done(); err != nil {
				return nil, fmt.Errorf("manatal %q: %w", b.Token, err)
			}
			return out, nil
		}
		if page == maxPages {
			return nil, fmt.Errorf("manatal %q: more than %d pages", b.Token, maxPages)
		}
	}
	return nil, fmt.Errorf("manatal %q: more than %d pages", b.Token, maxPages)
}

func (p pageDoc) checked() ([]item, int, bool, error) {
	if p.Count == nil {
		return nil, 0, false, errors.New("payload is missing count")
	}
	if p.Results == nil {
		return nil, 0, false, errors.New("payload has no results array")
	}
	if *p.Count < 0 {
		return nil, 0, false, fmt.Errorf("count %d is negative", *p.Count)
	}
	hasNext := p.Next != nil && strings.TrimSpace(*p.Next) != ""
	return *p.Results, *p.Count, hasNext, nil
}

func (it item) posting(b sources.Board, seen time.Time) (posting.Posting, error) {
	hash := strings.TrimSpace(it.Hash)
	if !idOK(hash) {
		return posting.Posting{}, fmt.Errorf("hash %q is not a posting hash", it.Hash)
	}
	key, err := posting.ManatalKey(b.Token, hash)
	if err != nil {
		return posting.Posting{}, err
	}
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := srcutil.Title(htmltext.Text(it.Title, srcutil.MaxTitle))
	if title == "" {
		return posting.Posting{}, errors.New("empty title")
	}
	rawURL := "https://www.careers-page.com/" + url.PathEscape(b.Token) + "/job/" + hash
	if err := srcutil.CheckURL("url", rawURL); err != nil {
		return posting.Posting{}, err
	}
	desc, err := it.description()
	if err != nil {
		return posting.Posting{}, err
	}
	where := it.where()
	remote := (it.IsRemote != nil && *it.IsRemote) || saysRemote(where)
	return posting.Posting{
		Key:         key,
		ATS:         posting.Manatal,
		Company:     companyName(b),
		Title:       title,
		Location:    where,
		Remote:      remote,
		URL:         rawURL,
		Description: desc,
		SeenAt:      seen,
	}, nil
}

func idOK(id string) bool {
	if id == "" || len(id) > 80 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func (it item) where() string {
	if loc := strings.TrimSpace(it.Location); loc != "" {
		return loc
	}
	return srcutil.JoinDistinct(", ", it.City, it.State, it.Country)
}

func companyName(b sources.Board) string {
	if name := strings.TrimSpace(b.Company); name != "" {
		return name
	}
	return b.Token
}

func saysRemote(s string) bool {
	for _, f := range strings.Fields(strings.ToLower(s)) {
		if strings.Trim(f, ",.") == "remote" {
			return true
		}
	}
	return false
}

func parsePayBound(field, raw string) (*float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil, fmt.Errorf("%s %q: %w", field, raw, err)
	}
	return &v, nil
}

func (it item) description() (string, error) {
	var payLine string
	if it.SalaryVisible != nil && *it.SalaryVisible {
		lo, err := parsePayBound("salary_min", it.SalaryMin)
		if err != nil {
			return "", err
		}
		hi, err := parsePayBound("salary_max", it.SalaryMax)
		if err != nil {
			return "", err
		}
		payLine = srcutil.PayLine(it.Currency, lo, hi, "")
	}
	workMode := ""
	if it.IsRemote != nil && *it.IsRemote {
		workMode = "remote"
	}
	lead, room := srcutil.Lead(
		payLine,
		srcutil.RoleLine(srcutil.EmploymentType(it.Contract), workMode),
	)
	return lead + srcutil.Sections(room, srcutil.Section{HTML: it.Description}), nil
}
