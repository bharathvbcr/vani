// Package workable lists postings from a public Workable account
// (GET https://apply.workable.com/api/v1/widget/accounts/{slug}).
// The widget has no description. Each job is read from
// /api/v2/accounts/{slug}/jobs/{shortcode}, and any detail that cannot be
// read fails the board.
package workable

import (
	"bytes"
	"context"
	"encoding/json"
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

// ErrBoardNotFound is returned when Workable has no such account.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

const widgetURL = "https://apply.workable.com/api/v1/widget/accounts/"

// maxDetailBody caps one job detail. The widget list is capped by srcutil.MaxBody.
var maxDetailBody int64 = 2 << 20

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the Workable source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (*source) ATS() posting.ATS { return posting.Workable }

type response struct {
	Name string    `json:"name"`
	Jobs []listJob `json:"jobs"`
}

type listJob struct {
	Title          string `json:"title"`
	Shortcode      string `json:"shortcode"`
	URL            string `json:"url"`
	ApplicationURL string `json:"application_url"`
	Department     string `json:"department"`
	Telecommuting  bool   `json:"telecommuting"`
	City           string `json:"city"`
	State          string `json:"state"`
	Country        string `json:"country"`
}

type detail struct {
	Shortcode    string          `json:"shortcode"`
	Title        string          `json:"title"`
	Remote       bool            `json:"remote"`
	Workplace    string          `json:"workplace"`
	IsInternal   bool            `json:"isInternal"`
	State        string          `json:"state"`
	Published    string          `json:"published"`
	Description  string          `json:"description"`
	Requirements string          `json:"requirements"`
	Benefits     string          `json:"benefits"`
	Department   json.RawMessage `json:"department"`
	Location     place           `json:"location"`
	Locations    []struct {
		place
		Hidden bool `json:"hidden"`
	} `json:"locations"`
}

type place struct {
	City    string `json:"city"`
	Region  string `json:"region"`
	Country string `json:"country"`
}

func accountURL(token string) string {
	return widgetURL + url.PathEscape(token)
}

func jobURL(token, code string) string {
	return "https://apply.workable.com/api/v2/accounts/" + url.PathEscape(token) + "/jobs/" + url.PathEscape(code)
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckBoard(b, posting.Workable); err != nil {
		return nil, err
	}
	var resp response
	if err := srcutil.GetJSON(ctx, s.d, accountURL(b.Token), &resp); err != nil {
		return nil, fmt.Errorf("workable %q: %w", b.Token, err)
	}
	if resp.Jobs == nil {
		return nil, fmt.Errorf("workable %q: payload has no jobs array", b.Token)
	}
	if err := srcutil.CheckCount(len(resp.Jobs)); err != nil {
		return nil, fmt.Errorf("workable %q: %w", b.Token, err)
	}
	seenID := map[string]bool{}
	for i, j := range resp.Jobs {
		id := strings.TrimSpace(j.Shortcode)
		if _, err := posting.WorkableKey(b.Token, id); err != nil {
			return nil, fmt.Errorf("workable %q: job %d: %w", b.Token, i, err)
		}
		if seenID[strings.ToLower(id)] {
			return nil, fmt.Errorf("workable %q: job %d: duplicate shortcode %q", b.Token, i, id)
		}
		seenID[strings.ToLower(id)] = true
	}
	company := b.Company
	if strings.TrimSpace(company) == "" {
		company = strings.TrimSpace(resp.Name)
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(resp.Jobs))
	for i, j := range resp.Jobs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var d detail
		if err := srcutil.GetJSONLimit(ctx, s.d, jobURL(b.Token, strings.TrimSpace(j.Shortcode)), maxDetailBody, &d); err != nil {
			if errors.Is(err, srcutil.ErrBoardNotFound) {
				err = fmt.Errorf("job %s is not on the board", strings.TrimSpace(j.Shortcode))
			}
			return nil, fmt.Errorf("workable %q: job %d: %w", b.Token, i, err)
		}
		p, keep, err := d.posting(b, j, company, seen)
		if err != nil {
			return nil, fmt.Errorf("workable %q: job %d: %w", b.Token, i, err)
		}
		if keep {
			out = append(out, p)
		}
	}
	return out, nil
}

func (d detail) posting(b sources.Board, list listJob, company string, seen time.Time) (posting.Posting, bool, error) {
	state := strings.TrimSpace(d.State)
	if d.IsInternal || (state != "" && !strings.EqualFold(state, "published")) {
		return posting.Posting{}, false, nil
	}
	id := strings.TrimSpace(list.Shortcode)
	if got := strings.TrimSpace(d.Shortcode); got != "" && !strings.EqualFold(got, id) {
		return posting.Posting{}, false, fmt.Errorf("detail shortcode %q does not match %q", got, id)
	}
	key, err := posting.WorkableKey(b.Token, id)
	if err != nil {
		return posting.Posting{}, false, err
	}
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, false, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := strings.TrimSpace(d.Title)
	if title == "" {
		title = strings.TrimSpace(list.Title)
	}
	if title == "" {
		return posting.Posting{}, false, errors.New("empty title")
	}
	rawURL := strings.TrimSpace(list.URL)
	if rawURL == "" {
		return posting.Posting{}, false, errors.New("empty url")
	}
	if err := srcutil.CheckURL("url", rawURL); err != nil {
		return posting.Posting{}, false, err
	}
	apply := strings.TrimSpace(list.ApplicationURL)
	if err := srcutil.CheckURL("application_url", apply); err != nil {
		return posting.Posting{}, false, err
	}
	posted, err := srcutil.ParseTime("published", strings.TrimSpace(d.Published))
	if err != nil {
		return posting.Posting{}, false, err
	}
	dept, err := departmentText(d.Department)
	if err != nil {
		return posting.Posting{}, false, err
	}
	if dept == "" {
		dept = strings.TrimSpace(list.Department)
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.Workable,
		Company:     company,
		Title:       srcutil.Title(title),
		Location:    d.where(list),
		Remote:      d.remote(list),
		Department:  dept,
		URL:         rawURL,
		ApplyURL:    apply,
		PostedAt:    posted,
		Description: htmlSections(d.Description, d.Requirements, d.Benefits),
		SeenAt:      seen,
	}, true, nil
}

func (d detail) remote(list listJob) bool {
	if d.Remote || remoteWord(d.Workplace) {
		return true
	}
	if strings.TrimSpace(d.Workplace) == "" && !d.Remote {
		return list.Telecommuting
	}
	return false
}

func (d detail) where(list listJob) string {
	for _, loc := range d.Locations {
		if loc.Hidden {
			continue
		}
		if p := loc.place.join(); p != "" {
			return p
		}
	}
	if p := d.Location.join(); p != "" {
		return p
	}
	return joinComma(list.City, list.State, list.Country)
}

func (p place) join() string { return joinComma(p.City, p.Region, p.Country) }

func departmentText(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return strings.TrimSpace(one), nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return "", errors.New("department is not text")
	}
	return joinComma(many...), nil
}

func remoteWord(v string) bool {
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' }) {
		if strings.EqualFold(strings.TrimSpace(p), "remote") {
			return true
		}
	}
	return false
}

func joinComma(parts ...string) string {
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
	return strings.Join(out, ", ")
}

func htmlSections(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		text := htmltext.Text(p, srcutil.MaxDescription)
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(text)
		if b.Len() >= srcutil.MaxDescription {
			break
		}
	}
	return htmltext.Plain(b.String(), srcutil.MaxDescription)
}
