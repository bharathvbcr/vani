// Package oraclehcm lists postings from an Oracle Recruiting Cloud candidate-
// experience site (JPMorgan Chase at jpmc.fa.oraclecloud.com, Oracle itself at
// eeho.fa.us2.oraclecloud.com) through the public JSON endpoints behind its
// careers page:
//
//	GET https://{host}/hcmRestApi/resources/latest/recruitingCEJobRequisitions
//	    ?onlyData=true&expand=requisitionList.secondaryLocations
//	    &finder=findReqs;siteNumber={site},keyword="{term}",limit=200,offset=N,
//	            sortBy=POSTING_DATES_DESC,facetsList=""
//	GET https://{host}/hcmRestApi/resources/latest/recruitingCEJobRequisitionDetails
//	    ?onlyData=true&finder=ById;Id="{id}",siteNumber={site}&fields=...
//
// The board token is "tenant/site" ("jpmc/CX_1001") and Host is
// "{tenant}.fa.oraclecloud.com" or "{tenant}.fa.{region}.oraclecloud.com"
// ("eeho.fa.us2.oraclecloud.com"). What the live board does, and the rules the
// source takes from it (all checked 2026-10-27 on jpmc/CX_1001 unless noted):
//
//   - The answer is one search item: its TotalJobsCount and requisitionList
//     are the board's; the outer count, hasMore and limit describe that one
//     item and say nothing about the jobs. The unfiltered board counts 7,317.
//   - A page holds at most 200 rows (limit=201 and 500 answered 200 rows
//     while echoing the limit asked for). An offset at or past the total
//     answers no rows; it never wraps.
//   - A page can come back one row short with the listing far from its end
//     (a counted requisition the page does not return), so a short page is not
//     the end: paging runs to the first page's total or an empty page. The
//     total drifts by one while a board changes (1601 then 1600), so it is read
//     from the first page only.
//   - The oldest end of a listing can hold deleted requisitions as rows whose
//     Id is "{number}_{number}_ORA_DELETED" (see tombstoneRE): counted in the
//     total, not postings. They are left out and counted as listed.
//   - The default order (RELEVANCY) is not stable across pages: a walk of one
//     1,600-job search listed 1,590 and 1,596 distinct requisitions in two
//     runs. POSTING_DATES_DESC listed 1,599 of 1,600 each time, and a posting
//     added mid-walk shifts later rows down (a repeat, which is deduplicated)
//     rather than skipping one. One search was sampled, so a listing that falls
//     short of its total by more than a hundredth fails the board (shortfall).
//   - The site number is not checked by Oracle: an unknown one (CX_9999)
//     answers the whole tenant's board, and a detail ignores it too. A mistyped
//     site therefore lists the tenant under a URL that does not open; only an
//     unresolvable host fails.
//   - A detail of a requisition that is no longer posted answers HTTP 200 with
//     no items, not a 404. Both are "gone".
//   - robots.txt on the host answers 403 from the WAF, so there is no policy
//     to obey.
//
// The listing carries one sentence of each job (ShortDescriptionStr), so a
// posting costs one detail request. A source from NewWith reuses the postings
// an earlier run stored and spends at most MaxDetails detail requests per board
// per run; a source from New fails a board that needs more.
package oraclehcm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// ErrBoardNotFound is returned when the host answers 404 for the board.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

// errIncomplete is a listing that ended before it could account for every job
// the board counts. It fails the board: a partial listing would make the
// reconciler age healthy postings as absent.
var errIncomplete = errors.New("listing is incomplete")

// errShape is an answer whose layout is not the one this source knows.
var errShape = errors.New("oraclehcm: unexpected response")

// errDetailGone is a requisition detail that no longer answers a posting
// (HTTP 200 with no items, or 404) under a listing that answered: the posting
// closed after it was listed.
var errDetailGone = errors.New("detail is gone (no requisition, or HTTP 404)")

