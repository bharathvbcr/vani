// Package bamboohr lists postings from a BambooHR careers page. The list is
// GET https://{token}.bamboohr.com/careers/list and has no description, so
// each posting's body is read from GET https://{token}.bamboohr.com/careers/{id}/detail,
// one at a time, spaced paceEvery apart. A detail that cannot be read fails
// the board: a partial list would age the missing postings out. At most
// maxDetails postings are read, and a board listing more fails before any
// detail request.
//
// robots.txt of sortitt.bamboohr.com (GET 2026-10-27) disallows only
// /jobs/embed.php and /jobs/embed2.php and sets no Crawl-delay; /careers is
// allowed. A list request sent as "Vani/0.1 (+personal job-listing reader)"
// answered 200 JSON (the answer varies on User-Agent for a tenant that does
// not exist).
//
// BambooHR does not answer 404 for a board it does not serve: it redirects the
// list request away from /careers. A tenant that does not exist goes to
// https://www.bamboohr.com, and the redirect target of a tenant whose careers
// page is off, expired or suspended is a page on its own host (/login.php,
// /settings/account/...). Any answer that is not the careers page is
// ErrBoardNotFound, with where it went in the error, because the destinations
// mean different things to the owner ("temporarily_suspended" is a board to
// leave alone). A redirect off the board's host is never followed: the egress
// client refuses the hop unless that host is allowed, and even then the page it
// lands on is not a list.
package bamboohr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// ErrBoardNotFound is returned when BambooHR does not serve the board: no such
// tenant, or its careers page is turned off, expired or suspended.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

const (
	// maxDetails is the most postings one board may have, each costing a
	// detail request; a board over it fails before any is made.
	maxDetails = srcutil.MaxPacedRequests
	// paceEvery spaces a board's requests. The robots.txt sets no Crawl-delay;
	// this is on top of the egress client's own per-host interval.
	paceEvery = 500 * time.Millisecond
)

// maxListBody and maxDetailBody cap the list and one detail. The list holds
// no description (35 postings were 11 KB); a detail is one job's text.
var (
	maxListBody   int64 = 8 << 20
	maxDetailBody int64 = 2 << 20
)

// Options configures NewWith.
type Options struct {
	// Wait spaces a board's requests (srcutil.Pacer.Sleep); nil is
	// srcutil.Sleep. A field only so tests need not wait in real time.
	Wait func(ctx context.Context, d time.Duration) error
}

type source struct {
	d    egress.Doer
	now  func() time.Time
	wait func(ctx context.Context, d time.Duration) error
}

// New returns the BambooHR source, sending requests through d.
func New(d egress.Doer) sources.Source {
	return &source{d: d, now: time.Now, wait: srcutil.Sleep}
}

// NewWith is New with the options of opt.
func NewWith(d egress.Doer, opt Options) sources.Source {
	wait := opt.Wait
	if wait == nil {
		wait = srcutil.Sleep
	}
	return &source{d: d, now: time.Now, wait: wait}
}

func (*source) ATS() posting.ATS { return posting.BambooHR }

type listResponse struct {
	Meta struct {
		TotalCount *int `json:"totalCount"`
	} `json:"meta"`
	Result []listJob `json:"result"`
}

type listJob struct {
	ID              string `json:"id"`
	Name            string `json:"jobOpeningName"`
	DepartmentLabel string `json:"departmentLabel"`
	StatusLabel     string `json:"employmentStatusLabel"`
	IsRemote        *bool  `json:"isRemote"`
	LocationType    text   `json:"locationType"`
	Location        struct {
		City  string `json:"city"`
		State string `json:"state"`
	} `json:"location"`
}

type detailResponse struct {
	Result struct {
		JobOpening *jobOpening `json:"jobOpening"`
	} `json:"result"`
}

type jobOpening struct {
	ShareURL        string `json:"jobOpeningShareUrl"`
	Name            string `json:"jobOpeningName"`
	Status          string `json:"jobOpeningStatus"`
	DepartmentLabel string `json:"departmentLabel"`
	StatusLabel     string `json:"employmentStatusLabel"`
	Description     string `json:"description"`
	DatePosted      string `json:"datePosted"`
	Compensation    text   `json:"compensation"`
	LocationType    text   `json:"locationType"`
	Location        struct {
		City    string `json:"city"`
		State   string `json:"state"`
		Country string `json:"addressCountry"`
	} `json:"location"`
}

