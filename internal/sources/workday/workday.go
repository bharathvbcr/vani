// Package workday lists postings from a Workday candidate-experience site
// through the public JSON endpoints behind it:
//
//	POST https://{host}/wday/cxs/{tenant}/{site}/jobs        (paged listing)
//	GET  https://{host}/wday/cxs/{tenant}/{site}{externalPath} (one posting)
//
// Workday refuses a page larger than 20 rows and serves at most 2000 rows of
// one query (crawl.go), so a larger board is listed by splitting it on its
// own facets. The listing carries no description, so a posting costs one
// detail request; a source built with NewWith reuses the postings an earlier
// run stored and spends at most MaxDetails detail requests per board per run.
package workday

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
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
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// ErrBoardNotFound is returned when Workday has no such tenant/site.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

// errDetailGone is a posting detail answering 404 under a listing that
// answered: the posting closed after it was listed.
var errDetailGone = errors.New("detail is gone (HTTP 404)")

// maxGone is how many of n listed postings may come back gone before the board
// fails: a tenth, at least one. A posting closing mid-run is ordinary; a
// tenth of the board closing in the minutes a listing takes is not.
func maxGone(n int) int { return max(1, n/10) }

const (
	// MaxPostings is the most postings one board may list (the union of its
	// search terms) unless the board sets max_postings, which may go up to
	// sources.MaxPostingsCeiling. It fits the largest board seen (NVIDIA,
	// about 2,750) with room to grow. Listing costs a request per 20 rows;
	// detail requests are bounded separately by MaxDetails.
	MaxPostings = 5000
	// MaxDetails is the most detail requests one board spends in one run.
	// A source from New fails a board that would need more; one from NewWith
	// reuses stored postings and defers new ones past it to the next run.
	MaxDetails = 300
	// refreshDays is how many runs it takes to re-read every stored posting:
	// each run re-reads the stored postings whose path falls in that day's
	// share, so a description edited on the board is seen within this many
	// days (budget permitting).
	refreshDays = 30
	// pageSize is Workday's largest page: a larger limit returns zero rows.
	pageSize = 20
)

// capFor is the board's listing limit: its max_postings, or MaxPostings.
func capFor(b sources.Board) (int, error) {
	switch {
	case b.MaxPostings == 0:
		return MaxPostings, nil
	case b.MaxPostings < 1 || b.MaxPostings > sources.MaxPostingsCeiling:
		return 0, fmt.Errorf("max_postings %d outside 1..%d", b.MaxPostings, sources.MaxPostingsCeiling)
	}
	return b.MaxPostings, nil
}

type source struct {
	d   egress.Doer
	now func() time.Time
	opt *Options
}

// New returns the Workday source, sending requests through d. Every listed
// posting costs a detail request, and a board needing more than MaxDetails
// fails.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

// Options turn on the bounded detail step (NewWith).
type Options struct {
	// Stored returns the postings an earlier run stored for b: every record
	// under b.KeyPrefix(), whatever its status. A listed row whose stored
	// posting has the row's path (read from its URL) is returned as stored,
	// with no detail request. Required.
	Stored func(b sources.Board) []posting.Posting
	// DropListed reports whether the engine drops a posting before triage
	// from what a listed row says: its title, the board's company and the
	// row's location text (no description, no remote flag). A listed row with
	// no stored posting that DropListed drops is neither fetched nor returned
	// (it would be dropped before triage anyway). Nil drops nothing.
	DropListed func(p posting.Posting) bool
	// Report, when set, receives each listed board's counts.
	Report func(sources.Board, Stats)
}

// Stats counts what one List did. Every listed row is counted once, in
// Reused, Fetched, Refreshed, Prefiltered, Deferred or Gone.
type Stats struct {
	Listed   int `json:"listed"`
	Queries  int `json:"queries"`  // listing queries; a split board has one per bucket
	Requests int `json:"requests"` // listing requests
	// Reused rows were returned as stored, with no request.
	Reused int `json:"reused"`
	// Fetched rows were new and got a detail request; Refreshed rows were
	// stored and were re-read in their turn.
	Fetched   int `json:"fetched"`
	Refreshed int `json:"refreshed,omitempty"`
	// Prefiltered rows were new and dropped by DropListed without a request.
	Prefiltered int `json:"prefiltered,omitempty"`
	// Deferred rows were new but past MaxDetails: they are fetched on a later
	// run. Not being stored, their absence ages nothing.
	Deferred int `json:"deferred,omitempty"`
	// Gone rows answered their detail with 404: closed since listing.
	Gone int `json:"gone,omitempty"`
}