const (
	// MaxPostings is the most postings one board may list (the union of its
	// search terms) unless the board sets max_postings, which may go up to
	// sources.MaxPostingsCeiling. A listing costs a request per 200 rows;
	// detail requests are bounded separately by MaxDetails.
	MaxPostings = 5000
	// MaxDetails is the most detail requests one board spends in one run. A
	// source from New fails a board that would need more; one from NewWith
	// reuses stored postings and defers new ones past it to the next run.
	MaxDetails = 300

	// pageSize is Oracle's largest page.
	pageSize = 200
	// maxListBody bounds one listing answer (200 rows are about 350 KB with
	// facets off); maxDetailBody one detail (about 7 KB).
	maxListBody   = 8 << 20
	maxDetailBody = 2 << 20
	// sortBy orders every listing: see the package comment.
	sortBy = "POSTING_DATES_DESC"
	// maxLocations bounds the places a posting's location joins.
	maxLocations = 20
	listPath     = "/hcmRestApi/resources/latest/recruitingCEJobRequisitions"
	detailPath   = "/hcmRestApi/resources/latest/recruitingCEJobRequisitionDetails"
	// detailFields names the detail attributes read, so the answer carries
	// nothing else (it also holds the hiring contact's name and email).
	detailFields = "Id,Title,ExternalDescriptionStr,ExternalResponsibilitiesStr,ExternalQualificationsStr," +
		"ExternalPostedStartDate,PrimaryLocation,JobSchedule,WorkplaceType,WorkplaceTypeCode"
)

var (
	idRE = regexp.MustCompile(`^[0-9]{1,20}$`)
	// tombstoneRE is the id of a deleted requisition the index still lists:
	// "210260943_300015306973757_ORA_DELETED" (two at the oldest end of
	// jpmc/CX_1001's "software engineer" search, 2026-10-27). The requisition
	// number alone answers no detail; the tombstone answers one, so it cannot be
	// told from a posting except by its id. It is counted in the search's total
	// but is not a posting.
	tombstoneRE = regexp.MustCompile(`^[0-9]{1,20}_[0-9]{1,20}_ORA_DELETED$`)
)

// reqID is a requisition's id as the host writes it: a JSON string ("210791916")
// on every board seen, a number accepted too. Anything else decodes as "" and
// fails the id check.
type reqID string

func (i reqID) String() string { return string(i) }

func (i *reqID) UnmarshalJSON(b []byte) error {
	switch {
	case len(b) > 0 && b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*i = reqID(s)
	case len(b) > 0 && (b[0] == '-' || b[0] >= '0' && b[0] <= '9'):
		*i = reqID(b)
	default: // null, bool, object, array
		*i = ""
	}
	return nil
}

// maxGone is how many of n listed postings may come back gone before the board
// fails: a tenth, at least one (workday's bound).
func maxGone(n int) int { return max(1, n/10) }

// shortfall is how many rows of a counted query may be missing before the
// listing is refused: a hundredth, at least one (workday's bound).
func shortfall(count int) int { return max(1, count/100) }

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

// New returns the Oracle HCM source, sending requests through d. Every listed
// posting costs a detail request, and a board needing more than MaxDetails
// fails.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

// Options turn on the bounded detail step (NewWith).
type Options struct {
	// Stored returns the postings an earlier run stored for b: every record
	// under b.KeyPrefix(), whatever its status. A listed row whose key has a
	// stored posting is returned as stored, with no detail request. Required.
	Stored func(b sources.Board) []posting.Posting
	// Report, when set, receives each board's counts, a failed board's too.
	Report func(sources.Board, Stats)
}

// Stats counts what one List did. Every listed row is counted once, in
// Reused, Fetched, Deferred or Gone.
type Stats struct {
	Listed   int `json:"listed"`
	Requests int `json:"requests"` // listing requests, every term's
	// Deleted counts the deleted-requisition markers the listing held (not
	// postings, and not in Listed or any count below).
	Deleted int `json:"deleted,omitempty"`
	// Reused rows were returned as stored, with no request.
	Reused int `json:"reused"`
	// Fetched rows were new and got a detail request.
	Fetched int `json:"fetched"`
	// Deferred rows were new but past MaxDetails: they are fetched on a later
	// run. Not being stored, their absence ages nothing.
	Deferred int `json:"deferred,omitempty"`
	// Gone rows answered their detail with no requisition: closed since listing.
	Gone int `json:"gone,omitempty"`
}

// NewWith returns the Oracle HCM source with the bounded detail step: stored
// postings are reused and at most MaxDetails detail requests go out per board
// per run. Unlike workday's, a stored posting is never re-read: its text is
// as it was when first fetched (see docs/FOLLOWUPS.md).
func NewWith(d egress.Doer, o Options) (sources.Source, error) {
	if o.Stored == nil {
		return nil, errors.New("oraclehcm: the bounded detail step needs the stored postings (Options.Stored)")
	}
	return &source{d: d, now: time.Now, opt: &o}, nil
}

func (s *source) ATS() posting.ATS { return posting.OracleHCM }

