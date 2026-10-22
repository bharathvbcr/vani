package workday

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// fakeJob is one job on a fakeBoard: its facet values by facet parameter.
type fakeJob struct {
	path, title string
	req         int
	attrs       map[string][]string
}

// fakeBoard serves a Workday listing the way the live service does (seen on
// NVIDIA, 2026-10-19): total is capped at window, offsets at or past the
// window serve page 1 again with HTTP 200, facets count past the window, and
// a facet's counts are taken over the query with that facet's own filter
// removed (so an applied facet counts its other options across the board).
// Facets are AND across parameters, OR within one.
type fakeBoard struct {
	jobs   []fakeJob
	params []string // facet parameters in answer order
	// nested lists the parameters served inside a location group.
	nested map[string]bool
	// total, when set, replaces the answer's total (raw JSON; "" omits it).
	total func(real int, applied map[string][]string) (string, bool)
	// wrapAt is the offset from which page 1 is served again (default window).
	wrapAt int
	// totalAt, when set, replaces the answer's total by the offset asked for
	// and takes precedence over total.
	totalAt func(offset, real int) (string, bool)
	// wrapPastEnd serves page 1 again at any offset at or past the query's last
	// row, as the live service does (salesforce/External_Career_Site,
	// 2026-10-22); otherwise such an offset serves an empty page.
	wrapPastEnd bool
	// page, when set, may rewrite a page's rows.
	page func(offset int, rows []fakeJob) []fakeJob
	// extraFacets is appended raw to the facets array.
	extraFacets []string

	mu      sync.Mutex
	posts   int
	gets    int
	applied []map[string][]string
	offsets []int // the offset of each listing request, in order
}

