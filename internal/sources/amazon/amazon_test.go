package amazon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

var fixedNow = time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)

func board(terms ...string) sources.Board {
	return sources.Board{ATS: posting.Amazon, Token: "amazon", Company: "Amazon", Searches: terms}
}

func fixtureJobs(t *testing.T) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/search.json")
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Jobs []map[string]any `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r.Jobs
}

func reply(hits int, jobs []map[string]any) string {
	raw, _ := json.Marshal(map[string]any{"error": nil, "hits": hits, "facets": map[string]any{}, "jobs": jobs})
	return string(raw)
}

// handler answers each search request from pages[term] by offset.
func handler(t *testing.T, hits map[string]int, pages map[string][][]map[string]any) func(srctest.Request) (srctest.Reply, bool) {
	return func(r srctest.Request) (srctest.Reply, bool) {
		u, err := url.Parse(r.URL)
		if err != nil || r.Method != "GET" || u.Host != "www.amazon.jobs" || u.Path != "/en/search.json" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			return srctest.Reply{}, false
		}
		q := u.Query()
		term := q.Get("base_query")
		off := 0
		fmt.Sscan(q.Get("offset"), &off)
		if q.Get("result_limit") != "100" {
			t.Errorf("result_limit %q", q.Get("result_limit"))
		}
		seenOff := 0
		for _, pg := range pages[term] {
			if seenOff == off {
				return srctest.Reply{Status: 200, Body: []byte(reply(hits[term], pg))}, true
			}
			seenOff += len(pg)
		}
		return srctest.Reply{Status: 200, Body: []byte(reply(hits[term], []map[string]any{}))}, true
	}
}

func newSource(h func(srctest.Request) (srctest.Reply, bool)) (*source, *srctest.Doer) {
	d := srctest.NewFunc(h)
	return &source{d: d, now: func() time.Time { return fixedNow }}, d
}

func TestFixture(t *testing.T) {
	jobs := fixtureJobs(t)
	s, _ := newSource(handler(t, map[string]int{"LLM inference": 3}, map[string][][]map[string]any{"LLM inference": {jobs}}))
	got, err := s.List(context.Background(), board("LLM inference"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d postings", len(got))
	}
	p := got[0]
	if p.Key != "az:amazon:10578742" || p.ATS != posting.Amazon || p.Company != "Amazon" ||
		p.Title != "Senior Software Development Engineer, Core Search" || p.Location != "Seattle, Washington, USA" ||
		p.URL != "https://www.amazon.jobs/en/jobs/10578742/senior-software-development-engineer-core-search" ||
		p.ApplyURL != "https://account.amazon.jobs/jobs/10578742/apply" || p.Department != "Software Development" ||
		!p.PostedAt.Equal(time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)) || p.Remote || !p.SeenAt.Equal(fixedNow) {
		t.Errorf("posting: %+v", p)
	}
	for _, want := range []string{"Role: full-time", "Basic Qualifications", "5+ years of non-internship", "Preferred Qualifications"} {
		if !strings.Contains(p.Description, want) {
			t.Errorf("description lacks %q: %.400s", want, p.Description)
		}
	}
	if strings.Contains(p.Description, "<br") {
		t.Errorf("markup kept: %.200s", p.Description)
	}
}

// Paging by offset to the hit count, filters on every request, and the union
// of terms with each posting once.
func TestPagingFiltersAndUnion(t *testing.T) {
	base := fixtureJobs(t)[0]
	mk := func(i int) map[string]any {
		j := map[string]any{}
		for k, v := range base {
			j[k] = v
		}
		id := fmt.Sprint(20000000 + i)
		j["id_icims"], j["job_path"] = id, "/en/jobs/"+id+"/role"
		return j
	}
	var a []map[string]any
	for i := 0; i < 150; i++ {
		a = append(a, mk(i))
	}
	pages := map[string][][]map[string]any{"a": {a[:100], a[100:]}, "b": {{mk(0), mk(149), mk(500)}}}
	var reqs []string
	h := handler(t, map[string]int{"a": 150, "b": 3}, pages)
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		reqs = append(reqs, r.URL)
		u, _ := url.Parse(r.URL)
		if u.Query().Get("normalized_country_code[]") != "USA" {
			t.Errorf("filter missing: %s", r.URL)
		}
		return h(r)
	})
	b := board("a", "b")
	b.Filters = map[string][]string{"normalized_country_code[]": {"USA"}}
	got, err := s.List(context.Background(), b)
	if err != nil || len(got) != 151 {
		t.Fatalf("got %d, err %v; want 150 + 1 new", len(got), err)
	}
	if len(reqs) != 3 {
		t.Errorf("%d requests, want 3 (two pages of a, one of b): %v", len(reqs), reqs)
	}
}

func TestRefusals(t *testing.T) {
	jobs := fixtureJobs(t)
	bad := func(k string, v any) []map[string]any {
		j := map[string]any{}
		for kk, vv := range jobs[0] {
			j[kk] = vv
		}
		j[k] = v
		return []map[string]any{j}
	}
	for name, tc := range map[string]struct {
		hits int
		page []map[string]any
		raw  string
		want error
	}{
		"over the cap":          {hits: MaxPostings + 1, page: jobs, want: srcutil.ErrTooManyPostings},
		"short of hits":         {hits: 5, page: jobs, want: ErrShape},
		"bad id":                {hits: 1, page: bad("id_icims", "abc"), want: ErrShape},
		"path names another id": {hits: 1, page: bad("job_path", "/en/jobs/1/x"), want: ErrShape},
		"offsite path":          {hits: 1, page: bad("job_path", "https://evil.example/en/jobs/10578742/x"), want: ErrShape},
		"empty title":           {hits: 1, page: bad("title", " "), want: ErrShape},
		"bad posted_date":       {hits: 1, page: bad("posted_date", "5 days ago"), want: ErrShape},
		"no hits":               {raw: `{"jobs":[]}`, want: ErrShape},
		"api error":             {raw: `{"error":"bad query","hits":0,"jobs":[]}`, want: ErrShape},
		"malformed":             {raw: `{"hits":`, want: nil},
	} {
		body := tc.raw
		if body == "" {
			body = reply(tc.hits, tc.page)
		}
		s, _ := newSource(func(srctest.Request) (srctest.Reply, bool) {
			return srctest.Reply{Status: 200, Body: []byte(body)}, true
		})
		got, err := s.List(context.Background(), board("x"))
		if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("%s: got %d postings, err %v; want %v", name, len(got), err, tc.want)
		}
	}
	// Zero hits is an honest empty term.
	s, _ := newSource(func(srctest.Request) (srctest.Reply, bool) {
		return srctest.Reply{Status: 200, Body: []byte(`{"error":null,"hits":0,"jobs":[]}`)}, true
	})
	if got, err := s.List(context.Background(), board("x")); err != nil || len(got) != 0 {
		t.Errorf("zero hits: %d %v", len(got), err)
	}
	// A non-https apply link is dropped, not followed.
	j := bad("url_next_step", "javascript:alert(1)")
	s, _ = newSource(func(srctest.Request) (srctest.Reply, bool) {
		return srctest.Reply{Status: 200, Body: []byte(reply(1, j))}, true
	})
	if got, err := s.List(context.Background(), board("x")); err != nil || got[0].ApplyURL != "" {
		t.Errorf("javascript apply url kept: %v %v", got, err)
	}
	// No search, unverified filter, cancelled context.
	if _, err := s.List(context.Background(), board()); err == nil {
		t.Error("no search accepted")
	}
	b := board("x")
	b.Filters = map[string][]string{"loc_query": {"Seattle"}}
	if _, err := s.List(context.Background(), b); err == nil {
		t.Error("unverified filter accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.List(ctx, board("x")); err == nil {
		t.Error("cancelled context accepted")
	}
}

func TestRoleFlagsAndRemote(t *testing.T) {
	tr := true
	j := job{IDIcims: "1", Title: "T", JobPath: "/en/jobs/1/t", IsIntern: &tr, IsManager: &tr, JobScheduleType: "full-time",
		Locations: []string{`{"normalizedLocation":"Virtual, USA","type":"REMOTE"}`, `not json`, `{"normalizedLocation":"Seattle, Washington, USA","type":"ONSITE"}`}}
	p, err := j.posting(board("x"), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Description, "Role: internship, people manager, full-time") || !p.Remote ||
		p.Location != "Virtual, USA; Seattle, Washington, USA" {
		t.Errorf("%+v", p)
	}
}
