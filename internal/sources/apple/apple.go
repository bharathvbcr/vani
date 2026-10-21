// Package apple lists postings from jobs.apple.com.
//
// The search page (en-us) is server-rendered: its result count and each
// row's details link are in the raw HTML, and every posting page embeds the
// full posting as JSON in window.__staticRouterHydrationData. So the source
// is plain GETs through egress: the search pages for each term, then one
// details page per posting. jobs.apple.com has no robots.txt (2026-10-19).
//
// A term whose result count exceeds the board cap fails the board rather
// than being truncated; quoting a phrase ("\"LLM inference\"") and the
// location filter narrow Apple's otherwise loose search.
package apple

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
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

const (
	site      = "https://jobs.apple.com"
	searchURL = site + "/en-us/search"
	// MaxPostings bounds one board: each posting costs a details request.
	MaxPostings = 200
	// pageSize is the rows per search page (observed 2026-10-19). It only
	// bounds the page loop; the count decides when a listing is complete.
	pageSize = 20
)

var (
	// ErrShape is a page whose layout is not the one this source knows.
	ErrShape = errors.New("apple: unexpected page layout")
)

var (
	countRE = regexp.MustCompile(`id="search-result-count"[^>]*>\s*([0-9][0-9,]*)(\+?)\s*Result`)
	// A row's link: /en-us/details/{positionId}-{suffix}/{slug}. The job
	// number is positionId-suffix; the position id is the role.
	rowRE        = regexp.MustCompile(`href="/en-us/details/([0-9]{1,15})-([0-9]{1,8})/([a-z0-9-]{1,300})`)
	hydrationTag = []byte("window.__staticRouterHydrationData = JSON.parse(")
)

// errDetailGone is a posting page answering 404 under a search that
// answered: the posting closed after it was listed.
var errDetailGone = errors.New("detail is gone (HTTP 404)")

// maxGone is how many of n listed postings may come back gone before the
// board fails: a tenth, at least one (workday's bound).
func maxGone(n int) int { return max(1, n/10) }

// Stats counts what one List did: the postings listed and the ones whose
// details page answered 404 (closed since listing).
type Stats struct {
	Listed int `json:"listed"`
	Gone   int `json:"gone,omitempty"`
}

// Options configures NewWith.
type Options struct {
	// Report, when set, receives each listed board's counts.
	Report func(sources.Board, Stats)
}

type source struct {
	d   egress.Doer
	now func() time.Time
	opt *Options
}

// New returns the Apple jobs source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

// NewWith is New with the board report of opt.
func NewWith(d egress.Doer, opt Options) sources.Source {
	return &source{d: d, now: time.Now, opt: &opt}
}

func (*source) ATS() posting.ATS { return posting.Apple }