func (fb *fakeBoard) matches(j fakeJob, applied map[string][]string, skip, term string) bool {
	if term != "" && !strings.Contains(strings.ToLower(j.title), strings.ToLower(term)) {
		return false
	}
	for p, ids := range applied {
		if p == skip {
			continue
		}
		hit := false
		for _, id := range ids {
			for _, a := range j.attrs[p] {
				hit = hit || a == id
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

func (fb *fakeBoard) facetsJSON(applied map[string][]string, term string) string {
	var parts, group []string
	for _, p := range fb.params {
		counts := map[string]int{}
		var ids []string
		for _, j := range fb.jobs {
			for _, a := range j.attrs[p] {
				if _, ok := counts[a]; !ok {
					ids = append(ids, a)
					counts[a] = 0
				}
				if fb.matches(j, applied, p, term) {
					counts[a]++
				}
			}
		}
		sort.Strings(ids)
		var vals []string
		for _, id := range ids {
			vals = append(vals, fmt.Sprintf(`{"descriptor":"d","id":%q,"count":%d}`, id, counts[id]))
		}
		f := fmt.Sprintf(`{"facetParameter":%q,"descriptor":"d","values":[%s]}`, p, strings.Join(vals, ","))
		if fb.nested[p] {
			group = append(group, f)
		} else {
			parts = append(parts, f)
		}
	}
	if len(group) > 0 {
		parts = append(parts, `{"facetParameter":"locationMainGroup","values":[`+strings.Join(group, ",")+`]}`)
	}
	parts = append(parts, fb.extraFacets...)
	return "[" + strings.Join(parts, ",") + "]"
}

func (fb *fakeBoard) handle(r srctest.Request) (srctest.Reply, bool) {
	if r.Method == "POST" && r.URL == jobsURL {
		var req listRequest
		if err := json.Unmarshal(r.Body, &req); err != nil || req.Limit != pageSize {
			return srctest.Reply{Status: 400}, true
		}
		fb.mu.Lock()
		fb.posts++
		fb.applied = append(fb.applied, req.AppliedFacets)
		fb.offsets = append(fb.offsets, req.Offset)
		fb.mu.Unlock()
		var hits []fakeJob
		for _, j := range fb.jobs {
			if fb.matches(j, req.AppliedFacets, "", req.SearchText) {
				hits = append(hits, j)
			}
		}
		wrap := fb.wrapAt
		if wrap == 0 {
			wrap = window
		}
		offset := req.Offset
		if offset >= wrap || fb.wrapPastEnd && offset >= len(hits) && len(hits) > 0 {
			offset = 0 // Workday serves page 1 again, HTTP 200
		}
		var rows []fakeJob
		if offset < len(hits) {
			rows = hits[offset:min(offset+pageSize, len(hits))]
		}
		if fb.page != nil {
			rows = fb.page(req.Offset, rows)
		}
		var out []string
		for _, j := range rows {
			out = append(out, fmt.Sprintf(`{"title":%q,"externalPath":%q,"locationsText":"L","postedOn":"Posted Today"}`, j.title, j.path))
		}
		total := fmt.Sprint(min(len(hits), window))
		if fb.totalAt != nil {
			raw, present := fb.totalAt(req.Offset, len(hits))
			if !present {
				total = ""
			} else {
				total = raw
			}
		} else if fb.total != nil {
			raw, present := fb.total(len(hits), req.AppliedFacets)
			if !present {
				total = ""
			} else {
				total = raw
			}
		}
		body := `{"jobPostings":[` + strings.Join(out, ",") + `],"facets":` + fb.facetsJSON(req.AppliedFacets, req.SearchText)
		if total != "" {
			body += `,"total":` + total
		}
		return ok(body + "}"), true
	}
	if r.Method == "GET" && strings.HasPrefix(r.URL, siteURL+"/job/") {
		fb.mu.Lock()
		fb.gets++
		fb.mu.Unlock()
		path := strings.TrimPrefix(r.URL, siteURL)
		for _, j := range fb.jobs {
			if p, _ := escapePath(j.path); p == path {
				ext := "https://" + host + "/NVIDIAExternalCareerSite" + p
				if j.req%2 == 0 { // some answers carry a locale, as live ones may
					ext = "https://" + host + "/en-US/NVIDIAExternalCareerSite" + p
				}
				return ok(fmt.Sprintf(`{"jobPostingInfo":{"jobReqId":"JR%d","title":%q,"location":"Loc","jobDescription":"<p>about %d</p>","externalUrl":%q}}`,
					j.req, j.title, j.req, ext)), true
			}
		}
		return srctest.Reply{Status: 404}, true
	}
	return srctest.Reply{}, false
}

func fid(facet, n int) string { return fmt.Sprintf("%016x%016x", facet+1, n+1) }

// nvidiaBoard is shaped like nvidia/NVIDIAExternalCareerSite on 2026-10-19:
// 2753 jobs; timeType and workerSubType each sum to the board; jobFamilyGroup
// splits it into buckets under the window (one large, 1500); a nested
// location facet counts a job in two places twice. Two in three titles are
// "Senior" ones.
func nvidiaBoard(n int) *fakeBoard {
	fb := &fakeBoard{params: []string{"timeType", "workerSubType", "jobFamilyGroup", "locations"}, nested: map[string]bool{"locations": true}}
	for i := range n {
		group := 0
		if i >= 1500 {
			group = 1 + i%14
		}
		tt, ws := 0, 0
		if i%51 == 0 {
			tt = 1
		}
		if i%27 == 0 {
			ws = 1
		}
		locs := []string{fid(3, i%40)}
		if i%3 == 0 {
			locs = append(locs, fid(3, 40+i%7))
		}
		title := fmt.Sprintf("Engineer %d", i)
		if i%3 != 0 {
			title = "Senior " + title
		}
		fb.jobs = append(fb.jobs, fakeJob{
			path:  fmt.Sprintf("/job/US-CA-Santa-Clara/Job-%d_JR%d", i, 100000+i),
			title: title,
			req:   100000 + i,
			attrs: map[string][]string{"timeType": {fid(0, tt)}, "workerSubType": {fid(1, ws)}, "jobFamilyGroup": {fid(2, group)}, "locations": locs},
		})
	}
	return fb
}

func fakeSource(fb *fakeBoard, o *Options) (*source, *srctest.Doer) {
	d := srctest.NewFunc(fb.handle)
	return &source{d: d, now: func() time.Time { return fixedNow }, opt: o}, d
}

// listAll walks the board's listing alone (no detail step).
func listAll(t *testing.T, fb *fakeBoard, b sources.Board) ([]listedJob, *walker, error) {
	t.Helper()
	s, _ := fakeSource(fb, nil)
	w := newWalker(s, jobsURL, MaxPostings)
	terms := b.SearchTerms()
	if len(terms) == 0 {
		terms = []string{""}
	}
	for _, term := range terms {
		w.term = term
		if _, err := w.walk(context.Background(), map[string][]string{}, 0); err != nil {
			return nil, w, err
		}
	}
	return w.rows, w, nil
}

// The NVIDIA-shaped board lists every one of its 2753 jobs, each once, by
// splitting on jobFamilyGroup, never paging a query past the window.
func TestNVIDIABoardListsEveryJob(t *testing.T) {
	fb := nvidiaBoard(2753)
	rows, w, err := listAll(t, fb, board)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2753 {
		t.Fatalf("listed %d of 2753", len(rows))
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.ExternalPath] {
			t.Fatalf("%s listed twice", r.ExternalPath)
		}
		seen[r.ExternalPath] = true
	}
	split := 0
	for _, a := range fb.applied {
		if len(a) > 1 {
			t.Errorf("split deeper than needed: %v", a)
		}
		if _, ok := a["jobFamilyGroup"]; ok {
			split++
		}
	}
	if split == 0 || w.queries != 16 {
		t.Errorf("queries %d (split requests %d), want the board plus its 15 jobFamilyGroup buckets", w.queries, split)
	}
	// 1 + 75 (the 1500 bucket: 75 full pages, no page asked for past its
	// total) + 14 buckets of 89 or 90 rows at 5 pages each.
	if w.requests != 1+75+14*5 {
		t.Errorf("%d listing requests", w.requests)
	}
}

// Through List: a source with stored postings for every job returns all
// 2753, spending detail requests only on the day's re-reads; the old adapter
// failed this board on every run (ErrTooManyPostings at 200).
func TestNVIDIABoardThroughListWithStoredPostings(t *testing.T) {
	fb := nvidiaBoard(2753)
	var stored []posting.Posting
	for _, j := range fb.jobs {
		p, _ := escapePath(j.path)
		key, _ := posting.WorkdayKey("nvidia", "NVIDIAExternalCareerSite", fmt.Sprintf("JR%d", j.req))
		stored = append(stored, posting.Posting{Key: key, ATS: posting.Workday, Title: j.title, URL: "https://" + host + "/NVIDIAExternalCareerSite" + p})
	}
	var st Stats
	s, _ := fakeSource(fb, &Options{Stored: func(sources.Board) []posting.Posting { return stored }, Report: func(_ sources.Board, x Stats) { st = x }})
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2753 || st.Listed != 2753 {
		t.Fatalf("got %d postings, listed %d; want 2753", len(got), st.Listed)
	}
	if fb.gets != st.Refreshed || st.Refreshed == 0 || st.Refreshed > MaxDetails || st.Reused+st.Refreshed != 2753 {
		t.Errorf("stats %+v, %d GETs: want only the day's re-reads fetched", st, fb.gets)
	}
	for _, p := range got {
		if !p.SeenAt.Equal(fixedNow) {
			t.Fatalf("posting %s not seen now: %v", p.Key, p.SeenAt)
		}
	}
}

// First runs on the NVIDIA board: new rows are title-prefiltered, at most
// MaxDetails are fetched per run, the rest are deferred. Each run returns
// every stored posting (so the reconciler ages none of them) and, within a
// few runs, every job the title rule keeps is stored.
func TestNVIDIABoardFillsOverRunsWithinTheDetailCap(t *testing.T) {
	fb := nvidiaBoard(2753)
	ledger := map[posting.Key]posting.Posting{}
	keep := 0
	for _, j := range fb.jobs {
		if !strings.HasPrefix(j.title, "Senior") {
			keep++
		}
	}
	for run := 0; run < 10; run++ {
		var st Stats
		before := len(ledger)
		gets := fb.gets
		s, _ := fakeSource(fb, &Options{
			Stored: func(sources.Board) []posting.Posting {
				var out []posting.Posting
				for _, p := range ledger {
					out = append(out, p)
				}
				return out
			},
			DropListed: func(p posting.Posting) bool { return strings.HasPrefix(p.Title, "Senior") },
			Report:     func(_ sources.Board, x Stats) { st = x },
		})
		s.now = func() time.Time { return fixedNow.AddDate(0, 0, run) }
		got, err := s.List(context.Background(), board)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if n := fb.gets - gets; n > MaxDetails {
			t.Fatalf("run %d: %d detail requests, cap %d", run, n, MaxDetails)
		}
		if st.Listed != 2753 || st.Prefiltered != 2753-keep || st.Reused+st.Refreshed+st.Fetched+st.Deferred+st.Prefiltered+st.Gone != 2753 {
			t.Fatalf("run %d: stats %+v do not account for every row", run, st)
		}
		returned := map[posting.Key]bool{}
		for _, p := range got {
			returned[p.Key] = true
		}
		for k := range ledger {
			if !returned[k] {
				t.Fatalf("run %d: stored posting %s left out of the listing", run, k)
			}
		}
		for _, p := range got {
			ledger[p.Key] = p
		}
		if len(ledger) < keep && len(ledger)-before != MaxDetails-st.Refreshed {
			t.Fatalf("run %d: stored %d new postings, want the cap's worth", run, len(ledger)-before)
		}
	}
	if len(ledger) != keep {
		t.Fatalf("stored %d postings after 10 runs, want the %d the title rule keeps", len(ledger), keep)
	}
}

// A source without stored postings cannot defer, so the NVIDIA board fails
// before any detail request rather than listing a part of it.
func TestNVIDIABoardWithoutStoredPostingsFailsCleanly(t *testing.T) {
	fb := nvidiaBoard(2753)
	s, _ := fakeSource(fb, nil)
	if _, err := s.List(context.Background(), board); !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("err = %v", err)
	}
	if fb.gets != 0 {
		t.Errorf("%d detail requests", fb.gets)
	}
}

func TestNewWithNeedsStored(t *testing.T) {
	if _, err := NewWith(srctest.NewFunc(nil), Options{}); err == nil {
		t.Fatal("NewWith without Stored accepted")
	}
}

// Adversarial listings: each must fail the board, never return a part.
func TestListingThatCannotBeAccountedForFails(t *testing.T) {
	for name, tc := range map[string]struct {
		fb   func() *fakeBoard
		want error
	}{
		"wraps before its total": {fb: func() *fakeBoard {
			fb := nvidiaBoard(300)
			fb.wrapAt = 100
			return fb
		}, want: errIncomplete},
		"wraps on page 2": {fb: func() *fakeBoard {
			fb := nvidiaBoard(300)
			fb.wrapAt = 20
			return fb
		}, want: errIncomplete},
		"stuck page": {fb: func() *fakeBoard {
			fb := nvidiaBoard(300)
			var last []fakeJob
			fb.page = func(offset int, rows []fakeJob) []fakeJob {
				if offset == 60 {
					return last
				}
				last = rows
				return rows
			}
			return fb
		}, want: errIncomplete},
		"over the window, one facet counts it": {fb: func() *fakeBoard {
			fb := nvidiaBoard(2753)
			fb.params = []string{"jobFamilyGroup"}
			return fb
		}, want: errIncomplete},
		"over the window, facets disagree": {fb: func() *fakeBoard {
			fb := nvidiaBoard(2753)
			fb.params = []string{"timeType", "jobFamilyGroup"}
			fb.jobs[5].attrs["jobFamilyGroup"] = nil // one job in no family: the count rests on timeType alone
			return fb
		}, want: errIncomplete},
		"over the window, a third facet corroborates": {fb: func() *fakeBoard {
			fb := nvidiaBoard(2753)
			fb.jobs[5].attrs["timeType"] = nil // workerSubType and jobFamilyGroup still agree
			return fb
		}, want: nil},
		"over the window, no facets": {fb: func() *fakeBoard {
			fb := nvidiaBoard(2753)
			fb.params = nil
			return fb
		}, want: errIncomplete},
		"over the window, only location splits": {fb: func() *fakeBoard {
			fb := nvidiaBoard(2753)
			fb.params = []string{"timeType", "workerSubType", "locations"}
			// Every timeType and workerSubType bucket holds the board.
			for i := range fb.jobs {
				fb.jobs[i].attrs["timeType"] = []string{fid(0, 0)}
				fb.jobs[i].attrs["workerSubType"] = []string{fid(1, 0)}
			}
			return fb
		}, want: nil}, // location buckets each under the window: listed in full
		"total missing, over the window": {fb: func() *fakeBoard {
			fb := nvidiaBoard(2100)
			fb.params = nil
			fb.total = func(int, map[string][]string) (string, bool) { return "", false }
			return fb
		}, want: errIncomplete},
		"total lies high": {fb: func() *fakeBoard {
			fb := nvidiaBoard(300)
			fb.total = func(real int, _ map[string][]string) (string, bool) { return fmt.Sprint(real + 50), true }
			return fb
		}, want: nil}, // kept: see TestEmptyPageStopsLoopWhenTotalOverstates
		"total negative": {fb: func() *fakeBoard {
			fb := nvidiaBoard(300)
			fb.total = func(int, map[string][]string) (string, bool) { return "-5", true }
			return fb
		}, want: nil},
		"total a string": {fb: func() *fakeBoard {
			fb := nvidiaBoard(300)
			fb.total = func(int, map[string][]string) (string, bool) { return `"300"`, true }
			return fb
		}, want: nil},
		"total lies low": {fb: func() *fakeBoard {
			fb := nvidiaBoard(300)
			fb.total = func(int, map[string][]string) (string, bool) { return "7", true }
			return fb
		}, want: nil},
		"a bucket's total overstates": {fb: func() *fakeBoard {
			fb := nvidiaBoard(2753)
			fb.total = func(real int, applied map[string][]string) (string, bool) {
				if ids, ok := applied["jobFamilyGroup"]; ok && ids[0] == fid(2, 3) {
					return fmt.Sprint(real + 40), true // claims rows that do not exist
				}
				return fmt.Sprint(min(real, window)), true
			}
			return fb
		}, want: nil},
		"a bucket loses rows the facets count": {fb: func() *fakeBoard {
			fb := nvidiaBoard(2753)
			fb.page = func(offset int, rows []fakeJob) []fakeJob {
				// The big bucket (1500) answers an empty page from offset 200 on.
				if offset >= 200 {
					if ids, ok := fb.applied[len(fb.applied)-1]["jobFamilyGroup"]; ok && ids[0] == fid(2, 0) {
						return nil
					}
				}
				return rows
			}
			return fb
		}, want: errIncomplete},
	} {
		t.Run(name, func(t *testing.T) {
			fb := tc.fb()
			rows, _, err := listAll(t, fb, board)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				if len(rows) != len(fb.jobs) {
					t.Fatalf("listed %d of %d", len(rows), len(fb.jobs))
				}
				return
			}
			if !errors.Is(err, tc.want) || rows != nil {
				t.Fatalf("err = %v with %d rows; want %v", err, len(rows), tc.want)
			}
		})
	}
}

// A board its facets count over its limit fails on the first page, before
// any split is walked (rows past a limit the count did not show are refused
// as they are listed: TestLimitedListingIsBounded).
func TestBoardCountedOverItsLimitFailsAtOnce(t *testing.T) {
	fb := nvidiaBoard(MaxPostings + 500)
	for i := range fb.jobs {
		fb.jobs[i].attrs["jobFamilyGroup"] = []string{fid(2, i%10)}
	}
	_, w, err := listAll(t, fb, board)
	if !errors.Is(err, srcutil.ErrTooManyPostings) || !strings.Contains(err.Error(), "max_postings") {
		t.Fatalf("err = %v", err)
	}
	if w.requests != 1 {
		t.Errorf("%d requests, want the first page only", w.requests)
	}
	// The board's max_postings lets the same board through: every row listed
	// once, within the request bound.
	fb = nvidiaBoard(MaxPostings + 500)
	for i := range fb.jobs {
		fb.jobs[i].attrs["jobFamilyGroup"] = []string{fid(2, i%10)}
	}
	s, _ := fakeSource(fb, nil)
	w = newWalker(s, jobsURL, MaxPostings+500)
	if _, err := w.walk(context.Background(), map[string][]string{}, 0); err != nil || len(w.rows) != MaxPostings+500 {
		t.Fatalf("raised limit: %d rows, %v", len(w.rows), err)
	}
	if w.requests > listRequests(w.limit) {
		t.Errorf("%d requests, bound %d", w.requests, listRequests(w.limit))
	}
}

// The board's limit is its max_postings within 1..MaxPostingsCeiling, or
// MaxPostings; the ceiling lists a whole split NVIDIA-sized board many times
// over, within a bounded number of requests.
func TestCapFor(t *testing.T) {
	for n, want := range map[int]int{0: MaxPostings, 1: 1, 3000: 3000, sources.MaxPostingsCeiling: sources.MaxPostingsCeiling} {
		b := board
		b.MaxPostings = n
		if got, err := capFor(b); err != nil || got != want {
			t.Errorf("capFor(%d) = %d, %v; want %d", n, got, err, want)
		}
	}
	for _, n := range []int{-1, sources.MaxPostingsCeiling + 1} {
		b := board
		b.MaxPostings = n
		if _, err := capFor(b); err == nil {
			t.Errorf("capFor(%d) accepted", n)
		}
	}
	if MaxPostings < 2753 || sources.MaxPostingsCeiling < MaxPostings || listRequests(sources.MaxPostingsCeiling) > 1100 {
		t.Errorf("bounds: default %d, ceiling %d, %d requests at the ceiling", MaxPostings, sources.MaxPostingsCeiling, listRequests(sources.MaxPostingsCeiling))
	}
}

// A cancelled context stops the walk inside a split.
func TestCancelMidSplit(t *testing.T) {
	fb := nvidiaBoard(2753)
	ctx, cancel := context.WithCancel(context.Background())
	fb.page = func(offset int, rows []fakeJob) []fakeJob {
		if len(fb.applied) == 5 {
			cancel()
		}
		return rows
	}
	s, _ := fakeSource(fb, nil)
	if _, err := s.List(ctx, board); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if fb.posts > 6 {
		t.Errorf("%d listing requests after cancel", fb.posts)
	}
}

// A 429 inside a bucket is retried, and the board still lists in full.
func TestRateLimitInsideBucketIsRetried(t *testing.T) {
	fb := nvidiaBoard(2753)
	limited := false
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if !limited && strings.Contains(string(r.Body), "jobFamilyGroup") {
			limited = true
			return srctest.Reply{Status: 429, Header: http.Header{"Retry-After": {"0"}}}, true
		}
		return fb.handle(r)
	})
	s := &source{d: d, now: func() time.Time { return fixedNow }}
	w := newWalker(s, jobsURL, MaxPostings)
	if _, err := w.walk(context.Background(), map[string][]string{}, 0); err != nil || len(w.rows) != 2753 || !limited {
		t.Fatalf("err %v, %d rows, limited %v", err, len(w.rows), limited)
	}
}