// NewWith returns the Workday source with the bounded detail step: stored
// postings are reused (a share of them re-read each run), new ones are
// title-prefiltered, and at most MaxDetails detail requests go out per board
// per run.
func NewWith(d egress.Doer, o Options) (sources.Source, error) {
	if o.Stored == nil {
		return nil, errors.New("workday: the bounded detail step needs the stored postings (Options.Stored)")
	}
	return &source{d: d, now: time.Now, opt: &o}, nil
}

func (s *source) ATS() posting.ATS { return posting.Workday }

type listRequest struct {
	AppliedFacets map[string][]string `json:"appliedFacets"`
	Limit         int                 `json:"limit"`
	Offset        int                 `json:"offset"`
	SearchText    string              `json:"searchText"`
}

type listedJob struct {
	Title         string `json:"title"`
	ExternalPath  string `json:"externalPath"`
	LocationsText string `json:"locationsText"`
	PostedOn      string `json:"postedOn"`
}

type detailResponse struct {
	JobPostingInfo struct {
		JobReqID       string `json:"jobReqId"`
		Title          string `json:"title"`
		Location       string `json:"location"`
		JobDescription string `json:"jobDescription"`
		StartDate      string `json:"startDate"`
		PostedOn       string `json:"postedOn"`
		ExternalURL    string `json:"externalUrl"`
		RemoteType     string `json:"remoteType"`
		// AdditionalLocations are the posting's other places, after
		// Location ("US, CA, Remote"); see detailLocation.
		AdditionalLocations []string `json:"additionalLocations"`
		// TimeType is "Full time" or "Part time" (NVIDIA and Intel,
		// 2026-10-20: 2 of 568 and 12 of 139 part time).
		TimeType string `json:"timeType"`
	} `json:"jobPostingInfo"`
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := srcutil.CheckBoard(b, posting.Workday); err != nil {
		return nil, err
	}
	tenant, site, err := parseToken(b.Token)
	if err != nil {
		return nil, fmt.Errorf("workday %q: %w", b.Token, err)
	}
	host, err := checkHost(b.Host, tenant)
	if err != nil {
		return nil, fmt.Errorf("workday %q: %w", b.Token, err)
	}
	if len(b.Filters) > 0 {
		return nil, fmt.Errorf("workday %q: filters are not supported (Workday facets are tenant-specific ids)", b.Token)
	}
	base := "https://" + host + "/wday/cxs/" + url.PathEscape(tenant) + "/" + url.PathEscape(site)

	// Each search term is listed in full; the board is their union, each
	// posting once. A listing that cannot account for every job it counts
	// fails the board: a partial one would age healthy postings as absent.
	terms := b.SearchTerms()
	if len(terms) == 0 {
		terms = []string{""}
	}
	limit, err := capFor(b)
	if err != nil {
		return nil, fmt.Errorf("workday %q: %w", b.Token, err)
	}
	w := newWalker(s, base+"/jobs", limit)
	for _, term := range terms {
		w.term = term
		if _, err := w.walk(ctx, map[string][]string{}, 0); err != nil {
			return nil, fmt.Errorf("workday %q: search %q: %w", b.Token, term, err)
		}
	}
	st := Stats{Listed: len(w.rows), Queries: w.queries, Requests: w.requests}
	out, err := s.details(ctx, b, base, host, tenant, site, w.rows, &st)
	if err != nil {
		return nil, fmt.Errorf("workday %q: %w", b.Token, err)
	}
	if s.opt != nil && s.opt.Report != nil {
		s.opt.Report(b, st)
	}
	return out, nil
}

// boardCompany is the company a board's postings carry: the board's, else
// the tenant.
func boardCompany(b sources.Board, tenant string) string {
	if c := strings.TrimSpace(b.Company); c != "" {
		return c
	}
	return tenant
}

// locationCount is a row's location text when it only counts the posting's
// places ("3 Locations"): no place to judge until the details are read.
var locationCount = regexp.MustCompile(`(?i)^[0-9]+\s+locations?$`)

// listedPosting is what a listed row says of its posting before its details
// are read: the fields DropListed may judge.
func listedPosting(b sources.Board, tenant string, row listedJob) posting.Posting {
	loc := strings.TrimSpace(row.LocationsText)
	if locationCount.MatchString(loc) {
		loc = ""
	}
	return posting.Posting{ATS: posting.Workday, Company: boardCompany(b, tenant), Title: strings.TrimSpace(row.Title), Location: loc}
}

