package workday

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// Workday serves at most window rows of one listing query (seen live on
// NVIDIA, 2026-10-19): it reports total capped at window, and every offset at
// or past it answers page 1 again with HTTP 200. Its facet counts are not
// capped, so a larger board is read by splitting it on one of its own facets
// into queries each under the window.
const (
	window = 2000
	// maxSplitDepth bounds how many facets one query may be split by in turn.
	maxSplitDepth = 3
	// splitRequests is the room listRequests leaves for split queries' first
	// pages and the short or empty pages that end each query.
	splitRequests = 100
	// maxFacetValues is the most values read from one facet of an answer; a
	// facet with more is partial and never used to split or count.
	maxFacetValues = 1000
	// maxFacets bounds the facets read from one answer, nested groups included.
	maxFacets = 50
	// maxFacetDepth bounds how deep a nested facet group is read.
	maxFacetDepth = 3
)

var facetID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// facetChoice is one facet a listing answer offers: the request parameter
// that applies it and its values' job counts.
type facetChoice struct {
	param  string
	values []facetValue
	// partial marks a facet with a value that could not be read or past
	// maxFacetValues: its buckets do not cover what it counts.
	partial bool
}

type facetValue struct {
	id    string
	count int
}

func (f facetChoice) sum() int {
	n := 0
	for _, v := range f.values {
		n += v.count
	}
	return n
}

func (f facetChoice) location() bool { return strings.Contains(strings.ToLower(f.param), "location") }

// listPage is one listing answer as read: total is nil when the answer gave
// none that can be believed.
type listPage struct {
	total  *int
	rows   []listedJob
	facets []facetChoice
}

type rawListResponse struct {
	Total       json.RawMessage `json:"total"`
	JobPostings []listedJob     `json:"jobPostings"`
	Facets      json.RawMessage `json:"facets"`
}

// readTotal is a total that is a non-negative integer, else nil: a missing,
// null, negative, fractional or non-numeric total says nothing.
func readTotal(raw json.RawMessage) *int {
	var f *float64 // null leaves it nil
	if len(raw) == 0 || json.Unmarshal(raw, &f) != nil || f == nil || *f < 0 || *f != math.Trunc(*f) || *f > math.MaxInt32 {
		return nil
	}
	n := int(*f)
	return &n
}

// readFacets reads the facets of a listing answer, nested groups (Workday's
// location groups) flattened. Anything unreadable marks its facet partial or
// is skipped; it never fails the answer, because facets only size and split.
func readFacets(raw json.RawMessage) []facetChoice {
	var nodes []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &nodes) != nil {
		return nil
	}
	var out []facetChoice
	var visit func(node json.RawMessage, depth int)
	visit = func(node json.RawMessage, depth int) {
		if depth > maxFacetDepth || len(out) >= maxFacets {
			return
		}
		var f struct {
			FacetParameter string            `json:"facetParameter"`
			Values         []json.RawMessage `json:"values"`
		}
		if json.Unmarshal(node, &f) != nil {
			return
		}
		c := facetChoice{param: f.FacetParameter, partial: len(f.Values) > maxFacetValues}
		seen := map[string]bool{}
		for i, v := range f.Values {
			if i >= maxFacetValues {
				break
			}
			var leaf struct {
				ID     *string          `json:"id"`
				Count  *float64         `json:"count"`
				Values *json.RawMessage `json:"values"`
			}
			if json.Unmarshal(v, &leaf) != nil {
				c.partial = true
				continue
			}
			if leaf.Values != nil && bytes.HasPrefix(bytes.TrimSpace(*leaf.Values), []byte("[")) {
				visit(v, depth+1)
				continue
			}
			if leaf.ID == nil || leaf.Count == nil {
				c.partial = true
				continue
			}
			id := strings.ToLower(*leaf.ID)
			n := *leaf.Count
			if !facetID.MatchString(id) || n < 0 || n != math.Trunc(n) || n > math.MaxInt32 {
				c.partial = true
				continue
			}
			if seen[id] { // the same bucket twice would be walked twice
				continue
			}
			seen[id] = true
			c.values = append(c.values, facetValue{id: id, count: int(n)})
		}
		if validFacetName(c.param) && len(c.values) > 0 {
			out = append(out, c)
		}
	}
	for _, n := range nodes {
		visit(n, 0)
	}
	return out
}