// text is a field BambooHR sends as a string or a number (locationType is the
// string "1" on the list) and as null or something else when unset: the
// string or the number's digits, and "" for anything else, so a field that is
// not what it was seen to be costs the posting that field, not the board.
type text string

func (t *text) UnmarshalJSON(raw []byte) error {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		*t = text(s)
		return nil
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		*t = text(n.String())
	}
	return nil
}

func host(token string) string { return token + ".bamboohr.com" }

func listURL(token string) string { return "https://" + host(token) + "/careers/list" }

func detailURL(token, id string) string {
	return "https://" + host(token) + "/careers/" + url.PathEscape(id) + "/detail"
}

// tracker remembers where the list request ended: the final URL of the
// response, which is not the one asked for when the client followed a
// redirect, and the Location of a redirect it did not.
type tracker struct {
	d        egress.Doer
	final    *url.URL
	location string
}

func (t *tracker) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if t.d == nil {
		return nil, errors.New("sources: no http doer configured")
	}
	resp, err := t.d.Do(ctx, req)
	if resp != nil {
		if resp.Request != nil {
			t.final = resp.Request.URL
		}
		t.location = resp.Header.Get("Location")
	}
	return resp, err
}

// movedAway reports whether the list request was sent away from the careers
// page, and the host and path it went to ("" when only the fact is known). It
// is the redirect itself that says so, whichever way the client met it: it
// followed it (the answer came from another URL), it refused the hop to a
// host that is not allowed (a *url.Error wrapping egress.ErrNotAllowed; the
// request's own host being refused is a bare error and not this), or it
// returned the 3xx unfollowed.
func (t *tracker) movedAway(token string, err error) (dest string, moved bool) {
	if t.final != nil && !(strings.EqualFold(t.final.Hostname(), host(token)) && (t.final.Path == "/careers" || strings.HasPrefix(t.final.Path, "/careers/"))) {
		return t.final.Hostname() + t.final.Path, true
	}
	var se *egress.StatusError
	if errors.As(err, &se) && se.Code >= 300 && se.Code < 400 {
		if u, perr := url.Parse(t.location); perr == nil && u.Hostname() != "" {
			return u.Hostname() + u.Path, true
		}
		return t.location, true
	}
	var ue *url.Error
	if errors.As(err, &ue) && errors.Is(err, egress.ErrNotAllowed) {
		return "a host that is not allowed", true
	}
	return "", false
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckTenant(b, posting.BambooHR); err != nil {
		return nil, err
	}
	pace := &srcutil.Pacer{Every: paceEvery, Budget: maxDetails * paceEvery, Sleep: s.wait}
	if err := pace.Wait(ctx); err != nil {
		return nil, err
	}
	var resp listResponse
	tr := &tracker{d: s.d}
	err := srcutil.GetJSONLimit(ctx, tr, listURL(b.Token), maxListBody, &resp)
	// The redirect is read before the error, and even when there is none: a
	// followed one usually lands on an HTML page, so the failure is a decode
	// error that says nothing of it, and a page that does decode is another
	// board's, not this one's.
	if dest, moved := tr.movedAway(b.Token, err); moved {
		return nil, fmt.Errorf("bamboohr %q: %w: the careers list went to %q (no such board, or its careers page is off, expired or suspended)", b.Token, ErrBoardNotFound, dest)
	}
	if err != nil {
		return nil, fmt.Errorf("bamboohr %q: %w", b.Token, err)
	}
	if resp.Result == nil {
		return nil, fmt.Errorf("bamboohr %q: payload has no result array", b.Token)
	}
	// The count BambooHR gives is the proof that the list is the whole board.
	if resp.Meta.TotalCount == nil {
		return nil, fmt.Errorf("bamboohr %q: payload has no meta.totalCount, so the list cannot be shown whole", b.Token)
	}
	if n := *resp.Meta.TotalCount; n != len(resp.Result) {
		return nil, fmt.Errorf("bamboohr %q: list holds %d postings but meta.totalCount is %d", b.Token, len(resp.Result), n)
	}
	if err := srcutil.CheckCount(len(resp.Result)); err != nil {
		return nil, fmt.Errorf("bamboohr %q: %w", b.Token, err)
	}
	if len(resp.Result) > maxDetails {
		return nil, fmt.Errorf("bamboohr %q: %w: %d postings need a detail request each, over %d", b.Token, srcutil.ErrTooManyPostings, len(resp.Result), maxDetails)
	}
	keys := make(map[posting.Key]int, len(resp.Result))
	for i, j := range resp.Result {
		key, err := posting.BambooHRKey(b.Token, j.ID)
		if err != nil {
			return nil, fmt.Errorf("bamboohr %q: job %d: %w", b.Token, i, err)
		}
		if k, dup := keys[key]; dup {
			return nil, fmt.Errorf("bamboohr %q: jobs %d and %d share key %s", b.Token, k, i, key)
		}
		keys[key] = i
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(resp.Result))
	for i, j := range resp.Result {
		if err := pace.Wait(ctx); err != nil {
			return nil, fmt.Errorf("bamboohr %q: job %d: %w", b.Token, i, err)
		}
		id := strings.TrimSpace(j.ID)
		var d detailResponse
		if err := srcutil.GetJSONLimit(ctx, s.d, detailURL(b.Token, id), maxDetailBody, &d); err != nil {
			if errors.Is(err, srcutil.ErrBoardNotFound) {
				err = fmt.Errorf("job %s is not on the board", id)
			}
			return nil, fmt.Errorf("bamboohr %q: job %d: %w", b.Token, i, err)
		}
		p, keep, err := d.Result.JobOpening.posting(b, j, seen)
		if err != nil {
			return nil, fmt.Errorf("bamboohr %q: job %d: %w", b.Token, i, err)
		}
		if keep {
			out = append(out, p)
		}
	}
	return out, nil
}