// fetch is one row that gets a detail request, and whether it was stored.
type fetch struct {
	row    listedJob
	stored bool
}

// details turns listed rows into postings. Without Options every row is
// fetched, and a board needing more than MaxDetails fails. With Options, a
// row whose posting is stored is reused, except the stored rows whose turn it
// is to be re-read; new rows dropped by title are skipped; new rows are
// fetched first, then the re-reads, up to MaxDetails, and the new rows past
// it are deferred. Results keep listing order.
func (s *source) details(ctx context.Context, b sources.Board, base, host, tenant, site string, rows []listedJob, st *Stats) ([]posting.Posting, error) {
	seen := s.now()
	stored := map[string]posting.Posting{}
	storedKeys := map[posting.Key]bool{}
	unmatched := 0 // stored postings whose URL names no path on this site
	if s.opt != nil {
		prefix := b.KeyPrefix()
		for _, p := range s.opt.Stored(b) {
			if !strings.HasPrefix(string(p.Key), prefix) {
				continue // another board's posting is never this board's row
			}
			storedKeys[p.Key] = true
			if path, ok := storedPath(p.URL, host, pagePrefix(host, tenant, site)); ok {
				stored[path] = p
			} else {
				unmatched++
			}
		}
	}
	slot := make([]*posting.Posting, len(rows))
	var fresh, refresh []int
	day := int(seen.UTC().Unix() / 86400)
	for i, row := range rows {
		p, ok := stored[row.ExternalPath]
		switch {
		case ok && refreshTurn(row.ExternalPath, day):
			refresh = append(refresh, i)
		case ok:
			p.SeenAt = seen
			slot[i] = &p
			st.Reused++
		case s.opt != nil && s.opt.DropListed != nil && s.opt.DropListed(listedPosting(b, tenant, row)):
			st.Prefiltered++
		default:
			fresh = append(fresh, i)
		}
	}
	if s.opt == nil && len(fresh) > MaxDetails {
		// Without stored postings a deferred row cannot be told from a stored
		// one, and leaving out a stored one would age it as absent.
		return nil, fmt.Errorf("%w: the board lists %d postings and each costs a detail request, over the %d limit; set the board's search to narrow it",
			srcutil.ErrTooManyPostings, len(fresh), MaxDetails)
	}
	if len(fresh) > MaxDetails {
		// Deferring is safe only for rows the ledger does not hold. A stored
		// posting whose URL could not be matched to a path may be one of the
		// rows about to be deferred, and leaving it out would age it.
		if unmatched > 0 {
			return nil, fmt.Errorf("%d stored postings have a URL that names no path on site %q, so the %d new rows past the %d-request cap cannot be deferred safely",
				unmatched, site, len(fresh)-MaxDetails, MaxDetails)
		}
		st.Deferred = len(fresh) - MaxDetails
		fresh = fresh[:MaxDetails]
	}
	if room := MaxDetails - len(fresh); len(refresh) > room {
		// A stored row whose re-read does not fit is reused as stored.
		for _, i := range refresh[room:] {
			p := stored[rows[i].ExternalPath]
			p.SeenAt = seen
			slot[i] = &p
			st.Reused++
		}
		refresh = refresh[:room]
	}
	jobs := make([]fetch, 0, len(fresh)+len(refresh))
	for _, i := range fresh {
		jobs = append(jobs, fetch{row: rows[i]})
	}
	for _, i := range refresh {
		jobs = append(jobs, fetch{row: rows[i], stored: true})
	}
	order := append(append([]int{}, fresh...), refresh...)
	allowed := maxGone(len(jobs))
	for n, j := range jobs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p, err := s.detail(ctx, base, host, tenant, site, j.row, b, seen)
		if errors.Is(err, errDetailGone) {
			// The posting closed between the listing and its detail; leaving it
			// out is the truth. Many at once is the detail endpoint failing, not
			// postings closing, and would empty a healthy board. With every
			// other row reused, one new posting gone is only that posting.
			if st.Gone++; st.Gone > allowed || (st.Gone == len(jobs) && st.Reused == 0) {
				return nil, fmt.Errorf("%d of %d posting details are gone (HTTP 404); the detail endpoint is not answering",
					st.Gone, len(jobs))
			}
			continue
		}
		if err != nil {
			// A partial listing would make the reconciler age healthy postings
			// as absent, so one bad posting fails the whole board.
			return nil, fmt.Errorf("posting %q: %w", j.row.ExternalPath, err)
		}
		if j.stored {
			st.Refreshed++
		} else {
			st.Fetched++
			// A "new" row that turns out to be stored means its URL and the
			// listing's path disagree: other stored rows may be among the
			// deferred, so a deferring listing cannot be trusted.
			if storedKeys[p.Key] && st.Deferred > 0 {
				return nil, fmt.Errorf("posting %s is stored under a URL that does not match its listed path %q, so the %d deferred rows may hold stored postings",
					p.Key, j.row.ExternalPath, st.Deferred)
			}
		}
		slot[order[n]] = &p
	}
	out := make([]posting.Posting, 0, len(rows))
	keys := make(map[posting.Key]bool, len(rows))
	for _, p := range slot {
		if p == nil || keys[p.Key] {
			continue
		}
		keys[p.Key] = true
		out = append(out, *p)
	}
	return out, nil
}

