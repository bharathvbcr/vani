// Package ibm lists postings from IBM's careers search API,
// POST https://www-api.ibm.com/search/api/v2 (appId "careers", scope
// "careers2"), the index behind careers.ibm.com. One request returns up to
// 100 postings with their full text, so a listing costs one request per 100
// postings and no detail requests. No cookie or login is needed (checked live
// 2026-10-27); the site's terms of use and rate limits for this host are not
// documented anywhere that was found, so the requests are few and ride the
// per-host pace and throttle every source gets from its context.
//
// Two boards read it, because IBM completed buying Confluent on 2026-03-17
// and lists Confluent's roles only in this index:
//
//   - token "ibm" is IBM's own listing;
//   - token "confluent" is Confluent's.
//
// The index has no brand field. A role is Confluent's when its title has the
// word "Confluent" in it (72 of 2,206 roles on 2026-10-27, every one of them
// titled so; a further 20 roles that mention Confluent in their text only are
// IBM's or unknowable, and stay on the "ibm" board). That one rule splits the
// index into two disjoint boards: "confluent" keeps the roles it holds and
// "ibm" drops them. Disjoint matters because a key is ib:{token}:{jobId} and
// the reconciler ages postings by that prefix; a role on both boards would be
// two live postings, and joburl.Normalize does not fold their links (it keeps
// "jobid" but IBM's parameter is "jobId"). The cost is that a watchlist with
// an "ibm" board and no "confluent" board never sees Confluent's roles.
//
// Every board is US-only (the index's country field is "United States"; 525
// of 2,206 roles). A search term matches the title only, every word required:
// "software engineer" is 30 US roles, where the same words anywhere in the
// text are 88 and the words in any order or place 475 of the 525. Postings
// carry the posting date (effectivedate) the API gives; the brief this source
// was written from said there was none, and the hits show it on every one.
package ibm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	apiURL = "https://www-api.ibm.com/search/api/v2"
	// jobHost is the host of every posting link; a link anywhere else is refused.
	jobHost = "careers.ibm.com"
	// country is the one country every board lists (term filter, exact case).
	country = "United States"
	// MaxPostings bounds one board (the union of its terms). It also keeps
	// every "from" small: the API refuses a large one (9,950 was a 400).
	MaxPostings = 500
	// pageSize is the size asked for; 100 was accepted and 500 refused (HTTP
	// 400) on 2026-10-27.
	pageSize = 100
	maxPages = MaxPostings/pageSize + 2
	// maxCities bounds the cities read from one posting (24 seen).
	maxCities = 50

	// TokenIBM and TokenConfluent are the two board tokens.
	TokenIBM       = "ibm"
	TokenConfluent = "confluent"
)

// ErrShape is a response whose layout is not the one this source knows.
var ErrShape = errors.New("ibm: unexpected search response")

var idRE = regexp.MustCompile(`^[0-9]{1,15}$`)

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the IBM careers source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (*source) ATS() posting.ATS { return posting.IBM }

// sourceFields is the explicit _source list asked for; without one the API
// returns hits with no _source at all.
var sourceFields = []string{
	"title", "url", "body", "field_text_01", "field_keyword_05", "field_keyword_08",
	"field_keyword_17", "field_keyword_18", "field_keyword_19", "field_keyword_21",
	"dcdate", "effectivedate",
}

type request struct {
	AppID  string   `json:"appId"`
	Scopes []string `json:"scopes"`
	Query  query    `json:"query"`
	From   int      `json:"from"`
	Size   int      `json:"size"`
	// Sort is by _id, which is unique: without a sort, hits that tie on
	// score come back in an order that differs from one request to the next,
	// and a walk by from/size then repeats some postings and skips others
	// (a 105-hit term listed 77 postings once and 78 the next, 2026-10-27).
	Sort   []string `json:"sort"`
	Source []string `json:"_source"`
}

type query struct {
	Bool struct {
		Must   []map[string]any `json:"must"`
		Filter []map[string]any `json:"filter"`
	} `json:"bool"`
}

func newRequest(term string, from int) request {
	r := request{AppID: "careers", Scopes: []string{"careers2"}, From: from, Size: pageSize, Sort: []string{"_id"}, Source: sourceFields}
	r.Query.Bool.Must = []map[string]any{{"simple_query_string": map[string]any{
		"query": term, "fields": []string{"title"}, "default_operator": "and",
	}}}
	r.Query.Bool.Filter = []map[string]any{{"term": map[string]any{"field_keyword_05": country}}}
	return r
}

type response struct {
	Hits *struct {
		Total *struct {
			Value    *int   `json:"value"`
			Relation string `json:"relation"`
		} `json:"total"`
		Hits []struct {
			Source *job `json:"_source"`
		} `json:"hits"`
	} `json:"hits"`
}