// validFacetName is a facet parameter safe to send back as an appliedFacets key.
func validFacetName(s string) bool {
	if s == "" || len(s) > 64 || !(s[0] >= 'a' && s[0] <= 'z' || s[0] >= 'A' && s[0] <= 'Z') {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// usable reports whether f can size or split a query under applied: it is
// whole, and the query has not applied it. Inside a facet-filtered query the
// applied facet counts its other options across the whole board, so its
// counts never describe this query.
func usable(f facetChoice, applied map[string][]string) bool {
	_, on := applied[f.param]
	return !f.partial && !on
}

// expectedTotal is the board's own count of a query's jobs: total when under
// the window, else the largest sum of a facet's buckets (facets count past
// the window). Location facets are left out (a job in three places counts
// three times). corroborated says two facets agree on it, or the total itself
// is believable: one facet alone cannot show which jobs it leaves out.
func expectedTotal(total *int, facets []facetChoice, applied map[string][]string) (count int, corroborated bool) {
	if total != nil && *total < window {
		return *total, true
	}
	var sums []int
	for _, f := range facets {
		if usable(f, applied) && !f.location() {
			sums = append(sums, f.sum())
		}
	}
	if total != nil {
		count = *total
	}
	for _, s := range sums {
		count = max(count, s)
	}
	agree := 0
	for _, s := range sums {
		if s == count {
			agree++
		}
	}
	return count, agree >= 2
}

// chooseSplit picks the facet to split an over-window query by: one the
// query has not applied, with at least two non-empty buckets, whose buckets
// add up to every job (a facet some jobs lack would lose them), with the
// fewest buckets still over the window. Location facets come last: their
// counts are inflated, so "fewer buckets over" proves nothing about them.
// ok is false when no facet can split.
func chooseSplit(facets []facetChoice, applied map[string][]string) (facetChoice, bool) {
	var open []facetChoice
	for _, f := range facets {
		nonEmpty := 0
		for _, v := range f.values {
			if v.count > 0 {
				nonEmpty++
			}
		}
		if usable(f, applied) && nonEmpty >= 2 {
			open = append(open, f)
		}
	}
	if len(open) == 0 {
		return facetChoice{}, false
	}
	// The board's count is the largest non-location sum (location counts are
	// inflated), or the largest location sum when only location facets split.
	var plain []facetChoice
	for _, f := range open {
		if !f.location() {
			plain = append(plain, f)
		}
	}
	if len(plain) == 0 {
		plain = open
	}
	expected := 0
	for _, f := range plain {
		expected = max(expected, f.sum())
	}
	rank := func(f facetChoice) [4]int {
		var r [4]int
		if f.sum() < expected {
			r[0] = 1
		}
		if f.location() {
			r[1] = 1
		}
		for _, v := range f.values {
			if v.count >= window {
				r[2]++
			}
		}
		r[3] = len(f.values)
		return r
	}
	sort.SliceStable(open, func(i, j int) bool {
		ri, rj := rank(open[i]), rank(open[j])
		for k := range ri {
			if ri[k] != rj[k] {
				return ri[k] < rj[k]
			}
		}
		return false
	})
	return open[0], true
}

// walker lists a board's rows into rows, in listing order, each externalPath
// once across every search term and every query of the board.
type walker struct {
	s        *source
	endpoint string
	limit    int    // the most rows the board may list (capFor)
	term     string // the search term being walked
	seen     map[string]bool
	rows     []listedJob
	requests int // listing requests, every term's
	queries  int // queries walked, a split query's buckets each counted
}

func newWalker(s *source, endpoint string, limit int) *walker {
	return &walker{s: s, endpoint: endpoint, limit: limit, seen: map[string]bool{}}
}

// listRequests bounds one board's listing requests, splits included: every
// row of its limit twice over, plus room for the split queries.
func listRequests(limit int) int { return 2*limit/pageSize + splitRequests }

func (w *walker) page(ctx context.Context, applied map[string][]string, offset int) (listPage, error) {
	if err := ctx.Err(); err != nil {
		return listPage{}, err
	}
	if n := listRequests(w.limit); w.requests >= n {
		return listPage{}, fmt.Errorf("%w: the listing needs more than %d requests; set the board's search to narrow it",
			srcutil.ErrTooManyPostings, n)
	}
	w.requests++
	var resp rawListResponse
	req := listRequest{AppliedFacets: applied, Limit: pageSize, Offset: offset, SearchText: w.term}
	if err := w.s.postJSON(ctx, w.endpoint, req, &resp); err != nil {
		return listPage{}, err
	}
	// A search with no match still answers "jobPostings":[]; a reply without
	// the array is an error page or a new shape, never an empty board.
	if resp.JobPostings == nil {
		return listPage{}, fmt.Errorf("listing at offset %d has no jobPostings array", offset)
	}
	for _, j := range resp.JobPostings {
		// A row that cannot be fetched or matched cannot be accounted for, and
		// leaving it out would age its posting as absent.
		if _, err := escapePath(j.ExternalPath); err != nil {
			return listPage{}, fmt.Errorf("listing at offset %d: %w", offset, err)
		}
	}
	return listPage{total: readTotal(resp.Total), rows: resp.JobPostings, facets: readFacets(resp.Facets)}, nil
}

// add records rows new to this query into here and new to the board into
// w.rows, and returns how many were new to this query.
func (w *walker) add(rows []listedJob, here map[string]bool) (int, error) {
	n := 0
	for _, r := range rows {
		if here[r.ExternalPath] {
			continue
		}
		here[r.ExternalPath] = true
		n++
		if w.seen[r.ExternalPath] {
			continue
		}
		if len(w.seen) >= w.limit {
			return n, fmt.Errorf("%w: board lists more than %d postings; set the board's search to narrow it or raise max_postings",
				srcutil.ErrTooManyPostings, w.limit)
		}
		w.seen[r.ExternalPath] = true
		w.rows = append(w.rows, r)
	}
	return n, nil
}

// shortfall is how many rows of a counted query may be missing before the
// listing is refused: a hundredth, at least one. A posting closing mid-walk
// shifts later rows up a page, so one is skipped; more is the listing failing.
func shortfall(count int) int { return max(1, count/100) }

// errIncomplete is a listing that ended before it could account for every
// job the board counts. It fails the board: a partial listing would make the
// reconciler age healthy postings as absent.
var errIncomplete = errors.New("listing is incomplete")

// pagedTotal is the first page's total as a stop for paging a query under the
// window: how many jobs the board counts for it. ok is false when the total
// cannot be believed: missing, 0 beside rows (a page cannot list jobs a board
// counts none of), or under the rows the same answer holds. The paging then
// goes by its rows. A total of 0 beside no rows is not asked about: an empty
// first page ends the query by itself.
func pagedTotal(first listPage) (stop int, ok bool) {
	if first.total == nil || *first.total < len(first.rows) || *first.total <= 0 || *first.total >= window {
		return 0, false
	}
	return *first.total, true
}

// walk lists every row of the query applied (with w.term) and returns the
// externalPaths it found, its buckets' included. Over the window the query is
// split by a facet.
func (w *walker) walk(ctx context.Context, applied map[string][]string, depth int) (map[string]bool, error) {
	first, err := w.page(ctx, applied, 0)
	if err != nil {
		return nil, err
	}
	w.queries++
	count, corroborated := expectedTotal(first.total, first.facets, applied)
	// The board's own count over the limit fails it before any paging: the
	// count is a total or a non-location facet sum, never an inflated one.
	if count > w.limit {
		return nil, fmt.Errorf("%w: board lists %d postings, over the %d limit; set the board's search to narrow it or raise max_postings",
			srcutil.ErrTooManyPostings, count, w.limit)
	}
	here := map[string]bool{}
	if _, err := w.add(first.rows, here); err != nil {
		return nil, err
	}

	if count >= window {
		split, ok := facetChoice{}, false
		if depth < maxSplitDepth {
			split, ok = chooseSplit(first.facets, applied)
		}
		if !ok {
			return nil, fmt.Errorf("%w: a part of the board counts %d jobs, over Workday's %d-row window, and no facet splits it; set the board's search to narrow it",
				errIncomplete, count, window)
		}
		if !corroborated {
			return nil, fmt.Errorf("%w: a part of the board counts %d jobs by one facet only, so the jobs that facet leaves out cannot be ruled out",
				errIncomplete, count)
		}
		// Buckets run in turn: egress paces one host serially anyway.
		for _, v := range split.values {
			if v.count == 0 {
				continue
			}
			sub := make(map[string][]string, len(applied)+1)
			for k, vs := range applied {
				sub[k] = vs
			}
			sub[split.param] = []string{v.id}
			found, err := w.walk(ctx, sub, depth+1)
			if err != nil {
				return nil, err
			}
			for p := range found {
				here[p] = true
			}
		}
		if len(here) < count-shortfall(count) {
			return nil, fmt.Errorf("%w: split by %s, the board listed %d of the %d jobs it counts",
				errIncomplete, split.param, len(here), count)
		}
		return here, nil
	}

	// Paged until a short or empty page, or the first page's total is reached.
	// Page 1 served again before that, or a page with nothing new, is Workday's
	// wrap (or a stuck cursor), never more rows.
	//
	// The total is read from the first page only: Workday reports 0 on every
	// later page (checked live, salesforce/External_Career_Site, 2026-10-22),
	// and a later page's total, when it differs, is not followed: a stop that
	// waited on a moving count could not be told from a wrap, and a posting
	// closing mid-walk only moves rows up a page. Workday answers an offset at
	// or past the total with page 1 again, so a total that is an exact multiple
	// of the page size ends on a full page and the next page is that wrap: once
	// every counted job has been read, the wrap ends the listing. The page past
	// the total is still asked for, never assumed, so a first total that
	// understates the board cannot end the listing early: rows past it are read
	// as rows (and absent postings are never aged on a guess). A total that
	// cannot be believed (see pagedTotal) sets no stop and the walk reads the
	// rows alone, as before.
	stop, known := pagedTotal(first)
	ended := len(first.rows) < pageSize
	firstPath := ""
	if len(first.rows) > 0 {
		firstPath = first.rows[0].ExternalPath
	}
	for offset := pageSize; !ended && offset < window; offset += pageSize {
		p, err := w.page(ctx, applied, offset)
		if err != nil {
			return nil, err
		}
		if len(p.rows) == 0 {
			ended = true
			break
		}
		if firstPath != "" && p.rows[0].ExternalPath == firstPath {
			if known && offset >= stop && len(here) >= stop {
				ended = true // the wrap past the total: every counted job was read
				break
			}
			if known {
				return nil, fmt.Errorf("%w: the board served its first page again at offset %d, after %d jobs, short of the %d it counts", errIncomplete, offset, len(here), stop)
			}
			return nil, fmt.Errorf("%w: the board served its first page again at offset %d, after %d jobs", errIncomplete, offset, len(here))
		}
		n, err := w.add(p.rows, here)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, fmt.Errorf("%w: the board repeated a page at offset %d, after %d jobs", errIncomplete, offset, len(here))
		}
		ended = len(p.rows) < pageSize
	}
	if !ended {
		return nil, fmt.Errorf("%w: a part of the board gave no usable count and listed past Workday's %d-row window", errIncomplete, window)
	}
	// A query under the window that ends short of its total is kept, as before
	// (docs/FOLLOWUPS.md, "Absence ages every open posting"): a live total that
	// overstates would otherwise fail the board on every run. Rows a split's
	// buckets lose are still caught against the facet count above.
	return here, nil
}