// refreshTurn reports whether the stored posting at path is re-read on day:
// each path's turn comes once every refreshDays days.
func refreshTurn(path string, day int) bool {
	h := fnv.New32a()
	h.Write([]byte(path))
	return int(h.Sum32()%refreshDays) == day%refreshDays
}

// pagePrefix is the leading path of a posting's public page on host: "{site}"
// on a tenant host "{tenant}.wd{N}.myworkdayjobs.com", and
// "recruiting/{tenant}/{site}" on the shared host "wd{N}.myworkdaysite.com",
// which has no tenant label to say whose site it is. Its segments are
// unescaped; parseToken has limited them to letters, digits, '-' and '_'.
func pagePrefix(host, tenant, site string) string {
	if sources.WorkdaySiteHost(host) {
		return "recruiting/" + tenant + "/" + site
	}
	return site
}

// storedPath is the listing externalPath a stored posting's URL carries: the
// path after the page prefix (pagePrefix; a bare site name works too) and an
// optional locale before it, on host. ok is false for a URL of another host or
// site, which is then never reused.
func storedPath(raw, host, prefix string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Hostname(), host) {
		return "", false
	}
	want := strings.Split(prefix, "/")
	segs := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(segs) > 1 && !strings.EqualFold(segs[0], want[0]) && locale.MatchString(segs[0]) {
		segs = segs[1:]
	}
	if len(segs) < len(want)+1 {
		return "", false
	}
	for i, w := range want {
		if !strings.EqualFold(segs[i], w) {
			return "", false
		}
	}
	return "/" + strings.Join(segs[len(want):], "/"), true
}

var locale = regexp.MustCompile(`^[a-z]{2}(-[A-Za-z]{2,4})?$`)

func (s *source) detail(ctx context.Context, base, host, tenant, site string, row listedJob, b sources.Board, seen time.Time) (posting.Posting, error) {
	path, err := escapePath(row.ExternalPath)
	if err != nil {
		return posting.Posting{}, err
	}
	var d detailResponse
	if err := srcutil.GetJSON(ctx, s.d, base+path, &d); err != nil {
		if errors.Is(err, srcutil.ErrBoardNotFound) {
			// The board exists (its listing answered); only this posting is gone.
			return posting.Posting{}, errDetailGone
		}
		return posting.Posting{}, err
	}
	info := d.JobPostingInfo
	key, err := posting.WorkdayKey(tenant, site, info.JobReqID)
	if err != nil {
		return posting.Posting{}, err
	}
	title := srcutil.Title(info.Title)
	if title == "" {
		title = srcutil.Title(row.Title)
	}
	if title == "" {
		return posting.Posting{}, errors.New("empty title")
	}
	pubURL := info.ExternalURL
	if pubURL == "" {
		pubURL = "https://" + host + "/" + pagePrefix(host, url.PathEscape(tenant), url.PathEscape(site)) + path
	}
	if err := srcutil.CheckURL("externalUrl", pubURL); err != nil {
		return posting.Posting{}, err
	}
	location := detailLocation(info.Location, info.AdditionalLocations)
	if location == "" {
		location = strings.TrimSpace(row.LocationsText)
	}
	company := boardCompany(b, tenant)
	phrase := row.PostedOn
	if phrase == "" {
		phrase = info.PostedOn
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.Workday,
		Company:     company,
		Title:       title,
		Location:    location,
		Remote:      strings.EqualFold(strings.TrimSpace(info.RemoteType), "remote"),
		URL:         pubURL,
		PostedAt:    postedAt(info.StartDate, phrase, seen),
		Description: description(info.JobDescription, info.TimeType),
		SeenAt:      seen,
	}, nil
}