type job struct {
	ID            json.Number `json:"field_text_01"`
	Title         string      `json:"title"`
	URL           string      `json:"url"`
	Body          string      `json:"body"`
	Country       string      `json:"field_keyword_05"`
	Category      *string     `json:"field_keyword_08"`
	Arrangement   string      `json:"field_keyword_17"` // "Remote", "Hybrid" or empty
	Level         string      `json:"field_keyword_18"` // "Professional", "Entry Level", "Internship"
	Primary       string      `json:"field_keyword_19"` // "Austin, US" or "Multiple Cities"
	Cities        []string    `json:"field_keyword_21"`
	LastModified  string      `json:"dcdate"`        // YYYY-MM-DD, never before effectivedate
	EffectiveDate string      `json:"effectivedate"` // YYYY-MM-DD
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := srcutil.CheckBoard(b, posting.IBM); err != nil {
		return nil, err
	}
	token := strings.ToLower(strings.TrimSpace(b.Token))
	if token != TokenIBM && token != TokenConfluent {
		return nil, fmt.Errorf("ibm %q: the token must be %q or %q", b.Token, TokenIBM, TokenConfluent)
	}
	terms := b.SearchTerms()
	if len(terms) == 0 {
		return nil, fmt.Errorf("ibm %q: the board needs at least one search", b.Token)
	}
	if err := sources.CheckFilters(posting.IBM, b.Filters); err != nil {
		return nil, fmt.Errorf("ibm %q: %w", b.Token, err)
	}
	seen := s.now()
	var out []posting.Posting
	keys := map[posting.Key]bool{}
	for _, term := range terms {
		jobs, err := s.search(ctx, term)
		if err != nil {
			return nil, fmt.Errorf("ibm %q: search %q: %w", b.Token, term, err)
		}
		for _, j := range jobs {
			if confluentTitle(j.Title) != (token == TokenConfluent) {
				continue // the other board's role
			}
			p, err := j.posting(b, token, seen)
			if err != nil {
				return nil, fmt.Errorf("ibm %q: search %q: job %s: %w", b.Token, term, j.ID, err)
			}
			if !keys[p.Key] {
				keys[p.Key] = true
				out = append(out, p)
			}
		}
		if len(out) > MaxPostings {
			return nil, fmt.Errorf("ibm %q: %w: the board's searches together list more than %d postings; narrow them",
				b.Token, srcutil.ErrTooManyPostings, MaxPostings)
		}
	}
	return out, nil
}

// confluentTitle reports whether a title names Confluent: one of its words,
// split where the API's tokenizer splits (anything but letters, digits,
// apostrophes and underscores), is "confluent" in any case.
func confluentTitle(title string) bool {
	words := strings.FieldsFunc(unmangle(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\'' && r != '_'
	})
	for _, w := range words {
		if strings.EqualFold(w, "confluent") {
			return true
		}
	}
	return false
}

// shortfall is how many counted jobs a listing may fall short of its total:
// a posting closing mid-walk moves later ones up a page, so one is skipped. A
// hundredth, at least one; more is the listing failing.
func shortfall(total int) int { return max(1, total/100) }

// search pages one term to its total. The total counts every hit of the
// query, so it is the stop: paging ends when from reaches it, a page with no
// hits or no new ones before that is an incomplete listing, and a listing over
// MaxPostings fails before any paging. It never returns a short list as if it
// were whole.
func (s *source) search(ctx context.Context, term string) ([]job, error) {
	var out []job
	ids := map[string]bool{}
	total, from := -1, 0
	for page := 0; page < maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp response
		if err := srcutil.PostJSON(ctx, s.d, apiURL, newRequest(term, from), nil, &resp); err != nil {
			return nil, err
		}
		t, err := readTotal(resp)
		if err != nil {
			return nil, err
		}
		if total < 0 {
			if total = t; total > MaxPostings {
				return nil, fmt.Errorf("%w: search matches %d postings, over the %d limit; use a narrower term",
					srcutil.ErrTooManyPostings, total, MaxPostings)
			}
		}
		if total == 0 {
			return nil, nil
		}
		rows := resp.Hits.Hits
		if len(rows) == 0 {
			return nil, fmt.Errorf("%w: no hits at from=%d, with %d of %d read", ErrShape, from, from, total)
		}
		added := 0
		for _, h := range rows {
			if h.Source == nil {
				return nil, fmt.Errorf("%w: a hit has no _source", ErrShape)
			}
			id := h.Source.ID.String()
			if !idRE.MatchString(id) {
				return nil, fmt.Errorf("%w: field_text_01 %q", ErrShape, id)
			}
			if ids[id] {
				continue // paging shifted under a changing listing
			}
			ids[id] = true
			out = append(out, *h.Source)
			added++
		}
		from += len(rows)
		if from >= total {
			if len(out) < total-shortfall(total) {
				return nil, fmt.Errorf("%w: listed %d of the %d postings it counts", ErrShape, len(out), total)
			}
			return out, nil
		}
		if added == 0 {
			return nil, fmt.Errorf("%w: the page at from=%d added nothing, with %d of %d read", ErrShape, from-len(rows), len(out), total)
		}
	}
	return nil, fmt.Errorf("%w: listing did not end within %d pages", ErrShape, maxPages)
}

