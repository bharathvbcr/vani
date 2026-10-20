package jibe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

var fixedNow = time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)

const host = "careers.amd.com"

func board(terms ...string) sources.Board {
	return sources.Board{ATS: posting.Jibe, Token: "amd", Company: "AMD", Host: host, Searches: terms}
}

// fixtureJobs is the saved page's rows (a real AMD "machine learning" search
// in the United States, trimmed to three rows and canonical_url).
func fixtureJobs(t *testing.T) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/jobs.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Jobs []struct {
			Data map[string]any `json:"data"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	out := make([]map[string]any, len(v.Jobs))
	for i, j := range v.Jobs {
		out[i] = j.Data
	}
	return out
}

// synth is a copy of tmpl renumbered to id.
func synth(tmpl map[string]any, id string) map[string]any {
	j := map[string]any{}
	for k, v := range tmpl {
		j[k] = v
	}
	j["req_id"], j["slug"], j["title"] = id, id, "Role "+id
	j["meta_data"] = map[string]any{"canonical_url": "https://" + host + "/jobs/" + id + "?lang=en-us"}
	return j
}

// site is a fake Jibe site: each (keywords, location) query has its rows,
// served in pages of limit by the 1-based page number.
type site struct {
	t     *testing.T
	rows  map[query][]map[string]any
	total map[query]int // reported totalCount (default len)
	reqs  []url.Values
}

func (f *site) handle(r srctest.Request) (srctest.Reply, bool) {
	u, err := url.Parse(r.URL)
	if err != nil || u.Host != host || u.Path != "/api/jobs" || r.Method != "GET" {
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		return srctest.Reply{}, false
	}
	v := u.Query()
	f.reqs = append(f.reqs, v)
	if len(v["location"]) > 1 || len(v["keywords"]) > 1 {
		f.t.Errorf("repeated parameter in %v", v)
	}
	q := query{term: v.Get("keywords"), location: v.Get("location")}
	page, _ := strconv.Atoi(v.Get("page"))
	limit, _ := strconv.Atoi(v.Get("limit"))
	if page < 1 || limit != pageSize {
		f.t.Errorf("page %q limit %q", v.Get("page"), v.Get("limit"))
	}
	rows := f.rows[q]
	total, ok := f.total[q]
	if !ok {
		total = len(rows)
	}
	jobs := []map[string]any{}
	for i := (page - 1) * limit; i < len(rows) && i < page*limit; i++ {
		jobs = append(jobs, map[string]any{"data": rows[i]})
	}
	raw, _ := json.Marshal(map[string]any{"jobs": jobs, "totalCount": total, "count": total})
	return srctest.Reply{Status: 200, Body: raw}, true
}

// newSource records each wait instead of sleeping.
func newSource(f *site) (*source, *[]time.Duration) {
	var waits []time.Duration
	s := &source{d: srctest.NewFunc(f.handle), now: func() time.Time { return fixedNow },
		wait: func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return ctx.Err() }}
	return s, &waits
}

func TestFixture(t *testing.T) {
	jobs := fixtureJobs(t)
	f := &site{t: t, rows: map[query][]map[string]any{{term: "machine learning"}: jobs}}
	s, waits := newSource(f)
	got, err := s.List(context.Background(), board("machine learning"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || len(f.reqs) != 1 || len(*waits) != 0 {
		t.Fatalf("got %d postings in %d requests with %d waits", len(got), len(f.reqs), len(*waits))
	}
	if q := f.reqs[0]; q.Get("page") != "1" || q.Get("limit") != "100" || q.Get("keywords") != "machine learning" || q.Has("location") {
		t.Errorf("query %v", q)
	}
	p := got[0]
	if p.Key != "jb:amd:91170" || p.ATS != posting.Jibe || p.Company != "AMD" ||
		p.Title != "2027 Undergrad Machine Learning (ML)/ Artificial Intelligence (AI) intern/co-op" ||
		p.Location != "Austin, Texas; Boxborough, Massachusetts; Fort Collins, Colorado; Longmont, Colorado; Rochester, New York; Fishkill, New York" ||
		p.Department != "Student / Intern / Temp" || p.Remote ||
		p.URL != "https://careers.amd.com/jobs/91170" || p.ApplyURL != "https://campus-amd.icims.com/jobs/91170/login" ||
		!p.PostedAt.Equal(time.Date(2026, 8, 20, 23, 28, 0, 0, time.UTC)) || p.UpdatedAt.IsZero() || !p.SeenAt.Equal(fixedNow) {
		t.Errorf("posting: %+v", p)
	}
	for _, p := range got {
		// The qualifications field is never inside AMD's description, so it
		// must be appended; tags must be gone.
		if !strings.Contains(p.Description, "Benefits offered") || strings.Contains(p.Description, "<p") || len(p.Description) > srcutil.MaxDescription {
			t.Errorf("%s description: %.300s", p.Key, p.Description)
		}
	}
	if got[1].Key != "jb:amd:91613" || got[2].Key != "jb:amd:93266" || got[1].Department != "Engineering" {
		t.Errorf("rows 2-3: %+v / %+v", got[1], got[2])
	}
}

// A responsibilities field already inside the description is not repeated;
// one that is not is appended once.
func TestDescriptionComposition(t *testing.T) {
	j := job{Description: "<p>Intro.</p><p>THE ROLE: build compilers.</p>", Responsibilities: "<p>THE ROLE: build compilers.</p>", Qualifications: "<p>Benefits offered are described.</p>"}
	d := description(j)
	if strings.Count(d, "THE ROLE") != 1 || !strings.Contains(d, "Benefits offered") || !strings.HasPrefix(d, "Intro.") {
		t.Errorf("description %q", d)
	}
	j.Responsibilities = "<p>Lead the team.</p>"
	if d := description(j); !strings.Contains(d, "Lead the team.") || strings.Index(d, "Lead") > strings.Index(d, "Benefits") {
		t.Errorf("appended out of order: %q", d)
	}
	if d1, d2 := description(j), description(j); d1 != d2 {
		t.Error("description is not deterministic")
	}
	if description(job{}) != "" {
		t.Error("empty job has a description")
	}
}

// Paging by 1-based page to the total, spaced by the crawl delay.
func TestPagingAndPacing(t *testing.T) {
	tmpl := fixtureJobs(t)[1]
	var rows []map[string]any
	for i := 0; i < 250; i++ {
		rows = append(rows, synth(tmpl, fmt.Sprint(10000+i)))
	}
	f := &site{t: t, rows: map[query][]map[string]any{{}: rows}}
	s, waits := newSource(f)
	got, err := s.List(context.Background(), board())
	if err != nil || len(got) != 250 {
		t.Fatalf("got %d, err %v", len(got), err)
	}
	if len(f.reqs) != 3 || f.reqs[2].Get("page") != "3" || f.reqs[0].Has("keywords") {
		t.Errorf("requests %v", f.reqs)
	}
	if len(*waits) != 2 || (*waits)[0] != crawlDelay {
		t.Errorf("waits %v, want two of %s", *waits, crawlDelay)
	}
}

// Every term is crossed with every location value, one location per request,
// and the board is their union with each posting once.
func TestUnionOfTermsAndLocations(t *testing.T) {
	tmpl := fixtureJobs(t)[1]
	j := func(id string) map[string]any { return synth(tmpl, id) }
	f := &site{t: t, rows: map[query][]map[string]any{
		{term: "a", location: "United States"}: {j("1"), j("2")},
		{term: "a", location: "Canada"}:        {j("3")},
		{term: "b", location: "United States"}: {j("2"), j("4")},
		{term: "b", location: "Canada"}:        {},
	}}
	s, waits := newSource(f)
	b := board("a", "b")
	b.Filters = map[string][]string{"location": {"United States", "Canada", " United States "}}
	got, err := s.List(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, p := range got {
		keys = append(keys, string(p.Key))
	}
	if strings.Join(keys, ",") != "jb:amd:1,jb:amd:2,jb:amd:3,jb:amd:4" || len(f.reqs) != 4 || len(*waits) != 3 {
		t.Errorf("keys %v in %d requests, %d waits", keys, len(f.reqs), len(*waits))
	}
}

func TestRefusals(t *testing.T) {
	tmpl := fixtureJobs(t)[1]
	bad := func(k string, v any) map[string]any { j := synth(tmpl, "5"); j[k] = v; return j }
	for name, tc := range map[string]struct {
		rows  []map[string]any
		total int
		want  error
	}{
		"over the cap":         {rows: nil, total: srcutil.MaxPostings + 1, want: srcutil.ErrTooManyPostings},
		"short of total":       {rows: []map[string]any{synth(tmpl, "1")}, total: 30, want: ErrShape},
		"bad req_id":           {rows: []map[string]any{bad("req_id", "5x")}, want: ErrShape},
		"empty req_id":         {rows: []map[string]any{bad("req_id", "")}, want: ErrShape},
		"other client":         {rows: []map[string]any{bad("client_code", "intel")}, want: ErrShape},
		"offsite canonical":    {rows: []map[string]any{bad("meta_data", map[string]any{"canonical_url": "https://evil.example/jobs/5"})}, want: ErrShape},
		"canonical other id":   {rows: []map[string]any{bad("meta_data", map[string]any{"canonical_url": "https://careers.amd.com/jobs/6"})}, want: ErrShape},
		"canonical http":       {rows: []map[string]any{bad("meta_data", map[string]any{"canonical_url": "http://careers.amd.com/jobs/5"})}, want: ErrShape},
		"canonical userinfo":   {rows: []map[string]any{bad("meta_data", map[string]any{"canonical_url": "https://careers.amd.com@evil.example/jobs/5"})}, want: ErrShape},
		"empty title":          {rows: []map[string]any{bad("title", " ")}, want: ErrShape},
		"bad posted_date":      {rows: []map[string]any{bad("posted_date", "yesterday")}, want: ErrShape},
		"script apply url":     {rows: []map[string]any{bad("apply_url", "javascript:alert(1)")}, want: nil},
		"userinfo apply url":   {rows: []map[string]any{bad("apply_url", "https://careers-amd.icims.com@evil.example/")}, want: nil},
		"duplicate within run": {rows: []map[string]any{synth(tmpl, "7"), synth(tmpl, "7")}, total: 3, want: ErrShape},
	} {
		total := tc.total
		if total == 0 {
			total = len(tc.rows)
		}
		f := &site{t: t, rows: map[query][]map[string]any{{term: "x"}: tc.rows}, total: map[query]int{{term: "x"}: total}}
		s, _ := newSource(f)
		got, err := s.List(context.Background(), board("x"))
		if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("%s: got %d postings, err %v; want %v", name, len(got), err, tc.want)
		}
	}
	// Unsearched listings carry the client code; the board's own, in any
	// case, is accepted.
	for _, code := range []string{"amd", "AMD"} {
		f := &site{t: t, rows: map[query][]map[string]any{{}: {bad("client_code", code)}}}
		s, _ := newSource(f)
		if got, err := s.List(context.Background(), board()); err != nil || len(got) != 1 || got[0].Key != "jb:amd:5" {
			t.Errorf("client_code %q: %v %v", code, got, err)
		}
	}
	// Response shape failures.
	for name, body := range map[string]string{
		"no total":   `{"jobs":[]}`,
		"null jobs":  `{"totalCount":1,"jobs":null}`,
		"null row":   `{"totalCount":1,"jobs":[{"data":null}]}`,
		"neg total":  `{"totalCount":-1,"jobs":[]}`,
		"not json":   `<html>maintenance</html>`,
		"jobs not [": `{"totalCount":1,"jobs":{}}`,
	} {
		s := &source{d: srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) {
			return srctest.Reply{Status: 200, Body: []byte(body)}, true
		}), now: time.Now, wait: sleep}
		if _, err := s.List(context.Background(), board("x")); err == nil {
			t.Errorf("%s: accepted", name)
		} else if name != "not json" && name != "jobs not [" && !errors.Is(err, ErrShape) {
			t.Errorf("%s: %v, want ErrShape", name, err)
		}
	}
}

// The site answers 422 for a place it does not know; that error names the
// place. A 404 is a missing board.
func TestStatusErrors(t *testing.T) {
	status := func(code int) *source {
		return &source{d: srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) {
			return srctest.Reply{Status: code, Body: []byte(`{"error":{"status":422}}`)}, true
		}), now: time.Now, wait: sleep}
	}
	b := board("x")
	b.Filters = map[string][]string{"location": {"Nowhereville"}}
	if _, err := status(422).List(context.Background(), b); err == nil || !strings.Contains(err.Error(), `location "Nowhereville" is not one the site knows`) {
		t.Errorf("422 with location: %v", err)
	}
	if _, err := status(422).List(context.Background(), board("x")); err == nil || strings.Contains(err.Error(), "location") {
		t.Errorf("422 without location: %v", err)
	}
	if _, err := status(404).List(context.Background(), board()); !errors.Is(err, srcutil.ErrBoardNotFound) {
		t.Errorf("404: %v", err)
	}
}

func TestBoardChecksAndCancellation(t *testing.T) {
	f := &site{t: t, rows: map[query][]map[string]any{}}
	s, _ := newSource(f)
	for name, b := range map[string]sources.Board{
		"token a domain":    func() sources.Board { b := board(); b.Token = "amd.com"; return b }(),
		"token with colon":  func() sources.Board { b := board(); b.Token = "a:md"; return b }(),
		"token empty":       func() sources.Board { b := board(); b.Token = " "; return b }(),
		"host missing":      func() sources.Board { b := board(); b.Host = ""; return b }(),
		"host with path":    func() sources.Board { b := board(); b.Host = "evil.example/x"; return b }(),
		"host ip":           func() sources.Board { b := board(); b.Host = "10.0.0.1"; return b }(),
		"unverified filter": func() sources.Board { b := board(); b.Filters = map[string][]string{"country": {"Canada"}}; return b }(),
		"empty filter":      func() sources.Board { b := board(); b.Filters = map[string][]string{"location": {}}; return b }(),
		"other ats":         func() sources.Board { b := board(); b.ATS = posting.Eightfold; return b }(),
	} {
		if _, err := s.List(context.Background(), b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(f.reqs) != 0 {
		t.Errorf("refused boards sent %d requests", len(f.reqs))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.List(ctx, board()); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled context: %v", err)
	}
	// Cancelled during the crawl delay: the wait's error ends the listing.
	tmpl := fixtureJobs(t)[1]
	var rows []map[string]any
	for i := 0; i < 150; i++ {
		rows = append(rows, synth(tmpl, fmt.Sprint(20000+i)))
	}
	f2 := &site{t: t, rows: map[query][]map[string]any{{}: rows}}
	s2 := &source{d: srctest.NewFunc(f2.handle), now: time.Now,
		wait: func(context.Context, time.Duration) error { return context.DeadlineExceeded }}
	if _, err := s2.List(context.Background(), board()); !errors.Is(err, context.DeadlineExceeded) || len(f2.reqs) != 1 {
		t.Errorf("wait failure: %v after %d requests", err, len(f2.reqs))
	}
	// Zero results is an honest empty listing.
	if got, err := s.List(context.Background(), board("nothing")); err != nil || len(got) != 0 {
		t.Errorf("zero results: %d %v", len(got), err)
	}
}

func TestParseTime(t *testing.T) {
	want := time.Date(2026, 10, 20, 1, 55, 0, 0, time.UTC)
	for _, s := range []string{"2026-10-20T01:55:00+0000", "2026-10-20T01:55:00Z", "2026-10-19T20:55:00-0500", " 2026-10-20T01:55:00+00:00 "} {
		if got, err := parseTime("f", s); err != nil || !got.Equal(want) {
			t.Errorf("%q: %v %v", s, got, err)
		}
	}
	if got, err := parseTime("f", ""); err != nil || !got.IsZero() {
		t.Errorf("empty: %v %v", got, err)
	}
	for _, s := range []string{"2026-10-20", "20 Oct 2026", "2026-13-01T00:00:00+0000"} {
		if _, err := parseTime("f", s); !errors.Is(err, ErrShape) {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestATS(t *testing.T) {
	src := New(nil)
	if src.ATS() != posting.Jibe {
		t.Fatal(src.ATS())
	}
	if _, err := src.List(context.Background(), board("x")); err == nil {
		t.Fatal("nil doer must error, not panic")
	}
}
