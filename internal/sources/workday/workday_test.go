package workday

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

var fixedNow = time.Date(2026, 10, 18, 9, 30, 0, 0, time.UTC)

const (
	host    = "nvidia.wd5.myworkdayjobs.com"
	jobsURL = "https://" + host + "/wday/cxs/nvidia/NVIDIAExternalCareerSite/jobs"
	siteURL = "https://" + host + "/wday/cxs/nvidia/NVIDIAExternalCareerSite"
)

var board = sources.Board{ATS: posting.Workday, Token: "nvidia/NVIDIAExternalCareerSite", Company: "NVIDIA", Host: host}

func read(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func ok(body string) srctest.Reply { return srctest.Reply{Status: 200, Body: []byte(body)} }

func newSource(h func(srctest.Request) (srctest.Reply, bool)) (*source, *srctest.Doer) {
	d := srctest.NewFunc(h)
	return &source{d: d, now: func() time.Time { return fixedNow }}, d
}

// fixtureHandler serves the fixture listing and the two fixture details.
func fixtureHandler(t *testing.T) func(srctest.Request) (srctest.Reply, bool) {
	list, d1, d2 := read(t, "list.json"), read(t, "detail1.json"), read(t, "detail2.json")
	return func(r srctest.Request) (srctest.Reply, bool) {
		switch {
		case r.Method == "POST" && r.URL == jobsURL:
			return ok(list), true
		case r.Method == "GET" && r.URL == siteURL+"/job/US-CA-Santa-Clara/NVIDIA-2027-Ignite-Internships--Hardware-Engineering_JR2027047":
			return ok(d1), true
		case r.Method == "GET" && r.URL == siteURL+"/job/India-Bengaluru/CAD-Engineer_JR2014480-1":
			return ok(d2), true
		}
		return srctest.Reply{}, false
	}
}

func TestFixture(t *testing.T) {
	s, d := newSource(fixtureHandler(t))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d postings, want 2", len(got))
	}
	p := got[0]
	if p.Key != "wd:nvidia:nvidiaexternalcareersite:jr2027047" || p.ATS != posting.Workday || p.Company != "NVIDIA" ||
		p.Title != "NVIDIA 2027 Ignite Internships: Hardware Engineering" || p.Location != "US, CA, Santa Clara" ||
		p.URL != "https://nvidia.wd5.myworkdayjobs.com/NVIDIAExternalCareerSite/job/US-CA-Santa-Clara/NVIDIA-2027-Ignite-Internships--Hardware-Engineering_JR2027047" ||
		!p.PostedAt.Equal(time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)) || !p.SeenAt.Equal(fixedNow) || p.Remote {
		t.Errorf("posting 0: %+v", p)
	}
	if got[1].Key != "wd:nvidia:nvidiaexternalcareersite:jr2014480" || got[1].Title != "CAD Engineer" {
		t.Errorf("posting 1: %+v", got[1])
	}
	if strings.Contains(p.Description, "<") || !strings.Contains(p.Description, "Ignite Internships") {
		t.Errorf("description not reduced to text: %q", p.Description)
	}
	for _, g := range got {
		if !strings.HasPrefix(string(g.Key), board.KeyPrefix()) {
			t.Errorf("key %q not under prefix %q", g.Key, board.KeyPrefix())
		}
	}
	reqs := d.Requests()
	if len(reqs) != 3 || reqs[0].Method != "POST" || reqs[1].Method != "GET" || reqs[2].Method != "GET" {
		t.Fatalf("requests: %+v", reqs)
	}
	if reqs[0].Header.Get("Accept") != "application/json" || reqs[0].Header.Get("Accept-Language") != "en-US" ||
		reqs[0].Header.Get("Content-Type") != "application/json" || reqs[1].Header.Get("Accept") != "application/json" {
		t.Errorf("headers: %v / %v", reqs[0].Header, reqs[1].Header)
	}
}

func TestRequestBodyExact(t *testing.T) {
	s, d := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		return ok(`{"total":0,"jobPostings":[]}`), r.URL == jobsURL
	})
	b := board
	b.Search = `golang "senior"`
	got, err := s.List(context.Background(), b)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
	want := `{"appliedFacets":{},"limit":20,"offset":0,"searchText":"golang \"senior\""}`
	if reqs := d.Requests(); len(reqs) != 1 || string(reqs[0].Body) != want {
		t.Errorf("body %q, want %q", reqs[0].Body, want)
	}
}

