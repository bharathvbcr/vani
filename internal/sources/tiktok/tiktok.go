// Package tiktok lists postings from TikTok's public careers site
// (lifeattiktok.com), through the JSON API the site's own search page calls
// (all checked live on 2026-10-28):
//
//	POST https://api.lifeattiktok.com/api/v1/public/supplier/search/job/posts
//	POST https://api.lifeattiktok.com/api/v1/public/supplier/config/job/filters
//
// Both take the header "website-path: tiktok" (the constant the site's own
// script sends; ByteDance's "bytedance" answered 400, so it is not a board
// here) and a JSON body. The search body is the one the site sends, with an
// empty "keyword" for an unsearched listing, "limit" 100 and "offset" paging.
//
// The answer carries data.count, the board's total for that query, and
// data.job_post_list, whose rows hold the whole posting: a description and a
// requirements text (plain text with line breaks, no HTML in any of the 4,318
// rows), the title, one city_info chain (city, state, country) and the job
// category. So a listing is one request per hundred rows and there is no detail
// request. Each row is one posting at one city; a role open in two cities is
// two rows with two ids. The job page is https://lifeattiktok.com/search/{id},
// the link the site's own search page gives every row.
//
// What the API does not give, and so is left out rather than invented:
//   - No posted date. publish_time is not a key of any of the 4,318 rows (the
//     researcher's notes had it as null), so PostedAt is zero.
//   - No pay. job_post_info.min_salary, max_salary and currency were null in
//     all 4,318 rows, so there is no pay line; add one only once a real row
//     carries a value.
//   - No remote flag, so Remote is false.
//
// The unsearched global board was 4,318 rows in 44 pages of about 0.5 MB, over
// 40 requests at the one-second pace. Narrow it with the board's search terms
// (keyword: "software engineer" gave 751) or with filters. The site filters by
// city code only ("CN_6", a country code, answered zero rows), so a "country"
// filter names an English country as the site's city list spells it ("United
// States of America") and the source reads the site's own city list
// (config/job/filters) to expand it to that country's city codes, and a
// "location" filter names city codes of that list. The union of every code
// goes in one location_code_list: the 19 United States codes answered 1,847
// rows, exactly the 1,847 United States rows of the whole 4,318. A name or code
// the list does not hold fails the board, never an empty listing, and so does a
// row outside the requested cities, which is how an API that stopped honouring
// the codes would show. The list holds 66 cities and covered every posted city
// on 2026-10-28, but it is a config list, not a promise.
//
// robots.txt: lifeattiktok.com disallows /referral/ for every agent and allows
// the rest; api.lifeattiktok.com/robots.txt answers 200 with an empty body.
package tiktok

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	apiBase    = "https://api.lifeattiktok.com/api/v1/public/supplier"
	searchURL  = apiBase + "/search/job/posts"
	filtersURL = apiBase + "/config/job/filters"
	// jobPageURL is where the site's own listing links a posting.
	jobPageURL = "https://lifeattiktok.com/search/"

	// pageSize is the page the site asks for.
	pageSize = 100
	maxPages = srcutil.MaxPostings/pageSize + 2
	// pageBytes bounds one search answer: a page was 0.45 to 0.5 MB.
	pageBytes = 8 << 20
	// filtersBytes bounds the city list answer, 26 KB for 66 cities.
	filtersBytes = 1 << 20
	// maxCities bounds the city list read.
	maxCities = 2000
	// pace spaces the board's requests: the API's robots.txt is empty, so this
	// is the courtesy floor the wave-2 brief sets.
	pace = time.Second
	// paceBudget is 600 paced requests, over the 100 pages of a 10,000 row board.
	paceBudget = 10 * time.Minute
)

// ErrShape is a response whose layout is not the one this source knows.
var ErrShape = errors.New("tiktok: unexpected response")

// sites maps a board token to the website-path header value the API is sent.
// The header is never built from the token: only a value of this table is
// sent, so a token cannot carry a header (or another site) into the request.
var sites = map[string]string{"tiktok": "tiktok"}