// row is one requisition of a listing page.
type row struct {
	ID                 reqID  `json:"Id"`
	Title              string `json:"Title"`
	PostedDate         string `json:"PostedDate"`
	PrimaryLocation    string `json:"PrimaryLocation"`
	WorkplaceTypeCode  string `json:"WorkplaceTypeCode"`
	SecondaryLocations []struct {
		Name string `json:"Name"`
	} `json:"secondaryLocations"`
}

type listResponse struct {
	Items []struct {
		TotalJobsCount  *int  `json:"TotalJobsCount"`
		RequisitionList []row `json:"requisitionList"`
	} `json:"items"`
}

type detailResponse struct {
	Items []detailItem `json:"items"`
}

type detailItem struct {
	ID                          reqID  `json:"Id"`
	Title                       string `json:"Title"`
	ExternalDescriptionStr      string `json:"ExternalDescriptionStr"`
	ExternalResponsibilitiesStr string `json:"ExternalResponsibilitiesStr"`
	ExternalQualificationsStr   string `json:"ExternalQualificationsStr"`
	ExternalPostedStartDate     string `json:"ExternalPostedStartDate"`
	PrimaryLocation             string `json:"PrimaryLocation"`
	JobSchedule                 string `json:"JobSchedule"`
	WorkplaceType               string `json:"WorkplaceType"`
	WorkplaceTypeCode           string `json:"WorkplaceTypeCode"`
}

// target is a board's validated request parameters.
type target struct {
	host, tenant, site string
	terms              []string
	limit              int
}

// resolve checks b and returns where and what to ask.
func resolve(b sources.Board) (target, error) {
	if err := srcutil.CheckBoard(b, posting.OracleHCM); err != nil {
		return target{}, err
	}
	tenant, site, err := parseToken(b.Token)
	if err != nil {
		return target{}, fmt.Errorf("oraclehcm %q: %w", b.Token, err)
	}
	host, err := checkHost(b.Host, tenant)
	if err != nil {
		return target{}, fmt.Errorf("oraclehcm %q: %w", b.Token, err)
	}
	if len(b.Filters) > 0 {
		return target{}, fmt.Errorf("oraclehcm %q: filters are not supported", b.Token)
	}
	limit, err := capFor(b)
	if err != nil {
		return target{}, fmt.Errorf("oraclehcm %q: %w", b.Token, err)
	}
	terms := b.SearchTerms()
	for _, t := range terms {
		if err := checkTerm(t); err != nil {
			return target{}, fmt.Errorf("oraclehcm %q: %w", b.Token, err)
		}
	}
	if len(terms) == 0 {
		terms = []string{""}
	}
	return target{host: host, tenant: tenant, site: site, terms: terms, limit: limit}, nil
}

func (s *source) List(ctx context.Context, b sources.Board) (out []posting.Posting, err error) {
	t, err := resolve(b)
	if err != nil {
		return nil, err
	}
	var st Stats
	if s.opt != nil && s.opt.Report != nil {
		defer func() { s.opt.Report(b, st) }()
	}
	// Each search term is listed in full; the board is their union, each
	// posting once. A listing that cannot account for every job it counts fails
	// the board: a partial one would age healthy postings as absent.
	w := newWalker(s, t)
	for _, term := range t.terms {
		if err := w.walk(ctx, term); err != nil {
			return nil, fmt.Errorf("oraclehcm %q: search %q: %w", b.Token, term, err)
		}
	}
	st.Listed, st.Requests, st.Deleted = len(w.rows), w.requests, w.deleted
	out, err = s.details(ctx, b, t, w.rows, &st)
	if err != nil {
		return nil, fmt.Errorf("oraclehcm %q: %w", b.Token, err)
	}
	return out, nil
}

// listURL is the request for one page of a term's listing; an empty term lists
// the whole board. The finder is one parameter and is encoded as a whole, and
// the term is quoted (checkTerm has kept a quote or backslash out of it):
// unquoted, a comma ends the keyword and the rest is read as finder parameters.
func listURL(t target, term string, offset int) string {
	var f strings.Builder
	f.WriteString("findReqs;siteNumber=" + t.site)
	if term != "" {
		f.WriteString(`,keyword="` + term + `"`)
	}
	fmt.Fprintf(&f, `,limit=%d,offset=%d,sortBy=%s,facetsList=""`, pageSize, offset, sortBy)
	q := url.Values{"onlyData": {"true"}, "expand": {"requisitionList.secondaryLocations"}, "finder": {f.String()}}
	return "https://" + t.host + listPath + "?" + q.Encode()
}

func detailURL(t target, id string) string {
	q := url.Values{
		"onlyData": {"true"},
		"finder":   {`ById;Id="` + id + `",siteNumber=` + t.site},
		"fields":   {detailFields},
	}
	return "https://" + t.host + detailPath + "?" + q.Encode()
}