// pagedHandler serves total rows over pages of 20, reporting total only on
// offset 0 like the live service. It records the offsets asked for.
func pagedHandler(total int, offsets *[]int, reportTotal func(offset int) int, detail string) func(srctest.Request) (srctest.Reply, bool) {
	return func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method == "POST" && r.URL == jobsURL {
			var req struct{ Limit, Offset int }
			if err := json.Unmarshal(r.Body, &req); err != nil || req.Limit != 20 {
				return srctest.Reply{}, false
			}
			*offsets = append(*offsets, req.Offset)
			var rows []string
			for i := req.Offset; i < total && i < req.Offset+20; i++ {
				rows = append(rows, fmt.Sprintf(`{"title":"T%d","externalPath":"/job/X/T%d_JR%d","locationsText":"L","postedOn":"Posted 3 Days Ago"}`, i, i, i))
			}
			return ok(fmt.Sprintf(`{"total":%d,"jobPostings":[%s]}`, reportTotal(req.Offset), strings.Join(rows, ","))), true
		}
		if r.Method == "GET" && strings.HasPrefix(r.URL, siteURL+"/job/X/T") {
			id := r.URL[strings.LastIndex(r.URL, "_JR")+3:]
			return ok(fmt.Sprintf(detail, id, id)), true
		}
		return srctest.Reply{}, false
	}
}

const detailTmpl = `{"jobPostingInfo":{"jobReqId":"JR%s","title":"T%s","location":"Loc","jobDescription":"<p>d</p>"}}`

func TestPagingThreePages(t *testing.T) {
	var offsets []int
	s, _ := newSource(pagedHandler(50, &offsets, func(o int) int {
		if o == 0 {
			return 50
		}
		return 0 // the live service reports 0 on later pages
	}, detailTmpl))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 50 {
		t.Fatalf("got %d postings, want 50", len(got))
	}
	if fmt.Sprint(offsets) != "[0 20 40]" {
		t.Errorf("offsets %v, want [0 20 40]", offsets)
	}
	// 3 days before the fixed now, no startDate in the detail.
	if want := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC); !got[0].PostedAt.Equal(want) {
		t.Errorf("PostedAt %v, want %v", got[0].PostedAt, want)
	}
	// No detail externalUrl: the public URL is built from host, site and path.
	if want := "https://" + host + "/NVIDIAExternalCareerSite/job/X/T0_JR0"; got[0].URL != want {
		t.Errorf("URL %q, want %q", got[0].URL, want)
	}
}

func TestEmptyPageStopsLoopWhenTotalIsZero(t *testing.T) {
	var offsets []int
	// Saturated/zero total on every page: only the empty page ends the loop.
	s, _ := newSource(pagedHandler(45, &offsets, func(int) int { return 0 }, detailTmpl))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 45 || fmt.Sprint(offsets) != "[0 20 40 45]" {
		t.Errorf("got %d postings, offsets %v", len(got), offsets)
	}
}

func TestEmptyPageStopsLoopWhenTotalOverstates(t *testing.T) {
	var offsets []int
	s, _ := newSource(pagedHandler(20, &offsets, func(int) int { return 100 }, detailTmpl))
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 20 || fmt.Sprint(offsets) != "[0 20]" {
		t.Fatalf("got %d, %v, offsets %v", len(got), err, offsets)
	}
}