// Search terms are each walked (split where needed) and their union listed
// once; searchText is kept inside every bucket.
func TestSearchTermsSplitAndUnion(t *testing.T) {
	fb := nvidiaBoard(2753)
	b := board
	b.Searches = []string{"senior", "engineer 1"}
	rows, _, err := listAll(t, fb, b)
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, j := range fb.jobs {
		if strings.Contains(strings.ToLower(j.title), "senior") || strings.Contains(strings.ToLower(j.title), "engineer 1") {
			want++
		}
	}
	if len(rows) != want {
		t.Fatalf("listed %d, want %d", len(rows), want)
	}
}

func TestReadFacets(t *testing.T) {
	id := func(n int) string { return fmt.Sprintf("%032x", n) }
	many := make([]string, maxFacetValues+1)
	for i := range many {
		many[i] = fmt.Sprintf(`{"id":%q,"count":1}`, id(i))
	}
	raw := `[
	 {"facetParameter":"timeType","values":[{"id":"` + id(1) + `","count":3},{"id":"` + strings.ToUpper(id(0xabc2)) + `","count":4},{"id":"` + id(1) + `","count":3}]},
	 {"facetParameter":"badId","values":[{"id":"x","count":3},{"id":"` + id(3) + `","count":1}]},
	 {"facetParameter":"badCount","values":[{"id":"` + id(4) + `","count":-1},{"id":"` + id(5) + `","count":1.5},{"id":"` + id(6) + `","count":2}]},
	 {"facetParameter":"noCount","values":[{"id":"` + id(7) + `"},{"id":"` + id(8) + `","count":2}]},
	 {"facetParameter":"locationMainGroup","values":[{"facetParameter":"locations","values":[{"id":"` + id(9) + `","count":5}]}]},
	 {"facetParameter":"bad name!","values":[{"id":"` + id(10) + `","count":5}]},
	 {"facetParameter":"many","values":[` + strings.Join(many, ",") + `]},
	 "not an object", 7, null
	]`
	got := map[string]facetChoice{}
	for _, f := range readFacets(json.RawMessage(raw)) {
		got[f.param] = f
	}
	if f := got["timeType"]; f.partial || len(f.values) != 2 || f.values[1].id != id(0xabc2) || f.sum() != 7 {
		t.Errorf("timeType %+v: want two values (a repeat dropped, id lowercased)", f)
	}
	for _, p := range []string{"badId", "badCount", "noCount", "many"} {
		if !got[p].partial {
			t.Errorf("%s not partial: %+v", p, got[p])
		}
	}
	if f, ok := got["locations"]; !ok || f.sum() != 5 || f.partial {
		t.Errorf("nested locations %+v", f)
	}
	if _, ok := got["bad name!"]; ok {
		t.Error("a facet whose name cannot be sent back was read")
	}
	for _, bad := range []string{``, `{}`, `null`, `"x"`, `[[[[[[]]]]]]`} {
		if f := readFacets(json.RawMessage(bad)); len(f) != 0 {
			t.Errorf("readFacets(%s) = %+v", bad, f)
		}
	}
}

