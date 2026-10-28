// Package eightfold lists postings from an Eightfold careers site, the
// system behind apply.careers.microsoft.com, careers.qualcomm.com and
// explore.jobs.netflix.net. A site answers one of two APIs and refuses the
// other with 403: Microsoft and Qualcomm answer v2 with "Not authorized for
// PCSX", Netflix answers pcsx with "PCSX is not enabled for this user."
// (verified 2026-10-22).
//
// PCSX (Microsoft, Qualcomm), tried first:
//
//	GET https://{host}/api/pcsx/search?domain={domain}&query={term}&start={n}&num=10
//	GET https://{host}/api/pcsx/position_details?position_id={id}&domain={domain}&hl=en
//
// v2 (Netflix, Millennium), used when the first pcsx search of a board answers
// 403, or when the host's robots.txt disallows pcsx:
//
//	GET https://{host}/api/apply/v2/jobs?domain={domain}&query={term}&start={n}&num=10
//	GET https://{host}/api/apply/v2/jobs/{id}?domain={domain}
//
// The board token is the company's domain and Host its careers site. The
// robots.txt of Microsoft, Qualcomm, Netflix and Millennium Allow both APIs
// (read 2026-10-28), but the host's own robots.txt is read first anyway, once
// per board and before any API request, and a request is sent only to an API
// whose paths it permits: /api/pcsx/search and /api/pcsx/position_details for
// pcsx, /api/apply/v2/jobs and /api/apply/v2/jobs/ for v2. A host that
// disallows pcsx is read through v2 without a pcsx request; one that disallows
// both, or whose robots.txt cannot be read, fails the board with nothing
// requested. Search results carry no description (v2 rows have an empty
// job_description), so each posting costs a details request; a term over the
// board cap fails the board rather than being truncated.
//
// A board is read through one API end to end: ids of the two are never mixed,
// and a 403 after the first pcsx search answered is an outage, not a reason to
// switch. A v2 attempt that also fails reports both errors.
package eightfold

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/page"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	// MaxPostings bounds one board: each posting costs a details request.
	MaxPostings = 200
	// pageSize is what the search returns at most per request (asking for
	// 100 still returned 10 on 2026-10-19; v2 on 2026-10-22).
	pageSize = 10
	maxPages = MaxPostings/pageSize + 2

	// A v2 search page is 22 KB and a detail 8 KB for Netflix, most of the page
	// branding this source ignores: a few MiB bounds either far more tightly
	// than srcutil's 64 MiB default.
	v2SearchMaxBody = 4 << 20
	v2DetailMaxBody = 2 << 20

	// pcsxRoot and v2Root are the robots.txt prefixes each API's requests fall
	// under; v2Path is the v2 search path.
	pcsxRoot = "/api/pcsx/"
	v2Root   = "/api/apply/v2/"
	v2Path   = v2Root + "jobs"

	// paceEvery spaces a board's requests. Neither Microsoft's nor
	// Qualcomm's robots.txt sets a Crawl-delay (2026-10-20); Qualcomm's
	// search answered 429 with no Retry-After after a burst of about ten
	// unspaced requests, while its details and every Microsoft request
	// answered. The rate it tolerates was not measured: 2 s is a guess that
	// keeps a full board (21 search pages and 200 details, 221 requests) to
	// 7.4 min of waiting. It sits above egress's one-second per-host floor, so
	// the two never add: a paced wait already spans that interval.
	paceEvery = 2 * time.Second
	// paceBudget bounds a board's waiting, as page.BoardTimeout bounds a
	// careers page: 450 requests at paceEvery, twice a full board.
	paceBudget = 15 * time.Minute
)

// ErrShape is a response whose layout is not the one this source knows.
var ErrShape = errors.New("eightfold: unexpected response")

var (
	hostRE   = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)
	idRE     = regexp.MustCompile(`^[0-9]{1,20}$`)
	domainRE = hostRE
)

// errDetailGone is a position detail answering 404 under a search that
// answered: the position closed after it was listed.
var errDetailGone = errors.New("detail is gone (HTTP 404)")

// maxGone is how many of n listed positions may come back gone before the
// board fails: a tenth, at least one (workday's bound).
func maxGone(n int) int { return max(1, n/10) }

// Stats counts what one List did: the positions listed and the ones whose
// detail answered 404 (closed since listing).
type Stats struct {
	Listed int `json:"listed"`
	Gone   int `json:"gone,omitempty"`
	// V2 is set when the board was read through /api/apply/v2: its pcsx search
	// answered 403, or its host's robots.txt disallows pcsx.
	V2 bool `json:"v2,omitempty"`
}