// description is the job description as text, led by a srcutil.RoleLine of
// the detail's time type.
func description(jobDescription, timeType string) string {
	lead, room := srcutil.Lead(srcutil.RoleLine(srcutil.EmploymentType(timeType)))
	return lead + htmltext.Text(jobDescription, room)
}

// maxLocations bounds the places detailLocation joins.
const maxLocations = 20

// detailLocation is the primary location then the additional ones, trimmed,
// without repeats, joined by "; ". A multi-location posting's places are only
// in the detail: live on 2026-10-20, 9 of 20 NVIDIA and 7 of 20 Intel rows
// listed "N Locations", and NVIDIA JR2021784's only remote place was an
// additional one ("US, CA, Remote"), which location.Parse reads as remote.
// Neither tenant sent remoteType.
func detailLocation(primary string, more []string) string {
	var out []string
	for _, l := range append([]string{primary}, more...) {
		if len(out) == maxLocations {
			break
		}
		if l = strings.TrimSpace(l); l != "" && !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, l) }) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "; ")
}

// postedAt prefers the detail's startDate (an exact day). Otherwise it reads
// Workday's relative phrase against now. A phrase that is not an exact day
// count ("Posted 30+ Days Ago" is only a lower bound) yields the zero time:
// a guessed date would be worse than none.
func postedAt(startDate, phrase string, now time.Time) time.Time {
	if t, err := time.Parse(time.DateOnly, strings.TrimSpace(startDate)); err == nil {
		return t
	}
	day := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	f := strings.Fields(strings.ToLower(phrase))
	switch {
	case len(f) == 2 && f[0] == "posted" && f[1] == "today":
		return day
	case len(f) == 2 && f[0] == "posted" && f[1] == "yesterday":
		return day.AddDate(0, 0, -1)
	case len(f) == 4 && f[0] == "posted" && (f[2] == "days" || f[2] == "day") && f[3] == "ago":
		n, err := strconv.Atoi(f[1])
		if err != nil || n < 1 || n > 365 {
			return time.Time{}
		}
		return day.AddDate(0, 0, -n)
	}
	return time.Time{}
}

// postJSON POSTs the listing search. Workday wants Accept-Language; status,
// body-cap and retry rules are srcutil's, shared with every adapter's GET.
func (s *source) postJSON(ctx context.Context, endpoint string, body, v any) error {
	return srcutil.PostJSON(ctx, s.d, endpoint, body, http.Header{"Accept-Language": {"en-US"}}, v)
}

// parseToken splits "tenant/site" and refuses anything that could change the
// request path.
func parseToken(token string) (tenant, site string, err error) {
	tenant, site, ok := strings.Cut(strings.TrimSpace(token), "/")
	if !ok || !validName(tenant) || !validName(site) {
		return "", "", errors.New(`token must be "tenant/site"`)
	}
	return tenant, site, nil
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// checkHost requires a bare hostname that is either Workday's shared host
// "wd{N}.myworkdaysite.com" (no tenant label; the token's tenant, already
// checked by parseToken, picks the site) or one whose first label is the
// tenant, so a board cannot send its requests to a host its token does not
// name.
func checkHost(host, tenant string) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "", errors.New("board host is empty")
	}
	for _, r := range host {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return "", fmt.Errorf("board host %q is not a bare hostname", host)
		}
	}
	if sources.WorkdaySiteHost(host) {
		return host, nil
	}
	first, _, _ := strings.Cut(host, ".")
	if first != strings.ToLower(tenant) {
		return "", fmt.Errorf("board host %q does not start with tenant %q", host, tenant)
	}
	return host, nil
}

// escapePath validates an externalPath from the (untrusted) listing and
// escapes each segment, so it can only extend the site path: under /job/,
// with no dot segment, empty segment, backslash or encoded separator or dot
// (a server that decodes "%2e%2e" or "%2f" before routing would climb out).
func escapePath(p string) (string, error) {
	if !strings.HasPrefix(p, "/job/") || len(p) > maxPathLen {
		return "", fmt.Errorf("externalPath %q is not a job path", p)
	}
	if strings.ContainsRune(p, '\\') || encodedDotOrSlash.MatchString(p) {
		return "", fmt.Errorf("externalPath %q has an encoded separator", p)
	}
	segs := strings.Split(p[1:], "/")
	for i, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("externalPath %q has an invalid segment", p)
		}
		segs[i] = url.PathEscape(seg)
	}
	return "/" + strings.Join(segs, "/"), nil
}

// maxPathLen bounds one externalPath.
const maxPathLen = 1000

var encodedDotOrSlash = regexp.MustCompile(`(?i)%(2e|2f|5c)`)