// publicURL is the requisition's public page, built from the host, site and id
// the source validated, never taken from an answer.
func publicURL(t target, id string) string {
	return "https://" + t.host + "/hcmUI/CandidateExperience/en/sites/" + url.PathEscape(t.site) + "/job/" + id
}

// boardCompany is the company a board's postings carry: the board's, else the
// tenant.
func boardCompany(b sources.Board, tenant string) string {
	if c := strings.TrimSpace(b.Company); c != "" {
		return c
	}
	return tenant
}

// fetch is one row that gets a detail request.
type fetch struct {
	index int
	row   row
}

// details turns listed rows into postings. Without Options every row is
// fetched, and a board needing more than MaxDetails fails. With Options, a row
// whose posting is stored is reused, new rows are fetched up to MaxDetails and
// the rest are deferred. Results keep listing order.
func (s *source) details(ctx context.Context, b sources.Board, t target, rows []row, st *Stats) ([]posting.Posting, error) {
	seen := s.now()
	stored := map[posting.Key]posting.Posting{}
	if s.opt != nil {
		for _, p := range s.opt.Stored(b) {
			stored[p.Key] = p
		}
	}
	slot := make([]*posting.Posting, len(rows))
	var fresh []fetch
	for i, r := range rows {
		key, err := posting.OracleHCMKey(t.tenant, t.site, r.ID.String())
		if err != nil {
			return nil, err
		}
		if p, ok := stored[key]; ok {
			p.SeenAt = seen
			slot[i] = &p
			st.Reused++
			continue
		}
		fresh = append(fresh, fetch{i, r})
	}
	if s.opt == nil && len(fresh) > MaxDetails {
		// Without stored postings a deferred row cannot be told from a stored
		// one, and leaving out a stored one would age it as absent.
		return nil, fmt.Errorf("%w: the board lists %d postings and each costs a detail request, over the %d limit; set the board's search to narrow it",
			srcutil.ErrTooManyPostings, len(fresh), MaxDetails)
	}
	if len(fresh) > MaxDetails {
		st.Deferred = len(fresh) - MaxDetails
		fresh = fresh[:MaxDetails]
	}
	allowed := maxGone(len(fresh))
	for _, f := range fresh {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p, err := s.detail(ctx, b, t, f.row, seen)
		if errors.Is(err, errDetailGone) {
			// The posting closed between the listing and its detail; leaving it
			// out is the truth. Many at once is the detail endpoint failing, not
			// postings closing, and would empty a healthy board. With every other
			// row reused, one new posting gone is only that posting.
			if st.Gone++; st.Gone > allowed || (st.Gone == len(fresh) && st.Reused == 0) {
				return nil, fmt.Errorf("%d of %d posting details are gone; the detail endpoint is not answering", st.Gone, len(fresh))
			}
			continue
		}
		if err != nil {
			// A partial listing would make the reconciler age healthy postings
			// as absent, so one bad posting fails the whole board.
			return nil, fmt.Errorf("posting %s: %w", f.row.ID, err)
		}
		st.Fetched++
		slot[f.index] = &p
	}
	out := make([]posting.Posting, 0, len(rows))
	for _, p := range slot {
		if p != nil {
			out = append(out, *p)
		}
	}
	return out, nil
}

// detail reads one requisition's text and builds its posting.
func (s *source) detail(ctx context.Context, b sources.Board, t target, r row, seen time.Time) (posting.Posting, error) {
	id := r.ID.String()
	var resp detailResponse
	if err := srcutil.GetJSONLimit(ctx, s.d, detailURL(t, id), maxDetailBody, &resp); err != nil {
		var se *egress.StatusError
		if errors.Is(err, srcutil.ErrBoardNotFound) || (errors.As(err, &se) && se.Code == http.StatusNotFound) {
			// The board exists (its listing answered); only this posting is gone.
			return posting.Posting{}, errDetailGone
		}
		return posting.Posting{}, err
	}
	switch len(resp.Items) {
	case 0:
		return posting.Posting{}, errDetailGone
	case 1:
	default:
		return posting.Posting{}, fmt.Errorf("%w: %d requisitions answered one id", errShape, len(resp.Items))
	}
	d := resp.Items[0]
	if d.ID.String() != id {
		return posting.Posting{}, fmt.Errorf("%w: asked for requisition %s, got %q", errShape, id, d.ID.String())
	}
	key, err := posting.OracleHCMKey(t.tenant, t.site, id)
	if err != nil {
		return posting.Posting{}, err
	}
	title := srcutil.Title(d.Title)
	if title == "" {
		title = srcutil.Title(r.Title)
	}
	if title == "" {
		return posting.Posting{}, errors.New("empty title")
	}
	pub := publicURL(t, id)
	if err := srcutil.CheckURL("job url", pub); err != nil {
		return posting.Posting{}, err
	}
	var secondary []string
	for _, l := range r.SecondaryLocations {
		secondary = append(secondary, l.Name)
	}
	location := joinLocations(r.PrimaryLocation, secondary)
	if location == "" {
		location = joinLocations(d.PrimaryLocation, nil)
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.OracleHCM,
		Company:     boardCompany(b, t.tenant),
		Title:       title,
		Location:    location,
		Remote:      remote(d.WorkplaceType, d.WorkplaceTypeCode, r.WorkplaceTypeCode),
		URL:         pub,
		PostedAt:    postedAt(d.ExternalPostedStartDate, r.PostedDate),
		Description: description(d),
		SeenAt:      seen,
	}, nil
}

