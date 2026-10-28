// Package rippling lists postings from a public Rippling board
// (GET https://api.rippling.com/platform/api/ats/v1/board/{slug}/jobs).
// The list has no description. Each job's detail is fetched, and any detail
// that cannot be read fails the board: a partial list would age the missing
// jobs out.
package rippling

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

// ErrBoardNotFound is returned when Rippling has no such board.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

const baseURL = "https://api.rippling.com/platform/api/ats/v1/board/"

// maxDetailBody caps one job detail. The list is capped by srcutil.MaxBody.
var maxDetailBody int64 = 2 << 20

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the Rippling source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (*source) ATS() posting.ATS { return posting.Rippling }

type listJob struct {
	UUID       string `json:"uuid"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Department struct {
		Label string `json:"label"`
	} `json:"department"`
	WorkLocation struct {
		Label string `json:"label"`
	} `json:"workLocation"`
}

type detail struct {
	UUID       string   `json:"uuid"`
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	Unlisted   bool     `json:"unlistedFromSearch"`
	CreatedOn  string   `json:"createdOn"`
	Company    string   `json:"companyName"`
	Locations  []string `json:"workLocations"`
	Department struct {
		Name string `json:"name"`
	} `json:"department"`
	Description struct {
		Company string `json:"company"`
		Role    string `json:"role"`
	} `json:"description"`
}

func jobsURL(token string) string {
	return baseURL + url.PathEscape(token) + "/jobs"
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckBoard(b, posting.Rippling); err != nil {
		return nil, err
	}
	var jobs []listJob
	if err := srcutil.GetJSON(ctx, s.d, jobsURL(b.Token), &jobs); err != nil {
		return nil, fmt.Errorf("rippling %q: %w", b.Token, err)
	}
	if jobs == nil {
		return nil, fmt.Errorf("rippling %q: payload is not a jobs array", b.Token)
	}
	if err := srcutil.CheckCount(len(jobs)); err != nil {
		return nil, fmt.Errorf("rippling %q: %w", b.Token, err)
	}
	// Rippling lists a job once per work location, each row with the same
	// uuid (its own board, 2026-10-27: 134 of 654 rows). Rows sharing a uuid
	// are one job only when they agree on all but the location; the labels
	// are kept as the location's fallback.
	var uniq []listJob
	at := map[string]int{}
	places := map[string][]string{}
	for i, j := range jobs {
		id := strings.TrimSpace(j.UUID)
		if _, err := posting.RipplingKey(b.Token, id); err != nil {
			return nil, fmt.Errorf("rippling %q: job %d: %w", b.Token, i, err)
		}
		k := strings.ToLower(id)
		if n, ok := at[k]; ok {
			if !sameJob(uniq[n], j) {
				return nil, fmt.Errorf("rippling %q: job %d: uuid %q repeats with a different title, link or department", b.Token, i, id)
			}
		} else {
			at[k] = len(uniq)
			uniq = append(uniq, j)
		}
		places[k] = append(places[k], j.WorkLocation.Label)
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(uniq))
	for i, j := range uniq {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var d detail
		endpoint := jobsURL(b.Token) + "/" + url.PathEscape(strings.TrimSpace(j.UUID))
		if err := srcutil.GetJSONLimit(ctx, s.d, endpoint, maxDetailBody, &d); err != nil {
			if errors.Is(err, srcutil.ErrBoardNotFound) {
				err = fmt.Errorf("job %s is not on the board", strings.TrimSpace(j.UUID))
			}
			return nil, fmt.Errorf("rippling %q: job %d: %w", b.Token, i, err)
		}
		p, keep, err := d.posting(b, j, places[strings.ToLower(strings.TrimSpace(j.UUID))], seen)
		if err != nil {
			return nil, fmt.Errorf("rippling %q: job %d: %w", b.Token, i, err)
		}
		if keep {
			out = append(out, p)
		}
	}
	return out, nil
}

// sameJob reports whether two list rows with one uuid describe the same job:
// the same uuid as written, title, link and department.
func sameJob(a, b listJob) bool {
	return strings.TrimSpace(a.UUID) == strings.TrimSpace(b.UUID) &&
		strings.TrimSpace(a.Name) == strings.TrimSpace(b.Name) &&
		strings.TrimSpace(a.URL) == strings.TrimSpace(b.URL) &&
		strings.TrimSpace(a.Department.Label) == strings.TrimSpace(b.Department.Label)
}

// posting builds the job's posting from its detail, falling back to the list
// row and listPlaces, the location labels of every row the job was listed in.
func (d detail) posting(b sources.Board, list listJob, listPlaces []string, seen time.Time) (posting.Posting, bool, error) {
	if d.Unlisted {
		return posting.Posting{}, false, nil
	}
	id := strings.TrimSpace(list.UUID)
	if got := strings.TrimSpace(d.UUID); got != "" && !strings.EqualFold(got, id) {
		return posting.Posting{}, false, fmt.Errorf("detail uuid %q does not match %q", got, id)
	}
	key, err := posting.RipplingKey(b.Token, id)
	if err != nil {
		return posting.Posting{}, false, err
	}
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, false, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := strings.TrimSpace(d.Name)
	if title == "" {
		title = strings.TrimSpace(list.Name)
	}
	if title == "" {
		return posting.Posting{}, false, errors.New("empty title")
	}
	rawURL := strings.TrimSpace(d.URL)
	if rawURL == "" {
		rawURL = strings.TrimSpace(list.URL)
	}
	if rawURL == "" {
		return posting.Posting{}, false, errors.New("empty url")
	}
	if err := srcutil.CheckURL("url", rawURL); err != nil {
		return posting.Posting{}, false, err
	}
	posted, err := srcutil.ParseTime("createdOn", strings.TrimSpace(d.CreatedOn))
	if err != nil {
		return posting.Posting{}, false, err
	}
	loc := joinParts("; ", d.Locations)
	if loc == "" {
		loc = joinParts("; ", listPlaces)
	}
	dept := strings.TrimSpace(d.Department.Name)
	if dept == "" {
		dept = strings.TrimSpace(list.Department.Label)
	}
	desc := htmltext.Text(d.Description.Role, srcutil.MaxDescription)
	if desc == "" {
		desc = htmltext.Text(d.Description.Company, srcutil.MaxDescription)
	}
	company := strings.TrimSpace(d.Company)
	if company == "" {
		company = b.Company
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.Rippling,
		Company:     company,
		Title:       srcutil.Title(title),
		Location:    loc,
		Department:  dept,
		URL:         rawURL,
		PostedAt:    posted,
		Description: desc,
		SeenAt:      seen,
	}, true, nil
}

func joinParts(sep string, parts []string) string {
	var out []string
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[strings.ToLower(p)] {
			continue
		}
		seen[strings.ToLower(p)] = true
		out = append(out, p)
	}
	return strings.Join(out, sep)
}