// posting maps one detail. A detail that is null, or has no jobOpeningShareUrl
// (every real one has), is an answer that is not the posting: a 200 that
// decodes to nothing must not pass for a read posting.
func (jo *jobOpening) posting(b sources.Board, l listJob, seen time.Time) (posting.Posting, bool, error) {
	if jo == nil || strings.TrimSpace(jo.ShareURL) == "" {
		return posting.Posting{}, false, errors.New("detail has no jobOpening with a jobOpeningShareUrl")
	}
	if st := strings.TrimSpace(jo.Status); st != "" && !strings.EqualFold(st, "open") {
		return posting.Posting{}, false, nil
	}
	key, err := posting.BambooHRKey(b.Token, l.ID)
	if err != nil {
		return posting.Posting{}, false, err
	}
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, false, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := srcutil.Title(l.Name)
	if title == "" {
		title = srcutil.Title(jo.Name)
	}
	if title == "" {
		return posting.Posting{}, false, errors.New("empty title")
	}
	rawURL := strings.TrimSpace(jo.ShareURL)
	if err := srcutil.CheckURL("jobOpeningShareUrl", rawURL); err != nil {
		return posting.Posting{}, false, err
	}
	posted, err := parseDate("datePosted", jo.DatePosted)
	if err != nil {
		return posting.Posting{}, false, err
	}
	dept := srcutil.Title(jo.DepartmentLabel)
	if dept == "" {
		dept = srcutil.Title(l.DepartmentLabel)
	}
	site := workSite(string(l.LocationType))
	if site == "" {
		site = workSite(string(jo.LocationType))
	}
	if site == "" && l.IsRemote != nil && *l.IsRemote {
		site = "remote"
	}
	status := jo.StatusLabel
	if strings.TrimSpace(status) == "" {
		status = l.StatusLabel
	}
	where := srcutil.JoinDistinct(", ", jo.Location.City, jo.Location.State, jo.Location.Country)
	if where == "" {
		where = srcutil.JoinDistinct(", ", l.Location.City, l.Location.State)
	}
	lead, room := srcutil.Lead(srcutil.CompensationLine(string(jo.Compensation)), srcutil.RoleLine(srcutil.EmploymentType(status), site))
	return posting.Posting{
		Key:         key,
		ATS:         posting.BambooHR,
		Company:     b.Company,
		Title:       title,
		Location:    where,
		Remote:      site == "remote",
		Department:  dept,
		URL:         rawURL,
		PostedAt:    posted,
		Description: lead + srcutil.Sections(room, srcutil.Section{HTML: jo.Description}),
		SeenAt:      seen,
	}, true, nil
}

// workSite is the role-line word for locationType, BambooHR's "0" (on site),
// "1" (remote) and "2" (hybrid); another value says nothing.
func workSite(locationType string) string {
	switch strings.TrimSpace(locationType) {
	case "0":
		return "on-site"
	case "1":
		return "remote"
	case "2":
		return "hybrid"
	}
	return ""
}

// parseDate reads datePosted, a calendar date ("2024-01-16"); empty is the zero time.
func parseDate(field, s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s %q is not a YYYY-MM-DD date", field, s)
	}
	return t, nil
}