var (
	// idRE is a job post id: a numeric string, one URL path segment.
	idRE = regexp.MustCompile(`^[0-9]{1,20}$`)
	// codeRE is a city code of the site's list: "CT_223", "MDCY00039300".
	codeRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,40}$`)
)

type source struct {
	d   egress.Doer
	now func() time.Time
	// wait spaces requests (srcutil.Pacer.Sleep); a field only so tests need
	// not wait in real time.
	wait func(ctx context.Context, d time.Duration) error
}

// New returns the TikTok source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now, wait: srcutil.Sleep} }

func (*source) ATS() posting.ATS { return posting.TikTok }

// searchBody is the request the site's search page sends; every list is
// present and empty unless a filter fills it.
type searchBody struct {
	Keyword           string   `json:"keyword"`
	Limit             int      `json:"limit"`
	Offset            int      `json:"offset"`
	JobCategoryIDList []string `json:"job_category_id_list"`
	TagIDList         []string `json:"tag_id_list"`
	LocationCodeList  []string `json:"location_code_list"`
	SubjectIDList     []string `json:"subject_id_list"`
	RecruitmentIDList []string `json:"recruitment_id_list"`
	PortalType        int      `json:"portal_type"`
	JobFunctionIDList []string `json:"job_function_id_list"`
	StorefrontIDList  []string `json:"storefront_id_list"`
	PortalEntrance    int      `json:"portal_entrance"`
}

// node is one level of a place or category chain: a city whose parent is its
// state and whose parent is its country, or a category under its parent.
type node struct {
	Code   string `json:"code"`
	EnName string `json:"en_name"`
	Parent *node  `json:"parent"`
}

func (n *node) name() string {
	if n == nil {
		return ""
	}
	return n.EnName
}

func (n *node) parent() *node {
	if n == nil {
		return nil
	}
	return n.Parent
}

type row struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Requirement string `json:"requirement"`
	RecruitType *node  `json:"recruit_type"`
	JobCategory *node  `json:"job_category"`
	CityInfo    *node  `json:"city_info"`
}

type searchAnswer struct {
	Code    *int   `json:"code"`
	Message string `json:"message"`
	Data    *struct {
		Count       *int  `json:"count"`
		JobPostList []row `json:"job_post_list"`
	} `json:"data"`
}

type filtersAnswer struct {
	Code    *int   `json:"code"`
	Message string `json:"message"`
	Data    *struct {
		CityList []node `json:"city_list"`
	} `json:"data"`
}

// lister is one List call: the board's pacer, its resolved city codes and the
// postings gathered across its search terms.
type lister struct {
	s     *source
	b     sources.Board
	site  string
	pace  srcutil.Pacer
	seen  time.Time
	codes []string        // the location_code_list sent; empty is no filter
	only  map[string]bool // the same codes, as a set; nil when there is no filter
	have  map[string]bool // ids already listed, by an earlier term
	out   []posting.Posting
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := srcutil.CheckBoard(b, posting.TikTok); err != nil {
		return nil, err
	}
	site, ok := sites[strings.ToLower(b.Token)]
	if !ok {
		return nil, fmt.Errorf("tiktok %q: token must be tiktok, the one site this source reads", b.Token)
	}
	if err := sources.CheckFilters(posting.TikTok, b.Filters); err != nil {
		return nil, fmt.Errorf("tiktok %q: %w", b.Token, err)
	}
	l := &lister{s: s, b: b, site: site, seen: s.now(), have: map[string]bool{},
		pace: srcutil.Pacer{Every: pace, Budget: paceBudget, Sleep: s.wait}}
	if len(b.Filters) > 0 {
		if err := l.resolveFilters(ctx); err != nil {
			return nil, fmt.Errorf("tiktok %q: %w", b.Token, err)
		}
	}
	terms := b.SearchTerms()
	if len(terms) == 0 {
		terms = []string{""}
	}
	for _, t := range terms {
		if err := l.run(ctx, t); err != nil {
			if t == "" {
				return nil, fmt.Errorf("tiktok %q: listing: %w", b.Token, err)
			}
			return nil, fmt.Errorf("tiktok %q: search %q: %w", b.Token, t, err)
		}
	}
	return l.out, nil
}

func (l *lister) header() http.Header { return http.Header{"Website-Path": {l.site}} }

// squash is a name as compared: whitespace folded, lower case.
func squash(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// resolveFilters turns the board's country names and city codes into the
// location_code_list, through the site's own city list. A country with no city
// and a code not in the list are errors, because the site answers zero rows
// for a code it does not know and the board would look empty.
func (l *lister) resolveFilters(ctx context.Context) error {
	if err := l.pace.Wait(ctx); err != nil {
		return err
	}
	var ans filtersAnswer
	if err := srcutil.PostJSONLimit(ctx, l.s.d, filtersURL, struct{}{}, l.header(), filtersBytes, &ans); err != nil {
		return fmt.Errorf("city list: %w", err)
	}
	if ans.Code == nil || *ans.Code != 0 || ans.Data == nil || ans.Data.CityList == nil || len(ans.Data.CityList) > maxCities {
		return fmt.Errorf("%w: city list answer is not ok (code %s)", ErrShape, codeText(ans.Code))
	}
	byCode := map[string]bool{}
	byCountry := map[string][]string{}
	for i, c := range ans.Data.CityList {
		if !codeRE.MatchString(c.Code) {
			return fmt.Errorf("%w: city list row %d has code %q", ErrShape, i, c.Code)
		}
		byCode[c.Code] = true
		country := squash(c.Parent.parent().name())
		if country != "" {
			byCountry[country] = append(byCountry[country], c.Code)
		}
	}
	add := func(code string) {
		if !slices.Contains(l.codes, code) {
			l.codes = append(l.codes, code)
		}
	}
	for _, name := range l.b.Filters["country"] {
		cities := byCountry[squash(name)]
		if len(cities) == 0 {
			return fmt.Errorf("country %q is not one the site's city list holds (spell it as the site does, e.g. United States of America)", name)
		}
		for _, c := range cities {
			add(c)
		}
	}
	for _, code := range l.b.Filters["location"] {
		code = strings.TrimSpace(code)
		if !byCode[code] {
			return fmt.Errorf("location %q is not a city code of the site's city list (e.g. CT_223)", code)
		}
		add(code)
	}
	l.only = make(map[string]bool, len(l.codes))
	for _, c := range l.codes {
		l.only[c] = true
	}
	return nil
}

func codeText(c *int) string {
	if c == nil {
		return "missing"
	}
	return fmt.Sprint(*c)
}

func (l *lister) page(ctx context.Context, term string, offset int) (count int, rows []row, err error) {
	if err := l.pace.Wait(ctx); err != nil {
		return 0, nil, err
	}
	body := searchBody{Keyword: term, Limit: pageSize, Offset: offset,
		JobCategoryIDList: []string{}, TagIDList: []string{}, LocationCodeList: []string{},
		SubjectIDList: []string{}, RecruitmentIDList: []string{}, JobFunctionIDList: []string{},
		StorefrontIDList: []string{}, PortalType: 6, PortalEntrance: 1}
	if l.codes != nil {
		body.LocationCodeList = l.codes
	}
	var ans searchAnswer
	if err := srcutil.PostJSONLimit(ctx, l.s.d, searchURL, body, l.header(), pageBytes, &ans); err != nil {
		return 0, nil, err
	}
	if ans.Code == nil || *ans.Code != 0 {
		return 0, nil, fmt.Errorf("%w: code %s, message %q", ErrShape, codeText(ans.Code), srcutil.Title(ans.Message))
	}
	if ans.Data == nil || ans.Data.Count == nil || *ans.Data.Count < 0 || ans.Data.JobPostList == nil {
		return 0, nil, fmt.Errorf("%w: no count or job_post_list", ErrShape)
	}
	return *ans.Data.Count, ans.Data.JobPostList, nil
}

// run pages one search term to its count. It refuses what would otherwise
// leave the board partial or looping: a count over the cap, a count that moves
// between pages, an id read twice, a short page before the count is reached,
// and an empty page short of it. A partial listing would age live postings.
func (l *lister) run(ctx context.Context, term string) error {
	read := map[string]bool{}
	total := -1
	offset := 0
	for page := 0; page < maxPages; page++ {
		count, rows, err := l.page(ctx, term, offset)
		if err != nil {
			return err
		}
		if total < 0 {
			total = count
			if err := srcutil.CheckCount(total); err != nil {
				return fmt.Errorf("%w; narrow it with a search term or a country filter", err)
			}
		} else if count != total {
			return fmt.Errorf("%w: the count moved from %d to %d at offset %d; the board changed while it was read", ErrShape, total, count, offset)
		}
		if len(rows) == 0 {
			if total == 0 && offset == 0 {
				return nil // a term with no match is a valid empty board
			}
			return fmt.Errorf("%w: offset %d answered no rows with %d of %d read", ErrShape, offset, len(read), total)
		}
		for i, r := range rows {
			if !idRE.MatchString(r.ID) {
				return fmt.Errorf("%w: offset %d row %d has id %q", ErrShape, offset, i, r.ID)
			}
			if read[r.ID] {
				return fmt.Errorf("%w: id %s read twice (offset %d); the paging repeated or wrapped", ErrShape, r.ID, offset)
			}
			read[r.ID] = true
			if l.have[r.ID] {
				continue // already listed by an earlier term
			}
			p, err := l.posting(r)
			if err != nil {
				return fmt.Errorf("id %s: %w", r.ID, err)
			}
			l.have[r.ID] = true
			l.out = append(l.out, p)
			if err := srcutil.CheckCount(len(l.out)); err != nil {
				return fmt.Errorf("%w: the board's terms together list more; narrow them", err)
			}
		}
		offset += len(rows)
		switch {
		case len(read) > total:
			return fmt.Errorf("%w: %d rows read but the count is %d", ErrShape, len(read), total)
		case len(read) == total:
			return nil
		case len(rows) < pageSize:
			return fmt.Errorf("%w: a page of %d rows with %d of %d read", ErrShape, len(rows), len(read), total)
		}
	}
	return fmt.Errorf("%w: listing did not end within %d pages", ErrShape, maxPages)
}

func (l *lister) posting(r row) (posting.Posting, error) {
	if l.only != nil && (r.CityInfo == nil || !l.only[r.CityInfo.Code]) {
		return posting.Posting{}, fmt.Errorf("%w: the row's city is outside the requested location codes", ErrShape)
	}
	title := srcutil.Title(r.Title)
	if title == "" {
		return posting.Posting{}, fmt.Errorf("%w: empty title", ErrShape)
	}
	key, err := posting.TikTokKey(l.b.Token, r.ID)
	if err != nil {
		return posting.Posting{}, err
	}
	if !strings.HasPrefix(string(key), l.b.KeyPrefix()) {
		return posting.Posting{}, fmt.Errorf("key %q is outside %q", key, l.b.KeyPrefix())
	}
	company := strings.TrimSpace(l.b.Company)
	if company == "" {
		company = "TikTok"
	}
	var city, state, country, parentCat string
	if r.CityInfo != nil {
		city, state, country = r.CityInfo.name(), r.CityInfo.Parent.name(), r.CityInfo.Parent.parent().name()
	}
	if r.JobCategory != nil {
		parentCat = r.JobCategory.Parent.name()
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.TikTok,
		Company:     company,
		Title:       title,
		Location:    srcutil.JoinDistinct(", ", city, state, country),
		Department:  srcutil.JoinDistinct(" / ", parentCat, r.JobCategory.name()),
		URL:         jobPageURL + r.ID,
		Description: description(r),
		SeenAt:      l.seen,
	}, nil
}

// description is the role line (the recruit type: "Regular", "Intern", ...,
// led by srcutil.Lead so the cap never cuts it), the description and the
// requirements. Both are plain text with line breaks, so they go through
// htmltext.Plain, which keeps the lines, drops control characters and bounds
// the bytes; they are untrusted and never read as markup.
func description(r row) string {
	lead, room := srcutil.Lead(srcutil.RoleLine(srcutil.EmploymentType(r.RecruitType.name())))
	return lead + htmltext.Plain(r.Description+"\n\n"+r.Requirement, room)
}