func TestExpectedTotalAndChooseSplit(t *testing.T) {
	v := func(counts ...int) []facetValue {
		var out []facetValue
		for i, c := range counts {
			out = append(out, facetValue{id: fid(9, i), count: c})
		}
		return out
	}
	n := func(x int) *int { return &x }
	tt := facetChoice{param: "timeType", values: v(2700, 53)}
	ws := facetChoice{param: "workerSubType", values: v(2650, 103)}
	fam := facetChoice{param: "jobFamilyGroup", values: v(1500, 600, 653)}
	loc := facetChoice{param: "locations", values: v(1900, 1900)}
	part := facetChoice{param: "skills", values: v(1000, 1000, 1000), partial: true}
	none := map[string][]string{}

	if c, ok := expectedTotal(n(150), []facetChoice{tt}, none); c != 150 || !ok {
		t.Errorf("under the window: %d %v", c, ok)
	}
	if c, ok := expectedTotal(n(2000), []facetChoice{tt, ws, loc, part}, none); c != 2753 || !ok {
		t.Errorf("capped total: %d %v, want 2753 corroborated (location and partial left out)", c, ok)
	}
	if c, ok := expectedTotal(n(2000), []facetChoice{tt, loc}, none); c != 2753 || ok {
		t.Errorf("one facet: %d %v, want uncorroborated", c, ok)
	}
	if c, ok := expectedTotal(nil, nil, none); c != 0 || ok {
		t.Errorf("nothing: %d %v", c, ok)
	}
	// Inside a timeType bucket, timeType's own counts span the board.
	applied := map[string][]string{"timeType": {fid(9, 0)}}
	if c, _ := expectedTotal(n(1800), []facetChoice{tt, ws}, applied); c != 1800 {
		t.Errorf("applied facet sized the query: %d", c)
	}
	if c, _ := expectedTotal(n(2000), []facetChoice{{param: "timeType", values: v(5000)}, ws}, applied); c != 2753 {
		t.Errorf("applied facet counted: %d", c)
	}

	if f, ok := chooseSplit([]facetChoice{tt, ws, fam, loc, part}, none); !ok || f.param != "jobFamilyGroup" {
		t.Errorf("split by %q, want jobFamilyGroup (no bucket over the window)", f.param)
	}
	if f, ok := chooseSplit([]facetChoice{loc, tt}, none); !ok || f.param != "timeType" {
		t.Errorf("split by %q, want timeType before location", f.param)
	}
	if f, ok := chooseSplit([]facetChoice{fam, tt}, map[string][]string{"jobFamilyGroup": {fid(9, 0)}}); !ok || f.param != "timeType" {
		t.Errorf("split by %q, want the facet not applied", f.param)
	}
	short := facetChoice{param: "short", values: v(1000, 900)} // lacks jobs
	if f, ok := chooseSplit([]facetChoice{short, tt}, none); !ok || f.param != "timeType" {
		t.Errorf("split by %q, want the facet that covers every job", f.param)
	}
	for name, fs := range map[string][]facetChoice{
		"only partial":   {part},
		"one bucket":     {{param: "x", values: v(2753, 0)}},
		"nothing at all": nil,
	} {
		if f, ok := chooseSplit(fs, none); ok {
			t.Errorf("%s: split by %q", name, f.param)
		}
	}
}