// Options configures NewWith.
type Options struct {
	// Report, when set, receives each listed board's counts.
	Report func(sources.Board, Stats)
	// Wait spaces a board's requests (srcutil.Pacer.Sleep); nil is
	// srcutil.Sleep. A field only so tests need not wait in real time.
	Wait func(ctx context.Context, d time.Duration) error
}

type source struct {
	d   egress.Doer
	now func() time.Time
	// wait spaces requests (srcutil.Pacer.Sleep); a field only so tests need
	// not wait in real time.
	wait func(ctx context.Context, d time.Duration) error
	opt  *Options
}

// New returns the Eightfold source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now, wait: srcutil.Sleep} }

// NewWith is New with the board report of opt.
func NewWith(d egress.Doer, opt Options) sources.Source {
	wait := opt.Wait
	if wait == nil {
		wait = srcutil.Sleep
	}
	return &source{d: d, now: time.Now, wait: wait, opt: &opt}
}

func (*source) ATS() posting.ATS { return posting.Eightfold }

type envelope struct {
	Status *int            `json:"status"`
	Error  json.RawMessage `json:"error"`
	Data   json.RawMessage `json:"data"`
}

type searchData struct {
	Count     *int       `json:"count"`
	Positions []position `json:"positions"`
}

// position is one search row. WorkLocationOption is v2's: its detail
// answers null for the field the search row carries.
type position struct {
	ID                 json.Number `json:"id"`
	Name               string      `json:"name"`
	WorkLocationOption string      `json:"work_location_option"`
}

// listing is one position a search found: its id, and the work location
// option of its v2 row ("" for pcsx, whose detail has it).
type listing struct{ id, work string }

// api is the Eightfold API a board is read through: its URL prefix and which
// of the two it is.
type api struct {
	base string
	v2   bool
	// pace spaces every request of the board, whichever API it is read
	// through: one pacer per List call, shared by a pcsx board that falls back
	// to v2.
	pace *srcutil.Pacer
}

func pcsxAPI(host string, pace *srcutil.Pacer) api {
	return api{base: "https://" + host + "/api/pcsx/", pace: pace}
}
func v2API(host string, pace *srcutil.Pacer) api {
	return api{base: "https://" + host + "/api/apply/v2/", v2: true, pace: pace}
}

// v2Details is a v2 position detail: no envelope, snake_case fields.
type v2Details struct {
	ID                 json.Number `json:"id"`
	Name               string      `json:"name"`
	Locations          []string    `json:"locations"`
	CreatedTs          *int64      `json:"t_create"`
	Department         string      `json:"department"`
	JobDescription     string      `json:"job_description"`
	CanonicalURL       string      `json:"canonicalPositionUrl"`
	WorkLocationOption string      `json:"work_location_option"`
}

// details is v as the pcsx shape. Netflix's detail names its page with a
// "?microsite=netflix.com" query its search row lacks (seen 2026-10-22); the
// query is dropped so both are checked against the same page URL. The stored
// URL is built from the host and id, never taken from the response.
func (v v2Details) details() details {
	canonical := v.CanonicalURL
	if u, err := url.Parse(canonical); err == nil && u.RawQuery != "" {
		u.RawQuery = ""
		canonical = u.String()
	}
	return details{
		ID: v.ID, Name: v.Name, Locations: v.Locations, PostedTs: v.CreatedTs, Department: v.Department,
		JobDescription: v.JobDescription, PublicURL: canonical, WorkLocationOption: v.WorkLocationOption,
	}
}

// pcsxForbidden is the first pcsx search of a board answering HTTP 403: the
// site does not serve pcsx, and the one signal that tries v2.
type pcsxForbidden struct{ err error }

func (e *pcsxForbidden) Error() string { return e.err.Error() }
func (e *pcsxForbidden) Unwrap() error { return e.err }