// readTotal is the answer's total hit count: present, not negative, and exact.
// A lower bound ("gte") cannot end a walk, so it is refused rather than used.
func readTotal(r response) (int, error) {
	if r.Hits == nil || r.Hits.Total == nil || r.Hits.Total.Value == nil || *r.Hits.Total.Value < 0 {
		return 0, fmt.Errorf("%w: no hits.total", ErrShape)
	}
	if r.Hits.Total.Relation != "eq" {
		return 0, fmt.Errorf("%w: hits.total is %q, not an exact count", ErrShape, r.Hits.Total.Relation)
	}
	return *r.Hits.Total.Value, nil
}

// date parses the API's YYYY-MM-DD; empty is the zero time.
func date(field, s string) (time.Time, error) {
	if s = strings.TrimSpace(s); s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s %q", ErrShape, field, s)
	}
	return t, nil
}

func (j job) posting(b sources.Board, token string, seen time.Time) (posting.Posting, error) {
	id := j.ID.String()
	if !idRE.MatchString(id) {
		return posting.Posting{}, fmt.Errorf("%w: field_text_01 %q", ErrShape, id)
	}
	if err := checkLink(j.URL, id); err != nil {
		return posting.Posting{}, err
	}
	key, err := posting.IBMKey(token, id)
	if err != nil {
		return posting.Posting{}, err
	}
	title := srcutil.Title(unmangle(j.Title))
	if title == "" {
		return posting.Posting{}, fmt.Errorf("%w: empty title", ErrShape)
	}
	posted, err := date("effectivedate", j.EffectiveDate)
	if err != nil {
		return posting.Posting{}, err
	}
	updated, err := date("dcdate", j.LastModified)
	if err != nil {
		return posting.Posting{}, err
	}
	company := b.Company
	if company == "" {
		company = "IBM"
		if token == TokenConfluent {
			company = "Confluent"
		}
	}
	p := posting.Posting{
		Key:         key,
		ATS:         posting.IBM,
		Company:     company,
		Title:       title,
		Location:    j.location(),
		Remote:      strings.EqualFold(strings.TrimSpace(j.Arrangement), "remote"),
		URL:         j.URL,
		PostedAt:    posted,
		UpdatedAt:   updated,
		Description: j.description(),
		SeenAt:      seen,
	}
	if j.Category != nil {
		p.Department = strings.TrimSpace(unmangle(*j.Category))
	}
	return p, nil
}

// checkLink accepts only the careers.ibm.com job link that names this job.
func checkLink(raw, id string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() != jobHost || u.Port() != "" || u.User != nil {
		return fmt.Errorf("%w: url %q is not a %s link", ErrShape, raw, jobHost)
	}
	if got := u.Query().Get("jobId"); got != id {
		return fmt.Errorf("%w: url %q does not name job %s", ErrShape, raw, id)
	}
	return nil
}

// location lists the posting's cities with the country, falling back to the
// primary location ("Austin, US") when the list is empty. The list holds a
// "No City" placeholder and some upper-case names ("POUGHKEEPSIE").
func (j job) location() string {
	ctry := strings.TrimSpace(unmangle(j.Country))
	var places []string
	seen := map[string]bool{}
	for _, c := range j.Cities {
		c = tidyCity(unmangle(c))
		if c == "" || strings.EqualFold(c, "No City") || seen[strings.ToLower(c)] {
			continue
		}
		seen[strings.ToLower(c)] = true
		if ctry != "" {
			c += ", " + ctry
		}
		if places = append(places, c); len(places) == maxCities {
			break
		}
	}
	if len(places) == 0 {
		if p := strings.TrimSpace(unmangle(j.Primary)); p != "" && !strings.EqualFold(p, "Multiple Cities") {
			return p
		}
		return ctry
	}
	return strings.Join(places, "; ")
}

// tidyCity trims a city and capitalizes it when the API sent it in capitals.
func tidyCity(c string) string {
	c = strings.Join(strings.Fields(c), " ")
	if c != strings.ToUpper(c) || c == strings.ToLower(c) {
		return c
	}
	words := strings.Fields(strings.ToLower(c))
	for i, w := range words {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

// description is the posting text, led by a srcutil.RoleLine of the role
// facts the API gives (level and work arrangement). The API's text is plain
// and carries, after the description, a dump of the posting's location,
// legal entity and skill tags; it is kept as the board wrote it.
func (j job) description() string {
	lead, room := srcutil.Lead(srcutil.RoleLine(
		strings.ToLower(strings.TrimSpace(j.Level)), strings.ToLower(strings.TrimSpace(j.Arrangement))))
	return lead + htmltext.Text(unmangle(j.Body), room)
}

// mangled maps the UTF-8 punctuation that the index holds read as Windows-1252
// and written as UTF-8 again ("worldâ€™s") back to what it was. Some of the
// index's text is stored that way (the first Confluent description of the
// fixture, not the second); all of it starts "â€", so nothing else is touched.
var mangled = strings.NewReplacer(
	"â€™", "’", "â€˜", "‘", "â€œ", "“", "â€\u009d", "”",
	"â€“", "–", "â€”", "—", "â€¢", "•", "â€¦", "…",
)

func unmangle(s string) string {
	if !strings.Contains(s, "â€") {
		return s
	}
	return mangled.Replace(s)
}