// row is one search result.
type row struct {
	position, jobNumber, slug string
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := srcutil.CheckBoard(b, posting.Apple); err != nil {
		return nil, err
	}
	terms := b.SearchTerms()
	if len(terms) == 0 {
		return nil, fmt.Errorf("apple %q: the board needs at least one search", b.Token)
	}
	if err := sources.CheckFilters(posting.Apple, b.Filters); err != nil {
		return nil, fmt.Errorf("apple %q: %w", b.Token, err)
	}
	var rows []row
	positions := map[string]bool{}
	for _, term := range terms {
		got, err := s.search(ctx, term, b.Filters)
		if err != nil {
			return nil, fmt.Errorf("apple %q: search %q: %w", b.Token, term, err)
		}
		for _, r := range got {
			if !positions[r.position] {
				positions[r.position] = true
				rows = append(rows, r)
			}
		}
		if len(rows) > MaxPostings {
			return nil, fmt.Errorf("apple %q: %w: the board's searches together list more than %d postings; narrow them",
				b.Token, srcutil.ErrTooManyPostings, MaxPostings)
		}
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(rows))
	st := Stats{Listed: len(rows)}
	if s.opt != nil && s.opt.Report != nil {
		defer func() { s.opt.Report(b, st) }()
	}
	allowed := maxGone(len(rows))
	for _, r := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p, err := s.detail(ctx, b, r, seen)
		if errors.Is(err, errDetailGone) {
			// The posting closed between the search and its page; leaving it out
			// is the truth. Many at once is the details endpoint failing. The
			// error is deliberately not ErrBoardNotFound: the search answered.
			if st.Gone++; st.Gone > allowed || st.Gone == len(rows) {
				return nil, fmt.Errorf("apple %q: %d of %d posting details are gone (HTTP 404); the details endpoint is not answering",
					b.Token, st.Gone, len(rows))
			}
			continue
		}
		if err != nil {
			// A partial listing would age healthy postings as absent.
			return nil, fmt.Errorf("apple %q: posting %s: %w", b.Token, r.jobNumber, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func pageURL(term string, filters map[string][]string, page int) string {
	q := url.Values{}
	q.Set("search", term)
	q.Set("sort", "relevance")
	for k, vs := range filters {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	return searchURL + "?" + q.Encode()
}

// search reads every page of one term. The first page's count must be exact
// (not "600+") and within the cap; the listing is complete when the distinct
// job numbers read reach it.
func (s *source) search(ctx context.Context, term string, filters map[string][]string) ([]row, error) {
	var rows []row
	jobNumbers := map[string]bool{}
	count := -1
	for page := 1; ; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		body, err := srcutil.GetBody(ctx, s.d, pageURL(term, filters, page))
		if err != nil {
			return nil, err
		}
		if count < 0 {
			if count, err = resultCount(body); err != nil {
				return nil, err
			}
		}
		added := 0
		for _, r := range parseRows(body) {
			if jobNumbers[r.jobNumber] {
				continue
			}
			jobNumbers[r.jobNumber] = true
			rows = append(rows, r)
			added++
		}
		if len(jobNumbers) >= count {
			return rows, nil
		}
		if added == 0 {
			return nil, fmt.Errorf("%w: page %d added no rows with %d of %d read; the listing shifted or changed layout",
				ErrShape, page, len(jobNumbers), count)
		}
		if page > count/pageSize+2 {
			return nil, fmt.Errorf("%w: listing did not end within %d pages", ErrShape, page)
		}
	}
}

// resultCount reads "N Result(s)". "600+" and anything over the cap fail:
// the board cannot list them all.
func resultCount(body []byte) (int, error) {
	m := countRE.FindSubmatch(body)
	if m == nil {
		return 0, fmt.Errorf("%w: no result count on the search page", ErrShape)
	}
	n, err := strconv.Atoi(strings.ReplaceAll(string(m[1]), ",", ""))
	if err != nil {
		return 0, fmt.Errorf("%w: result count %q", ErrShape, m[1])
	}
	if len(m[2]) > 0 || n > MaxPostings {
		return 0, fmt.Errorf("%w: search matches %s%s postings, over the %d limit; quote a phrase or add the location filter",
			srcutil.ErrTooManyPostings, m[1], m[2], MaxPostings)
	}
	return n, nil
}

func parseRows(body []byte) []row {
	var out []row
	for _, m := range rowRE.FindAllSubmatch(body, -1) {
		out = append(out, row{position: string(m[1]), jobNumber: string(m[1]) + "-" + string(m[2]), slug: string(m[3])})
	}
	return out
}

// jobsData is the part of a details page's hydration data this source reads.
type jobsData struct {
	PositionID              string   `json:"positionId"`
	JobNumber               string   `json:"jobNumber"`
	PostingTitle            string   `json:"postingTitle"`
	JobSummary              string   `json:"jobSummary"`
	Description             string   `json:"description"`
	Responsibilities        string   `json:"responsibilities"`
	MinimumQualifications   string   `json:"minimumQualifications"`
	PreferredQualifications string   `json:"preferredQualifications"`
	HighJobTitle            string   `json:"highJobTitle"`
	LowJobTitle             string   `json:"lowJobTitle"`
	TeamNames               []string `json:"teamNames"`
	PostDateInGMT           string   `json:"postDateInGMT"`
	HomeOffice              bool     `json:"homeOffice"`
	EmploymentType          string   `json:"employmentType"`
	Locations               []struct {
		Name          string `json:"name"`
		City          string `json:"city"`
		StateProvince string `json:"stateProvince"`
		CountryName   string `json:"countryName"`
	} `json:"locations"`
	PostingFooters []struct {
		Localizations map[string][]struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		} `json:"localizations"`
	} `json:"postingFooters"`
}

func (s *source) detail(ctx context.Context, b sources.Board, r row, seen time.Time) (posting.Posting, error) {
	u := site + "/en-us/details/" + r.jobNumber + "/" + r.slug
	body, err := srcutil.GetBody(ctx, s.d, u)
	if err != nil {
		if errors.Is(err, srcutil.ErrBoardNotFound) {
			return posting.Posting{}, errDetailGone
		}
		return posting.Posting{}, err
	}
	jd, err := decodeJobsData(body)
	if err != nil {
		return posting.Posting{}, err
	}
	if jd.PositionID != r.position {
		return posting.Posting{}, fmt.Errorf("%w: details page is position %q, the search row named %q", ErrShape, jd.PositionID, r.position)
	}
	return jd.posting(b, u, seen)
}

// decodeJobsData reads the JSON string literal passed to JSON.parse, then the
// object it holds. Nothing after the literal is guessed at.
func decodeJobsData(body []byte) (jobsData, error) {
	at := bytes.Index(body, hydrationTag)
	if at < 0 {
		return jobsData{}, fmt.Errorf("%w: no hydration data on the details page", ErrShape)
	}
	dec := json.NewDecoder(bytes.NewReader(body[at+len(hydrationTag):]))
	var lit string
	if err := dec.Decode(&lit); err != nil {
		return jobsData{}, fmt.Errorf("%w: hydration data is not a JSON string: %v", ErrShape, err)
	}
	var doc struct {
		LoaderData struct {
			JobDetails *struct {
				JobsData *jobsData `json:"jobsData"`
			} `json:"jobDetails"`
		} `json:"loaderData"`
	}
	if err := json.Unmarshal([]byte(lit), &doc); err != nil {
		return jobsData{}, fmt.Errorf("%w: hydration data: %v", ErrShape, err)
	}
	if doc.LoaderData.JobDetails == nil || doc.LoaderData.JobDetails.JobsData == nil {
		return jobsData{}, fmt.Errorf("%w: hydration data has no jobDetails.jobsData", ErrShape)
	}
	return *doc.LoaderData.JobDetails.JobsData, nil
}

var positionID = regexp.MustCompile(`^[0-9]{1,15}$`)

func (jd jobsData) posting(b sources.Board, u string, seen time.Time) (posting.Posting, error) {
	if !positionID.MatchString(jd.PositionID) {
		return posting.Posting{}, fmt.Errorf("%w: position id %q", ErrShape, jd.PositionID)
	}
	key, err := posting.AppleKey(b.Token, jd.PositionID)
	if err != nil {
		return posting.Posting{}, err
	}
	title := strings.TrimSpace(html.UnescapeString(jd.PostingTitle))
	if title == "" {
		return posting.Posting{}, fmt.Errorf("%w: empty postingTitle", ErrShape)
	}
	posted, err := srcutil.ParseTime("postDateInGMT", jd.PostDateInGMT)
	if err != nil {
		return posting.Posting{}, err
	}
	var locs []string
	for _, l := range jd.Locations {
		parts := []string{}
		for _, x := range []string{l.City, l.StateProvince, l.CountryName} {
			if x = strings.TrimSpace(x); x != "" && !slices.Contains(parts, x) {
				parts = append(parts, x)
			}
		}
		if len(parts) == 0 && strings.TrimSpace(l.Name) != "" {
			parts = append(parts, strings.TrimSpace(l.Name))
		}
		if s := strings.Join(parts, ", "); s != "" && !slices.Contains(locs, s) {
			locs = append(locs, s)
		}
	}
	company := b.Company
	if company == "" {
		company = "Apple"
	}
	p := posting.Posting{
		Key:         key,
		ATS:         posting.Apple,
		Company:     company,
		Title:       title,
		Location:    strings.Join(locs, "; "),
		Remote:      jd.HomeOffice,
		URL:         u,
		PostedAt:    posted,
		Description: description(jd),
		SeenAt:      seen,
	}
	if len(jd.TeamNames) > 0 {
		p.Department = strings.TrimSpace(jd.TeamNames[0])
	}
	return p, nil
}

// description is the posting's sections as text. Apple's level range
// ("Machine Learning ICT4" to "ICT5") leads, because seniority decides fit;
// the US pay footer, where present, closes it.
func description(jd jobsData) string {
	var parts []string
	if lv := levelLine(jd.LowJobTitle, jd.HighJobTitle); lv != "" {
		parts = append(parts, lv)
	}
	for _, sec := range []struct{ head, body string }{
		{"Summary", jd.JobSummary},
		{"Description", jd.Description},
		{"Responsibilities", jd.Responsibilities},
		{"Minimum Qualifications", jd.MinimumQualifications},
		{"Preferred Qualifications", jd.PreferredQualifications},
	} {
		if t := strings.TrimSpace(sec.body); t != "" {
			parts = append(parts, sec.head+"\n"+t)
		}
	}
	for _, f := range jd.PostingFooters {
		for _, x := range f.Localizations["en_US"] {
			if strings.Contains(x.Name, "Pay") && strings.TrimSpace(x.Content) != "" {
				parts = append(parts, x.Name+"\n"+htmltext.Text(x.Content, srcutil.MaxDescription))
			}
		}
		break // one footer: they repeat per location
	}
	text := html.UnescapeString(strings.Join(parts, "\n\n"))
	if len(text) > srcutil.MaxDescription {
		text = strings.ToValidUTF8(text[:srcutil.MaxDescription], "")
	}
	return text
}

func levelLine(low, high string) string {
	low, high = strings.TrimSpace(low), strings.TrimSpace(high)
	switch {
	case low == "" && high == "":
		return ""
	case low == "" || low == high:
		return "Level: " + firstNonEmpty(high, low)
	case high == "":
		return "Level: " + low
	}
	return "Level: " + low + " to " + high
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