type details struct {
	ID                 json.Number `json:"id"`
	Name               string      `json:"name"`
	Locations          []string    `json:"locations"`
	PostedTs           *int64      `json:"postedTs"`
	Department         string      `json:"department"`
	JobDescription     string      `json:"jobDescription"`
	PublicURL          string      `json:"publicUrl"`
	WorkLocationOption string      `json:"workLocationOption"`
	RoleType           []string    `json:"efcustomTextRoletype"`
	EmploymentType     []string    `json:"efcustomTextEmploymentType"`
	WorkSite           []string    `json:"efcustomTextWorkSite"`
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := srcutil.CheckBoard(b, posting.Eightfold); err != nil {
		return nil, err
	}
	domain := strings.ToLower(strings.TrimSpace(b.Token))
	host := strings.ToLower(strings.TrimSpace(b.Host))
	if !domainRE.MatchString(domain) {
		return nil, fmt.Errorf("eightfold %q: token must be the company domain", b.Token)
	}
	if !hostRE.MatchString(host) {
		return nil, fmt.Errorf("eightfold %q: host %q is not a hostname", b.Token, b.Host)
	}
	terms := b.SearchTerms()
	if len(terms) == 0 {
		return nil, fmt.Errorf("eightfold %q: the board needs at least one search", b.Token)
	}
	if err := sources.CheckFilters(posting.Eightfold, b.Filters); err != nil {
		return nil, fmt.Errorf("eightfold %q: %w", b.Token, err)
	}
	pace := &srcutil.Pacer{Every: paceEvery, Budget: paceBudget, Sleep: s.wait}
	// The host's robots.txt comes first, before a request to either API.
	acc, err := s.robots(ctx, pace, host)
	if err != nil {
		return nil, fmt.Errorf("eightfold %q: nothing was requested, robots.txt of %s could not be read: %w", b.Token, host, err)
	}
	a := pcsxAPI(host, pace)
	switch {
	case !acc.pcsx && !acc.v2:
		return nil, fmt.Errorf("eightfold %q: robots.txt of %s disallows both %s and %s; nothing was requested", b.Token, host, pcsxRoot, v2Root)
	case !acc.pcsx:
		a = v2API(host, pace) // no pcsx request: it is not permitted
	}
	var ids []listing
	have := map[string]bool{}
	for i, term := range terms {
		got, err := s.search(ctx, a, domain, term, b.Filters, i == 0 && !a.v2)
		var denied *pcsxForbidden
		if errors.As(err, &denied) {
			// The site does not serve pcsx. Read the whole board through v2 from
			// this term on; a v2 that fails too reports both.
			a = v2API(host, pace)
			got, err = s.searchV2(ctx, a, host, domain, term, b.Filters, denied, acc.v2)
		}
		if err != nil {
			return nil, fmt.Errorf("eightfold %q: search %q: %w", b.Token, term, err)
		}
		for _, l := range got {
			if !have[l.id] {
				have[l.id] = true
				ids = append(ids, l)
			}
		}
		if len(ids) > MaxPostings {
			return nil, fmt.Errorf("eightfold %q: %w: the board's searches together list more than %d postings; narrow them",
				b.Token, srcutil.ErrTooManyPostings, MaxPostings)
		}
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(ids))
	st := Stats{Listed: len(ids), V2: a.v2}
	if s.opt != nil && s.opt.Report != nil {
		defer func() { s.opt.Report(b, st) }()
	}
	allowed := maxGone(len(ids))
	for _, l := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p, err := s.detail(ctx, a, host, domain, l, b, seen)
		if errors.Is(err, errDetailGone) {
			// The position closed between the search and its detail; leaving it
			// out is the truth. Many at once is the detail endpoint failing. The
			// error is deliberately not ErrBoardNotFound: the search answered.
			if st.Gone++; st.Gone > allowed || st.Gone == len(ids) {
				return nil, fmt.Errorf("eightfold %q: %d of %d position details are gone (HTTP 404); the detail endpoint is not answering",
					b.Token, st.Gone, len(ids))
			}
			continue
		}
		if err != nil {
			// A partial listing would age healthy postings as absent.
			return nil, fmt.Errorf("eightfold %q: position %s: %w", b.Token, l.id, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// get fetches one PCSX endpoint and returns its data, checking the envelope.
func (s *source) get(ctx context.Context, pace *srcutil.Pacer, u string, v any) error {
	if err := pace.Wait(ctx); err != nil {
		return err
	}
	var env envelope
	if err := srcutil.GetJSON(ctx, s.d, u, &env); err != nil {
		return err
	}
	if env.Status == nil || *env.Status != 200 {
		return fmt.Errorf("%w: envelope status %v, error %s", ErrShape, env.Status, env.Error)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return fmt.Errorf("%w: no data", ErrShape)
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		return fmt.Errorf("%w: data: %v", ErrShape, err)
	}
	return nil
}

// getV2 fetches one v2 endpoint, which answers its data at the top level with
// no envelope, and decodes it into v. A body that is not the JSON this source
// reads is ErrShape; a 404 is srcutil.ErrBoardNotFound, a body over limit
// srcutil.ErrBodyTooLarge, as for pcsx.
func (s *source) getV2(ctx context.Context, pace *srcutil.Pacer, u string, limit int64, v any) error {
	if err := pace.Wait(ctx); err != nil {
		return err
	}
	var raw json.RawMessage
	if err := srcutil.GetJSONLimit(ctx, s.d, u, limit, &raw); err != nil {
		var syn *json.SyntaxError
		if errors.As(err, &syn) {
			return fmt.Errorf("%w: %v", ErrShape, err)
		}
		return err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return fmt.Errorf("%w: no data", ErrShape)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%w: data: %v", ErrShape, err)
	}
	return nil
}

func searchURL(a api, domain, term string, filters map[string][]string, start int) string {
	q := url.Values{}
	q.Set("domain", domain)
	q.Set("query", term)
	q.Set("start", strconv.Itoa(start))
	q.Set("num", strconv.Itoa(pageSize))
	for k, vs := range filters {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	if a.v2 {
		return a.base + "jobs?" + q.Encode()
	}
	return a.base + "search?" + q.Encode()
}

// searchPage fetches one page of a search through a.
func (s *source) searchPage(ctx context.Context, a api, u string, sd *searchData) error {
	if a.v2 {
		return s.getV2(ctx, a.pace, u, v2SearchMaxBody, sd)
	}
	return s.get(ctx, a.pace, u, sd)
}

// access is what a host's robots.txt permits Vani to request: every path of
// each API a board needs.
type access struct{ pcsx, v2 bool }

// robots reads host's robots.txt once, paced as a request of the board, and
// answers for both APIs from that one read. An unreadable robots.txt is an
// error (page.RobotsAllowsPaths), not permission.
func (s *source) robots(ctx context.Context, pace *srcutil.Pacer, host string) (access, error) {
	if err := pace.Wait(ctx); err != nil {
		return access{}, err
	}
	paths := []string{pcsxRoot + "search", pcsxRoot + "position_details", v2Path, v2Path + "/"}
	ok, err := page.RobotsAllowsPaths(ctx, s.d, host, paths...)
	if err != nil {
		return access{}, err
	}
	return access{pcsx: ok[0] && ok[1], v2: ok[2] && ok[3]}, nil
}

// searchV2 is the search of a board whose pcsx search answered 403 (denied):
// the term read through v2, provided the host's robots.txt permitted it
// (v2Allowed, from the board's one read). Every way it can fail reports denied
// as well, and ErrBoardNotFound, ErrTooManyPostings and the like stay reachable
// through errors.Is.
func (s *source) searchV2(ctx context.Context, a api, host, domain, term string, filters map[string][]string, denied *pcsxForbidden, v2Allowed bool) ([]listing, error) {
	both := func(err error) ([]listing, error) {
		return nil, fmt.Errorf("pcsx search: %w; v2 fallback: %w", denied.err, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !v2Allowed {
		return both(fmt.Errorf("not tried, robots.txt of %s disallows %s", host, v2Root))
	}
	got, err := s.search(ctx, a, domain, term, filters, false)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return both(err)
	}
	return got, nil
}

// search pages one term to its count; a count over the cap fails first. probe
// marks the first search of a board over pcsx: its first page answering 403
// is a *pcsxForbidden rather than a plain error.
func (s *source) search(ctx context.Context, a api, domain, term string, filters map[string][]string, probe bool) ([]listing, error) {
	var ids []listing
	have := map[string]bool{}
	count := -1
	for pg := 0; pg < maxPages; pg++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var sd searchData
		if err := s.searchPage(ctx, a, searchURL(a, domain, term, filters, len(ids)), &sd); err != nil {
			var se *egress.StatusError
			if probe && pg == 0 && errors.As(err, &se) && se.Code == http.StatusForbidden {
				return nil, &pcsxForbidden{err: err}
			}
			return nil, err
		}
		if sd.Count == nil || *sd.Count < 0 {
			return nil, fmt.Errorf("%w: no result count", ErrShape)
		}
		if count < 0 {
			count = *sd.Count
			if count > MaxPostings {
				return nil, fmt.Errorf("%w: search matches %d postings, over the %d limit; use a narrower term or the location filter",
					srcutil.ErrTooManyPostings, count, MaxPostings)
			}
		}
		added := 0
		for _, p := range sd.Positions {
			id := p.ID.String()
			if !idRE.MatchString(id) {
				return nil, fmt.Errorf("%w: position id %q", ErrShape, id)
			}
			if !have[id] {
				have[id] = true
				ids = append(ids, listing{id: id, work: strings.TrimSpace(p.WorkLocationOption)})
				added++
			}
		}
		if len(ids) >= count {
			return ids, nil
		}
		if added == 0 {
			return nil, fmt.Errorf("%w: page at %d added nothing with %d of %d read", ErrShape, len(ids), len(ids), count)
		}
	}
	return nil, fmt.Errorf("%w: listing did not end within %d pages", ErrShape, maxPages)
}

// fetchDetail reads one position's detail through a. A 404 is errDetailGone.
func (s *source) fetchDetail(ctx context.Context, a api, domain, id string) (details, error) {
	var d details
	var err error
	if a.v2 {
		var v v2Details
		err = s.getV2(ctx, a.pace, a.base+"jobs/"+id+"?"+url.Values{"domain": {domain}}.Encode(), v2DetailMaxBody, &v)
		d = v.details()
	} else {
		q := url.Values{}
		q.Set("position_id", id)
		q.Set("domain", domain)
		q.Set("hl", "en")
		err = s.get(ctx, a.pace, a.base+"position_details?"+q.Encode(), &d)
	}
	if errors.Is(err, srcutil.ErrBoardNotFound) {
		return details{}, errDetailGone
	}
	return d, err
}

func (s *source) detail(ctx context.Context, a api, host, domain string, l listing, b sources.Board, seen time.Time) (posting.Posting, error) {
	id := l.id
	d, err := s.fetchDetail(ctx, a, domain, id)
	if err != nil {
		return posting.Posting{}, err
	}
	if strings.TrimSpace(d.WorkLocationOption) == "" {
		d.WorkLocationOption = l.work // v2 answers it on the search row, null on the detail
	}
	if d.ID.String() != id {
		return posting.Posting{}, fmt.Errorf("%w: details are for position %q", ErrShape, d.ID.String())
	}
	key, err := posting.EightfoldKey(domain, id)
	if err != nil {
		return posting.Posting{}, err
	}
	title := srcutil.Title(d.Name)
	if title == "" {
		return posting.Posting{}, fmt.Errorf("%w: empty name", ErrShape)
	}
	// The posting URL must be the careers site's own page for this position.
	u := "https://" + host + "/careers/job/" + id
	if d.PublicURL != "" && d.PublicURL != u {
		return posting.Posting{}, fmt.Errorf("%w: publicUrl %q is not %s", ErrShape, d.PublicURL, u)
	}
	company := b.Company
	if company == "" {
		company = domain
	}
	p := posting.Posting{
		Key:         key,
		ATS:         posting.Eightfold,
		Company:     company,
		Title:       title,
		Location:    joinUnique(d.Locations),
		Department:  strings.TrimSpace(d.Department),
		Remote:      remote(d),
		URL:         u,
		Description: description(d),
		SeenAt:      seen,
	}
	if d.PostedTs != nil && *d.PostedTs > 0 {
		p.PostedAt = time.Unix(*d.PostedTs, 0).UTC()
	}
	return p, nil
}

// remote reports whether the position is remote: its work location option says
// so (isRemote), or a work-site value ends in a "remote" part. Microsoft marks
// remote roles only in the latter: live on 2026-10-20 position 1970393557019355
// had workLocationOption "onsite" and work site "0 days / week in-office –
// remote", one of the six work-site values its search offers.
func remote(d details) bool {
	if isRemote(d.WorkLocationOption) {
		return true
	}
	for _, v := range d.WorkSite {
		parts := strings.FieldsFunc(v, func(r rune) bool { return r == '–' || r == '—' || r == '-' })
		if len(parts) > 0 && strings.EqualFold(strings.TrimSpace(parts[len(parts)-1]), "remote") {
			return true
		}
	}
	return false
}

// isRemote reads a work location option: pcsx says "remote", v2 also
// "remote_local" (Netflix's remote-within-a-location roles, seen 2026-10-22).
// Other values ("onsite", "hybrid") are not remote.
func isRemote(opt string) bool {
	opt = strings.ToLower(strings.TrimSpace(opt))
	return opt == "remote" || strings.HasPrefix(opt, "remote_")
}

func joinUnique(ss []string) string {
	var out []string
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return strings.Join(out, "; ")
}

// description is the job description as text, led by a srcutil.RoleLine of
// the tenant's role fields where it has them (Microsoft: role type,
// employment type, work site).
func description(d details) string {
	var flags []string
	flags = append(flags, d.RoleType...)
	for _, v := range d.EmploymentType {
		flags = append(flags, srcutil.EmploymentType(v))
	}
	flags = append(flags, d.WorkSite...)
	lead, room := srcutil.Lead(srcutil.RoleLine(flags...))
	return lead + htmltext.Text(d.JobDescription, room)
}