// A reply without a jobPostings array is not an empty board: the live service
// answers a search with no match with "jobPostings":[], so a missing or null
// array is an error page or a changed shape, and reading it as empty would age
// every posting on the board as absent.
func TestMissingJobPostingsIsError(t *testing.T) {
	for name, body := range map[string]string{
		"missing":    `{"total":0}`,
		"null":       `{"total":0,"jobPostings":null}`,
		"error page": `{"errorCode":"S22","message":"Service unavailable"}`,
		"empty":      `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
				if r.Method == "POST" && r.URL == jobsURL {
					return ok(body), true
				}
				return srctest.Reply{}, false
			})
			if got, err := s.List(context.Background(), board); err == nil {
				t.Fatalf("read %s as a board of %d postings", body, len(got))
			}
		})
	}
}

// A later page that loses the array fails the board too; a partial listing
// would age the postings it did not reach.
func TestMissingJobPostingsOnLaterPageIsError(t *testing.T) {
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method == "POST" && r.URL == jobsURL {
			var req struct{ Offset int }
			json.Unmarshal(r.Body, &req)
			if req.Offset == 0 {
				var rows []string
				for i := range 20 {
					rows = append(rows, fmt.Sprintf(`{"title":"T%d","externalPath":"/job/X/T%d_JR%d","postedOn":"Posted Today"}`, i, i, i))
				}
				return ok(`{"total":40,"jobPostings":[` + strings.Join(rows, ",") + `]}`), true
			}
			return ok(`{"total":0}`), true
		}
		if r.Method == "GET" && strings.HasPrefix(r.URL, siteURL+"/job/X/T") {
			id := r.URL[strings.LastIndex(r.URL, "_JR")+3:]
			return ok(fmt.Sprintf(detailTmpl, id, id)), true
		}
		return srctest.Reply{}, false
	})
	if got, err := s.List(context.Background(), board); err == nil {
		t.Fatalf("a listing cut short by a malformed page returned %d postings", len(got))
	}
}

// A search that matches nothing is a genuine empty board, not an error.
func TestEmptyJobPostingsIsEmptyBoard(t *testing.T) {
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method == "POST" && r.URL == jobsURL {
			return ok(`{"total":0,"jobPostings":[],"facets":[]}`), true
		}
		return srctest.Reply{}, false
	})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty board: %d postings, %v", len(got), err)
	}
}

func TestTotalOverCapNamesSearch(t *testing.T) {
	var offsets []int
	s, d := newSource(pagedHandler(500, &offsets, func(int) int { return 201 }, detailTmpl))
	_, err := s.List(context.Background(), board)
	if err == nil || !strings.Contains(err.Error(), "search") || !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("err = %v, want too-many mentioning search", err)
	}
	if len(d.Requests()) != 1 {
		t.Errorf("made %d requests, want only the first listing page", len(d.Requests()))
	}
}

func TestSaturatedListingIsBoundedAndErrors(t *testing.T) {
	// Total claims 0 and the service never runs dry: the row cap ends it.
	var offsets []int
	s, _ := newSource(pagedHandler(100000, &offsets, func(int) int { return 0 }, detailTmpl))
	_, err := s.List(context.Background(), board)
	if err == nil || !strings.Contains(err.Error(), "search") {
		t.Fatalf("err = %v, want over-cap error mentioning search", err)
	}
	if len(offsets) > maxPages {
		t.Errorf("%d pages requested, bound is %d", len(offsets), maxPages)
	}
}

func TestRepeatedRowAcrossPagesIsOnePosting(t *testing.T) {
	row := `{"title":"A","externalPath":"/job/X/T1_JR1","locationsText":"L","postedOn":"Posted Today"}`
	calls := 0
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		if r.URL == jobsURL {
			calls++
			if calls == 1 {
				return ok(`{"total":2,"jobPostings":[` + row + `]}`), true
			}
			return ok(`{"total":0,"jobPostings":[` + row + `]}`), true
		}
		return ok(fmt.Sprintf(detailTmpl, "1", "1")), strings.HasSuffix(r.URL, "/job/X/T1_JR1")
	})
	// First page has 1 row of total 2; second repeats it; offset reaches 2.
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d, %v", len(got), err)
	}
}

func TestDetailFailureFailsBoard(t *testing.T) {
	h := fixtureHandler(t)
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		if strings.HasSuffix(r.URL, "CAD-Engineer_JR2014480-1") {
			return srctest.Reply{Status: 500}, true
		}
		return h(r)
	})
	got, err := s.List(context.Background(), board)
	if err == nil || got != nil {
		t.Fatalf("got %v, %v; want an error and no postings", got, err)
	}
	if errors.Is(err, ErrBoardNotFound) {
		t.Errorf("a failed detail must not read as a missing board: %v", err)
	}
}

// A posting that closes between the listing and its detail answers 404. It
// used to fail the whole board, so one closed posting hid every open one for
// the run; now it is left out, up to maxGone.
func TestGoneDetailIsLeftOut(t *testing.T) {
	h := fixtureHandler(t)
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		if strings.HasSuffix(r.URL, "CAD-Engineer_JR2014480-1") {
			return srctest.Reply{Status: 404}, true
		}
		return h(r)
	})
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].URL, "JR2027047") {
		t.Fatalf("got %+v, want only the posting whose detail answered", got)
	}
}

// Many gone details at once is the detail endpoint failing, not postings
// closing: leaving them all out would empty a healthy board.
func TestGoneDetailsAreBounded(t *testing.T) {
	for _, tc := range []struct {
		total, gone int
		wantErr     bool
	}{
		{50, 5, false}, // a tenth of the board
		{50, 6, true},
		{50, 50, true},
		{1, 1, true}, // the only posting gone is indistinguishable from a dead endpoint
		{5, 1, false},
		{5, 2, true},
	} {
		t.Run(fmt.Sprintf("%d_of_%d", tc.gone, tc.total), func(t *testing.T) {
			var offsets []int
			h := pagedHandler(tc.total, &offsets, func(int) int { return tc.total }, detailTmpl)
			s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
				if r.Method == "GET" {
					var i int
					if _, err := fmt.Sscanf(r.URL[strings.LastIndex(r.URL, "_JR")+3:], "%d", &i); err == nil && i < tc.gone {
						return srctest.Reply{Status: 404}, true
					}
				}
				return h(r)
			})
			got, err := s.List(context.Background(), board)
			if tc.wantErr {
				if err == nil || got != nil {
					t.Fatalf("got %d postings, %v; want an error", len(got), err)
				}
				if errors.Is(err, ErrBoardNotFound) {
					t.Errorf("gone details must not read as a missing board: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.total-tc.gone {
				t.Errorf("got %d postings, want %d", len(got), tc.total-tc.gone)
			}
		})
	}
}

func TestListing404IsBoardNotFound(t *testing.T) {
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) { return srctest.Reply{Status: 404}, true })
	_, err := s.List(context.Background(), board)
	if !errors.Is(err, ErrBoardNotFound) || !errors.Is(err, srcutil.ErrBoardNotFound) {
		t.Fatalf("err = %v, want ErrBoardNotFound", err)
	}
}

func TestListing500IsStatusError(t *testing.T) {
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) { return srctest.Reply{Status: 500}, true })
	_, err := s.List(context.Background(), board)
	var se *egress.StatusError
	if !errors.As(err, &se) || se.Code != 500 {
		t.Fatalf("err = %v, want StatusError 500", err)
	}
}

// A board that rate-limits part-way through (Salesforce's answered 429 on a
// live run) is retried rather than failed: the listing POST and the detail
// GETs both go through srcutil's retry.
func TestRateLimitIsRetried(t *testing.T) {
	inner := fixtureHandler(t)
	limited := map[string]bool{}
	s, d := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		k := r.Method + " " + r.URL
		if !limited[k] {
			limited[k] = true
			return srctest.Reply{Status: 429, Header: http.Header{"Retry-After": {"0"}}}, true
		}
		return inner(r)
	})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d postings, err %v; want both after one 429 each", len(got), err)
	}
	if n := len(d.Requests()); n != 6 { // 3 distinct requests, each limited once
		t.Errorf("%d requests, want 6", n)
	}
}

// Several search terms list their union: a posting both terms find is fetched
// and reported once, and each term is its own listing request.
func TestSearchesListTheUnion(t *testing.T) {
	inner := fixtureHandler(t)
	var terms []string
	s, d := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method == "POST" {
			var req listRequest
			if err := json.Unmarshal(r.Body, &req); err != nil {
				t.Errorf("listing body: %v", err)
			}
			terms = append(terms, req.SearchText)
		}
		return inner(r)
	})
	b := board
	b.Search, b.Searches = "TensorRT", []string{"new college grad", "tensorrt", " "}
	got, err := s.List(context.Background(), b)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d postings, err %v; want the fixture's 2 once each", len(got), err)
	}
	if strings.Join(terms, "|") != "TensorRT|new college grad" {
		t.Errorf("listing terms %q, want each distinct term once", terms)
	}
	gets := 0
	for _, r := range d.Requests() {
		if r.Method == "GET" {
			gets++
		}
	}
	if gets != 2 {
		t.Errorf("%d detail requests, want 2 (a posting both terms list is fetched once)", gets)
	}
}

// Two terms each under the cap whose union is over it fail the board before
// any detail request is spent, and filters are refused.
func TestSearchesUnionCap(t *testing.T) {
	page := func(prefix string, n, offset int) string {
		var resp listResponse
		resp.Total = n
		for i := offset; i < n && i < offset+pageSize; i++ {
			resp.JobPostings = append(resp.JobPostings, listedJob{Title: "t", ExternalPath: fmt.Sprintf("/job/%s_JR%d", prefix, i)})
		}
		raw, _ := json.Marshal(resp)
		return string(raw)
	}
	s, d := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		var req listRequest
		_ = json.Unmarshal(r.Body, &req)
		if r.Method != "POST" {
			return srctest.Reply{}, false
		}
		return ok(page(req.SearchText, 150, req.Offset)), true
	})
	b := board
	b.Searches = []string{"a", "b"}
	if _, err := s.List(context.Background(), b); !errors.Is(err, srcutil.ErrTooManyPostings) || !strings.Contains(err.Error(), "searches together") {
		t.Fatalf("err = %v, want the union over the cap", err)
	}
	for _, r := range d.Requests() {
		if r.Method == "GET" {
			t.Fatal("a detail request was spent on a board over the cap")
		}
	}
	b.Filters = map[string][]string{"locations": {"x"}}
	if _, err := s.List(context.Background(), b); err == nil || !strings.Contains(err.Error(), "filters") {
		t.Fatalf("filters accepted: %v", err)
	}
}

func TestMalformedJSON(t *testing.T) {
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) { return ok(`{"total":`), true })
	if _, err := s.List(context.Background(), board); err == nil {
		t.Fatal("malformed listing accepted")
	}
	h := fixtureHandler(t)
	s, _ = newSource(func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method == "GET" {
			return ok(`<html>`), true
		}
		return h(r)
	})
	if got, err := s.List(context.Background(), board); err == nil || got != nil {
		t.Fatalf("malformed detail accepted: %v, %v", got, err)
	}
}

func TestDetailWithoutReqIDIsError(t *testing.T) {
	h := fixtureHandler(t)
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method == "GET" {
			return ok(`{"jobPostingInfo":{"title":"x"}}`), true
		}
		return h(r)
	})
	if _, err := s.List(context.Background(), board); !errors.Is(err, posting.ErrKeyPart) {
		t.Fatalf("err = %v, want ErrKeyPart", err)
	}
}

func TestBoardValidation(t *testing.T) {
	never := func(srctest.Request) (srctest.Reply, bool) {
		t.Error("request made for an invalid board")
		return srctest.Reply{}, false
	}
	cases := map[string]sources.Board{
		"wrong ats":        {ATS: posting.Lever, Token: board.Token, Host: host},
		"empty token":      {ATS: posting.Workday, Host: host},
		"no site":          {ATS: posting.Workday, Token: "nvidia", Host: host},
		"empty site":       {ATS: posting.Workday, Token: "nvidia/", Host: host},
		"extra segment":    {ATS: posting.Workday, Token: "nvidia/a/b", Host: host},
		"path in token":    {ATS: posting.Workday, Token: "nvidia/..%2f", Host: host},
		"empty host":       {ATS: posting.Workday, Token: board.Token},
		"tenant mismatch":  {ATS: posting.Workday, Token: board.Token, Host: "other.wd5.myworkdayjobs.com"},
		"host in userinfo": {ATS: posting.Workday, Token: board.Token, Host: "nvidia@evil.example"},
		"host with path":   {ATS: posting.Workday, Token: board.Token, Host: "nvidia.wd5.myworkdayjobs.com/x"},
		"host with port":   {ATS: posting.Workday, Token: board.Token, Host: "nvidia.example:8080"},
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := newSource(never)
			if _, err := s.List(context.Background(), b); err == nil {
				t.Fatal("invalid board accepted")
			}
		})
	}
}

func TestHostMatchIsCaseInsensitive(t *testing.T) {
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		// The path keeps the token's case; only the host is lowered.
		return ok(`{"total":0,"jobPostings":[]}`),
			r.URL == "https://"+host+"/wday/cxs/NVIDIA/NVIDIAExternalCareerSite/jobs"
	})
	b := board
	b.Host = "NVIDIA.wd5.myworkdayjobs.com"
	b.Token = "NVIDIA/NVIDIAExternalCareerSite"
	if _, err := s.List(context.Background(), b); err != nil {
		t.Fatal(err)
	}
}

func TestHostileExternalPath(t *testing.T) {
	for _, p := range []string{"job/x", "/../admin", "/job//x", "/job/./x", ""} {
		s, d := newSource(func(r srctest.Request) (srctest.Reply, bool) {
			body, _ := json.Marshal(map[string]any{"total": 1, "jobPostings": []map[string]string{{"title": "t", "externalPath": p}}})
			return ok(string(body)), r.URL == jobsURL
		})
		if _, err := s.List(context.Background(), board); err == nil {
			t.Errorf("externalPath %q accepted", p)
		}
		if n := len(d.Requests()); n != 1 {
			t.Errorf("externalPath %q: %d requests, want only the listing", p, n)
		}
	}
}

func TestExternalPathSegmentsAreEscaped(t *testing.T) {
	var got string
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method == "POST" {
			return ok(`{"total":1,"jobPostings":[{"title":"t","externalPath":"/job/a b/c?d_JR1"}]}`), true
		}
		got = r.URL
		return ok(`{"jobPostingInfo":{"jobReqId":"JR1","title":"t"}}`), true
	})
	if _, err := s.List(context.Background(), board); err != nil {
		t.Fatal(err)
	}
	if want := siteURL + "/job/a%20b/c%3Fd_JR1"; got != want {
		t.Errorf("detail URL %q, want %q", got, want)
	}
}

func TestPostedAtPhrases(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		start, phrase string
		want          time.Time
	}{
		{"", "Posted Today", day(18)},
		{"", "posted today", day(18)},
		{"", "Posted Yesterday", day(17)},
		{"", "Posted 3 Days Ago", day(15)},
		{"", "Posted 1 Day Ago", day(17)},
		{"", "Posted 30+ Days Ago", time.Time{}},
		{"", "Posted 0 Days Ago", time.Time{}},
		{"", "Posted -2 Days Ago", time.Time{}},
		{"", "Posted 99999 Days Ago", time.Time{}},
		{"", "Posted Sometime", time.Time{}},
		{"", "", time.Time{}},
		{"2026-09-01", "Posted Today", day(18).AddDate(0, 0, -47)},
		{"not a date", "Posted Yesterday", day(17)},
		{"2026-13-45", "", time.Time{}},
	}
	for _, c := range cases {
		if got := postedAt(c.start, c.phrase, fixedNow); !got.Equal(c.want) {
			t.Errorf("postedAt(%q, %q) = %v, want %v", c.start, c.phrase, got, c.want)
		}
	}
}

func TestStartDatePreferredOverPhrase(t *testing.T) {
	// The fixture phrase says Today (the 18th); the detail startDate differs.
	h := fixtureHandler(t)
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		rep, found := h(r)
		rep.Body = []byte(strings.ReplaceAll(string(rep.Body), `"2026-10-18"`, `"2026-10-01"`))
		return rep, found
	})
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC); !got[0].PostedAt.Equal(want) {
		t.Errorf("PostedAt %v, want startDate %v", got[0].PostedAt, want)
	}
}

func TestRemoteType(t *testing.T) {
	h := fixtureHandler(t)
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		rep, found := h(r)
		if strings.HasSuffix(r.URL, "_JR2027047") {
			rep.Body = []byte(strings.Replace(string(rep.Body), `"timeType"`, `"remoteType": "Remote", "timeType"`, 1))
		}
		return rep, found
	})
	got, err := s.List(context.Background(), board)
	if err != nil || !got[0].Remote || got[1].Remote {
		t.Fatalf("remote flags %v / %v, err %v", got[0].Remote, got[1].Remote, err)
	}
}

func TestDescriptionCapped(t *testing.T) {
	huge := strings.Repeat("<p>word </p>", srcutil.MaxDescription)
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method == "POST" {
			return ok(`{"total":1,"jobPostings":[{"title":"t","externalPath":"/job/x_JR1"}]}`), true
		}
		body, _ := json.Marshal(map[string]any{"jobPostingInfo": map[string]string{"jobReqId": "JR1", "title": "t", "jobDescription": huge}})
		return ok(string(body)), true
	})
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(got[0].Description); n == 0 || n > srcutil.MaxDescription {
		t.Errorf("description is %d bytes, want 1..%d", n, srcutil.MaxDescription)
	}
}

func TestContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, _ := newSource(func(srctest.Request) (srctest.Reply, bool) {
		return ok(`{"total":0,"jobPostings":[]}`), true
	})
	if _, err := s.List(ctx, board); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestNilDoer(t *testing.T) {
	if _, err := New(nil).List(context.Background(), board); err == nil {
		t.Fatal("nil doer accepted")
	}
}

func TestATS(t *testing.T) {
	if New(nil).ATS() != posting.Workday {
		t.Fatal("wrong ATS")
	}
}