func TestReadTotal(t *testing.T) {
	for raw, want := range map[string]int{`0`: 0, `2000`: 2000, `3.0`: 3} {
		if got := readTotal(json.RawMessage(raw)); got == nil || *got != want {
			t.Errorf("readTotal(%s) = %v", raw, got)
		}
	}
	for _, raw := range []string{``, `null`, `-1`, `2.5`, `"5"`, `{}`, `1e300`} {
		if got := readTotal(json.RawMessage(raw)); got != nil {
			t.Errorf("readTotal(%s) = %d, want none", raw, *got)
		}
	}
}

func TestStoredPath(t *testing.T) {
	const site = "NVIDIAExternalCareerSite"
	for raw, want := range map[string]string{
		"https://" + host + "/" + site + "/job/X/T_JR1":                     "/job/X/T_JR1",
		"https://" + host + "/en-US/" + site + "/job/X/T_JR1":               "/job/X/T_JR1",
		"https://NVIDIA.wd5.myworkdayjobs.com/" + site + "/job/a%20b/c%3Fd": "/job/a b/c?d",
		"https://" + host + "/nvidiaexternalcareersite/job/X/T_JR1":         "/job/X/T_JR1",
	} {
		if got, ok := storedPath(raw, host, site); !ok || got != want {
			t.Errorf("storedPath(%q) = %q, %v; want %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{
		"https://evil.example/" + site + "/job/X",
		"https://" + host + "/OtherSite/job/X",
		"https://" + host + "/" + site,
		"https://" + host + "/en-US/fr/" + site + "/job/X",
		"::not a url",
		"",
	} {
		if got, ok := storedPath(raw, host, site); ok {
			t.Errorf("storedPath(%q) = %q, want no match", raw, got)
		}
	}
	// What detail builds is what storedPath reads back.
	for _, p := range []string{"/job/X/T_JR1", "/job/a b/c?d_JR1", "/job/ü/x"} {
		esc, _ := escapePath(p)
		if got, ok := storedPath("https://"+host+"/"+site+esc, host, site); !ok || got != p {
			t.Errorf("round trip %q: %q %v", p, got, ok)
		}
	}
}

// Every stored posting's turn to be re-read comes exactly once in
// refreshDays days.
func TestRefreshTurnCoversEveryPathOnce(t *testing.T) {
	for i := range 500 {
		p := fmt.Sprintf("/job/X/T_JR%d", i)
		turns := 0
		for day := 1000; day < 1000+refreshDays; day++ {
			if refreshTurn(p, day) {
				turns++
			}
		}
		if turns != 1 {
			t.Fatalf("%s re-read %d times in %d days", p, turns, refreshDays)
		}
	}
}

// Deferring is safe only when every stored posting is matched to its listed
// row. Stored postings whose URLs do not match (another site segment, or a
// path the listing no longer uses) would look new, and deferring the rest
// would age them as absent: such a board fails instead. With nothing
// deferred, every row is returned anyway, so the same mismatch is harmless.
func TestDeferralRefusedWhenStoredPostingsDoNotMatch(t *testing.T) {
	storedAs := func(fb *fakeBoard, n int, url func(j fakeJob, esc string) string) []posting.Posting {
		var out []posting.Posting
		for _, j := range fb.jobs[:n] {
			esc, _ := escapePath(j.path)
			key, _ := posting.WorkdayKey("nvidia", "NVIDIAExternalCareerSite", fmt.Sprintf("JR%d", j.req))
			out = append(out, posting.Posting{Key: key, ATS: posting.Workday, Title: j.title, URL: url(j, esc)})
		}
		return out
	}
	for name, url := range map[string]func(fakeJob, string) string{
		"another site segment": func(_ fakeJob, esc string) string { return "https://" + host + "/External" + esc },
		"a path the listing no longer uses": func(j fakeJob, _ string) string {
			return fmt.Sprintf("https://%s/NVIDIAExternalCareerSite/job/Old-Slug_JR%d", host, j.req)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fb := nvidiaBoard(400 + MaxDetails)
			fb.params = nil
			stored := storedAs(fb, 400, url)
			s, _ := fakeSource(fb, &Options{Stored: func(sources.Board) []posting.Posting { return stored }})
			got, err := s.List(context.Background(), board)
			if err == nil || got != nil {
				t.Fatalf("deferred past unmatched stored postings: %d postings, %v", len(got), err)
			}
			if fb.gets > MaxDetails {
				t.Errorf("%d detail requests", fb.gets)
			}

			// Few enough rows to fetch them all: nothing is deferred, so the
			// stored postings come back fetched, none left out.
			fb = nvidiaBoard(250)
			fb.params = nil
			stored = storedAs(fb, 200, url)
			s, _ = fakeSource(fb, &Options{Stored: func(sources.Board) []posting.Posting { return stored }})
			got, err = s.List(context.Background(), board)
			if err != nil || len(got) != 250 {
				t.Fatalf("no deferral: %d postings, %v", len(got), err)
			}
		})
	}
}

// Re-reads that do not fit the cap beside new rows are reused as stored; a
// stored posting another board owns is never matched; one new posting gone
// among reused ones is that posting closing, not a dead endpoint.
func TestDetailBudgetAndGone(t *testing.T) {
	fb := nvidiaBoard(400)
	fb.params = nil
	var stored []posting.Posting
	for _, j := range fb.jobs[:350] {
		p, _ := escapePath(j.path)
		key, _ := posting.WorkdayKey("nvidia", "NVIDIAExternalCareerSite", fmt.Sprintf("JR%d", j.req))
		stored = append(stored, posting.Posting{Key: key, ATS: posting.Workday, Title: j.title, URL: "https://" + host + "/NVIDIAExternalCareerSite" + p})
	}
	other := stored[0]
	other.Key = "wd:other:site:jr1"
	stored = append(stored, other)
	// Job 399 is listed but its detail is gone.
	gone := fb.jobs[399]
	fb.jobs[399].path += "-gone-on-detail"
	listedRow := gone
	fb.page = func(offset int, rows []fakeJob) []fakeJob {
		for i := range rows {
			if rows[i].req == listedRow.req {
				rows[i] = listedRow
			}
		}
		return rows
	}
	var st Stats
	s, _ := fakeSource(fb, &Options{Stored: func(sources.Board) []posting.Posting { return stored }, Report: func(_ sources.Board, x Stats) { st = x }})
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if st.Fetched != 49 || st.Gone != 1 || st.Reused+st.Refreshed != 350 || fb.gets > MaxDetails {
		t.Fatalf("stats %+v, %d GETs", st, fb.gets)
	}
	if len(got) != 399 {
		t.Fatalf("got %d postings, want 399", len(got))
	}
	for _, p := range got {
		if p.Key == other.Key {
			t.Fatal("another board's stored posting was returned")
		}
	}

	// New rows fill the cap: no re-read fits, so every stored row is reused.
	fb = nvidiaBoard(400 + MaxDetails)
	fb.params = nil
	stored = nil
	for _, j := range fb.jobs[:400] {
		p, _ := escapePath(j.path)
		key, _ := posting.WorkdayKey("nvidia", "NVIDIAExternalCareerSite", fmt.Sprintf("JR%d", j.req))
		stored = append(stored, posting.Posting{Key: key, ATS: posting.Workday, Title: j.title, URL: "https://" + host + "/en-US/NVIDIAExternalCareerSite" + p})
	}
	s, _ = fakeSource(fb, &Options{Stored: func(sources.Board) []posting.Posting { return stored }, Report: func(_ sources.Board, x Stats) { st = x }})
	got, err = s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if st.Fetched != MaxDetails || st.Refreshed != 0 || st.Reused != 400 || st.Deferred != 0 || fb.gets != MaxDetails || len(got) != 400+MaxDetails {
		t.Fatalf("full cap: stats %+v, %d GETs, %d postings", st, fb.gets, len(got))
	}
}