// remote reports a remote workplace. Every requisition seen live (JPMorgan 200
// rows, Oracle 100) had an empty WorkplaceType and a null WorkplaceTypeCode,
// and the one code the board named, in its facet, was "ORA_ON_SITE": the
// "REMOTE" match is inferred from that naming, not seen. The location text
// ("Remote - US") is read by the engine either way.
func remote(text string, codes ...string) bool {
	if strings.EqualFold(strings.TrimSpace(text), "remote") {
		return true
	}
	return slices.ContainsFunc(codes, func(c string) bool { return strings.Contains(strings.ToUpper(c), "REMOTE") })
}

// description is the job description as text, led by a srcutil.RoleLine of the
// schedule. It joins the posting's own description, responsibilities and
// qualifications; the corporate and organization blurbs the detail also holds
// are the same for every posting and are not read.
func description(d detailItem) string {
	lead, room := srcutil.Lead(srcutil.RoleLine(srcutil.EmploymentType(d.JobSchedule)))
	var parts []string
	for _, h := range []string{d.ExternalDescriptionStr, d.ExternalResponsibilitiesStr, d.ExternalQualificationsStr} {
		if strings.TrimSpace(h) != "" {
			parts = append(parts, h)
		}
	}
	return lead + htmltext.Text(strings.Join(parts, "<br><br>"), room)
}

// joinLocations is the primary location then the other ones, trimmed, without
// repeats, joined by "; " (workday's rule), at most maxLocations of them.
func joinLocations(primary string, more []string) string {
	var out []string
	for _, l := range append([]string{primary}, more...) {
		if len(out) == maxLocations {
			break
		}
		l = strings.Join(strings.Fields(l), " ")
		if l != "" && !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, l) }) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "; ")
}

// postedAt is the detail's start of posting (an RFC 3339 instant), else the
// listing row's day. A date that cannot be read is the zero time: a guessed
// one would be worse than none.
func postedAt(start, day string) time.Time {
	if t, err := srcutil.ParseTime("ExternalPostedStartDate", strings.TrimSpace(start)); err == nil && !t.IsZero() {
		return t
	}
	if t, err := time.Parse(time.DateOnly, strings.TrimSpace(day)); err == nil {
		return t
	}
	return time.Time{}
}

// parseToken splits "tenant/site" and refuses anything that could change the
// request path or the finder.
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

// checkHost requires a bare hostname "{tenant}.fa.oraclecloud.com" or
// "{tenant}.fa.{region}.oraclecloud.com" (sources.OracleHCMHostTenant, the one
// definition config and the ingest read too) whose first label is the tenant,
// so a board cannot send its requests to a host its token does not name.
func checkHost(host, tenant string) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "", errors.New("board host is empty")
	}
	label, ok := sources.OracleHCMHostTenant(host)
	if !ok {
		return "", fmt.Errorf("board host %q is not {tenant}.fa[.{region}].oraclecloud.com", host)
	}
	if label != strings.ToLower(tenant) {
		return "", fmt.Errorf("board host %q does not start with tenant %q", host, tenant)
	}
	return host, nil
}

// checkTerm refuses a search term the finder could not carry as written: a
// quote ends the keyword, and a backslash escapes the next character (both
// seen live), so neither is sent; a comma, semicolon or equals sign inside the
// quotes is read as text (also seen).
func checkTerm(term string) error {
	for _, r := range term {
		if r == '"' || r == '\\' || unicode.IsControl(r) {
			return fmt.Errorf("search %q has a quote, backslash or control character, which the finder cannot carry", term)
		}
	}
	return nil
}
