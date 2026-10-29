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

	"github.com/bharathvbcr/Vani/internal/jobinsight"
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

// A board whose requests would wait past the pace budget fails, with the
// error saying so, before it waits past it.
func TestPaceBudgetFailsTheBoard(t *testing.T) {
	tmpl := fixtureJobs(t)[1]
	n := (int(paceBudget/crawlDelay) + 2) * pageSize
	var rows []map[string]any
	for i := 0; i < min(n, srcutil.MaxPostings); i++ {
		rows = append(rows, synth(tmpl, fmt.Sprint(10000+i)))
	}
	// Several locations, each its own query series, so the board's requests
	// together pass the budget while every query stays under MaxPostings.
	b := board()
	b.Filters = map[string][]string{"location": {"A", "B", "C", "D", "E"}}
	f := &site{t: t, rows: map[query][]map[string]any{}}
	for _, l := range b.Filters["location"] {
		f.rows[query{location: l}] = rows
	}
	s, waits := newSource(f)
	_, err := s.List(context.Background(), b)
	if !errors.Is(err, srcutil.ErrPaceBudget) {
		t.Fatalf("err %v, want ErrPaceBudget", err)
	}
	var total time.Duration
	for _, w := range *waits {
		total += w
	}
	if total > paceBudget {
		t.Errorf("waited %s, over the %s budget", total, paceBudget)
	}
}

// The employment type leads as one wording; a row without one gets none.
func TestEmploymentTypeLine(t *testing.T) {
	for in, want := range map[any]string{
		"FULL_TIME": "Role: full-time", "PART_TIME": "Role: part-time", "TEMPORARY": "Role: temporary",
		"CONTRACTOR": "Role: contract", nil: "", "": "", "  ": "",
	} {
		jobs := fixtureJobs(t)[:1]
		jobs[0]["employment_type"] = in
		jobs[0]["tags2"], jobs[0]["tags3"] = nil, nil
		f := &site{t: t, rows: map[query][]map[string]any{{}: jobs}}
		s, _ := newSource(f)
		got, err := s.List(context.Background(), board())
		if err != nil {
			t.Fatal(err)
		}
		line, _, _ := strings.Cut(got[0].Description, "\n")
		if want == "" {
			if strings.HasPrefix(got[0].Description, srcutil.RolePrefix) {
				t.Errorf("%v: role line %q", in, line)
			}
			continue
		}
		if line != want || srcutil.SourceText(got[0].Description) == got[0].Description {
			t.Errorf("%v: first line %q, want %q", in, line, want)
		}
	}
}

// Titles are one clean line: AMD served a double space (2026-10-20).
func TestTitleIsCleaned(t *testing.T) {
	jobs := fixtureJobs(t)[:1]
	jobs[0]["title"] = "Senior Graphics Machine Learning  Engineer &amp; Lead\n"
	f := &site{t: t, rows: map[query][]map[string]any{{}: jobs}}
	s, _ := newSource(f)
	got, err := s.List(context.Background(), board())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Title != "Senior Graphics Machine Learning Engineer & Lead" {
		t.Errorf("title %q", got[0].Title)
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
		"req_id not canonical": {rows: []map[string]any{bad("req_id", "5x")}, want: ErrShape},
		// synth keeps canonical_url in step, so only idRE refuses these.
		"req_id with slash":     {rows: []map[string]any{synth(tmpl, "5/6")}, want: ErrShape},
		"req_id with colon":     {rows: []map[string]any{synth(tmpl, "a:b")}, want: ErrShape},
		"req_id with space":     {rows: []map[string]any{synth(tmpl, "5 6")}, want: ErrShape},
		"req_id leading dash":   {rows: []map[string]any{synth(tmpl, "-5")}, want: ErrShape},
		"req_id percent":        {rows: []map[string]any{synth(tmpl, "5%2F6")}, want: ErrShape},
		"req_id oversized":      {rows: []map[string]any{synth(tmpl, strings.Repeat("9", 65))}, want: ErrShape},
		"ids differing in case": {rows: []map[string]any{synth(tmpl, "P1-5-1"), synth(tmpl, "p1-5-1")}, want: ErrShape},
		"empty req_id":          {rows: []map[string]any{bad("req_id", "")}, want: ErrShape},
		"other client":          {rows: []map[string]any{bad("client_code", "intel")}, want: ErrShape},
		"offsite canonical":     {rows: []map[string]any{bad("meta_data", map[string]any{"canonical_url": "https://evil.example/jobs/5"})}, want: ErrShape},
		"canonical other id":    {rows: []map[string]any{bad("meta_data", map[string]any{"canonical_url": "https://careers.amd.com/jobs/6"})}, want: ErrShape},
		"canonical http":        {rows: []map[string]any{bad("meta_data", map[string]any{"canonical_url": "http://careers.amd.com/jobs/5"})}, want: ErrShape},
		"canonical userinfo":    {rows: []map[string]any{bad("meta_data", map[string]any{"canonical_url": "https://careers.amd.com@evil.example/jobs/5"})}, want: ErrShape},
		// Only the board's own client is accepted on Jibe's hosted domain.
		"canonical other client's jibeapply": {rows: []map[string]any{bad("meta_data", map[string]any{"canonical_url": "https://intel.jibeapply.com/jobs/5"})}, want: ErrShape},
		"canonical jibeapply apex":           {rows: []map[string]any{bad("meta_data", map[string]any{"canonical_url": "https://jibeapply.com/jobs/5"})}, want: ErrShape},
		"canonical jibeapply suffix":         {rows: []map[string]any{bad("meta_data", map[string]any{"canonical_url": "https://amd.jibeapply.com.evil.example/jobs/5"})}, want: ErrShape},
		"canonical jibeapply http":           {rows: []map[string]any{bad("meta_data", map[string]any{"canonical_url": "http://amd.jibeapply.com/jobs/5"})}, want: ErrShape},
		"canonical jibeapply other id":       {rows: []map[string]any{bad("meta_data", map[string]any{"canonical_url": "https://amd.jibeapply.com/jobs/6"})}, want: ErrShape},
		"empty title":           {rows: []map[string]any{bad("title", " ")}, want: ErrShape},
		"bad posted_date":       {rows: []map[string]any{bad("posted_date", "yesterday")}, want: ErrShape},
		"script apply url":      {rows: []map[string]any{bad("apply_url", "javascript:alert(1)")}, want: nil},
		"userinfo apply url":    {rows: []map[string]any{bad("apply_url", "https://careers-amd.icims.com@evil.example/")}, want: nil},
		"duplicate within run":  {rows: []map[string]any{synth(tmpl, "7"), synth(tmpl, "7")}, total: 3, want: ErrShape},
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
	// A site on its own domain may name Jibe's hosted copy of itself,
	// {client}.jibeapply.com, as canonical (GitHub's every row on 2026-10-28,
	// on www.github.careers). The posting keeps the board's host, which serves
	// the same job page.
	for _, c := range []string{"https://amd.jibeapply.com/jobs/5?lang=en-us", "https://AMD.jibeapply.com/jobs/5"} {
		f := &site{t: t, rows: map[query][]map[string]any{{term: "x"}: {bad("meta_data", map[string]any{"canonical_url": c})}}}
		s, _ := newSource(f)
		got, err := s.List(context.Background(), board("x"))
		if err != nil || len(got) != 1 || got[0].URL != "https://"+host+"/jobs/5" {
			t.Errorf("canonical %q: %v %v", c, got, err)
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
		}), now: time.Now, wait: srcutil.Sleep}
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
		}), now: time.Now, wait: srcutil.Sleep}
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

// The fixture's real tags2/tags3 lead each description as a pay line that
// jobinsight reads and SourceText removes, so adding it re-renders a stored
// posting rather than re-triaging it.
func TestFixturePay(t *testing.T) {
	jobs := fixtureJobs(t)
	f := &site{t: t, rows: map[query][]map[string]any{{term: "machine learning"}: jobs}}
	s, _ := newSource(f)
	got, err := s.List(context.Background(), board("machine learning"))
	if err != nil {
		t.Fatal(err)
	}
	// The lead: the pay line, then the employment type where the row has one
	// (91170 has none; the other two are FULL_TIME).
	want := map[posting.Key]string{
		"jb:amd:91170": "Pay range: USD 60320 – 73840 per year",
		"jb:amd:91613": "Pay range: USD 178500 – 255000 per year\nRole: full-time",
		"jb:amd:93266": "Pay range: USD 178500 – 255000 per year\nRole: full-time",
	}
	for _, p := range got {
		line, rest, _ := strings.Cut(p.Description, "\n\n")
		if line != want[p.Key] {
			t.Errorf("%s lead %q, want %q", p.Key, line, want[p.Key])
		}
		pays := jobinsight.ParsePay(p.Description)
		if len(pays) == 0 || pays[0].Currency != "USD" {
			t.Errorf("%s: ParsePay read %+v", p.Key, pays)
		}
		if srcutil.SourceText(p.Description) != rest || strings.HasPrefix(rest, "Pay range") {
			t.Errorf("%s: SourceText kept the pay line", p.Key)
		}
		if p.Remote {
			t.Errorf("%s: an office location is remote", p.Key)
		}
	}
}

func TestPayLine(t *testing.T) {
	one := func(s string) []string { return []string{s} }
	for _, c := range []struct {
		name   string
		lo, hi []string
		want   string
	}{
		{"usd", one("USD $178,500.00/Yr."), one("USD $255,000.00/Yr."), "Pay range: USD 178500 – 255000 per year"},
		{"inr symbol", one("INR ₹3,338,160.00/Yr."), one("INR ₹5,007,240.00/Yr."), "Pay range: INR 3338160 – 5007240 per year"},
		{"multi-rune symbol", one("SGD S$90,000.00/Yr."), one("SGD S$120,000.00/Yr."), "Pay range: SGD 90000 – 120000 per year"},
		{"equal", one("EUR €78,680.00/Yr."), one("EUR €78,680.00/Yr."), "Pay range: EUR 78680 per year"},
		{"hourly", one("USD $30.50/Hr."), one("USD $45.00/Hr."), "Pay range: USD 30.5 – 45 per hour"},
		{"no period dot", one("USD $1,000/Mo"), one("USD $2,000/Mo"), "Pay range: USD 1000 – 2000 per month"},
		{"padded", one(" USD $10.00/Hr. "), one("USD $20.00/Hr."), "Pay range: USD 10 – 20 per hour"},
		{"internship zero", one("USD $0.00/Yr."), one("USD $0.00/Yr."), ""},
		{"zero bottom", one("USD $0.00/Yr."), one("USD $90,000.00/Yr."), "Pay range: USD 90000 per year"},
		{"inverted", one("USD $200,000.00/Yr."), one("USD $100,000.00/Yr."), ""},
		{"two currencies", one("USD $1.00/Yr."), one("CAD $2.00/Yr."), ""},
		{"two periods", one("USD $20.00/Hr."), one("USD $90,000.00/Yr."), ""},
		{"missing", nil, one("USD $1.00/Yr."), ""},
		{"empty", []string{}, []string{}, ""},
		{"two values", []string{"USD $1.00/Yr.", "USD $2.00/Yr."}, one("USD $3.00/Yr."), ""},
		{"lowercase code", one("usd $1.00/Yr."), one("usd $2.00/Yr."), ""},
		{"bad commas", one("USD $1,23.00/Yr."), one("USD $200.00/Yr."), ""},
		{"unknown period", one("USD $1.00/Qtr."), one("USD $2.00/Qtr."), ""},
		{"no amount", one("USD $/Yr."), one("USD $/Yr."), ""},
		{"prose", one("Competitive"), one("Competitive"), ""},
		{"oversized", one("USD $" + strings.Repeat("9", 400) + "/Yr."), one("USD $1.00/Yr."), ""},
		{"long symbol", one("USD abcde1.00/Yr."), one("USD abcde2.00/Yr."), ""},
	} {
		if got := payLine(c.lo, c.hi); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// The pay line survives a description at the cap, and the whole stays within it.
func TestPayLineUnderCap(t *testing.T) {
	j := job{Description: "<p>" + strings.Repeat("word ", srcutil.MaxDescription) + "</p>",
		Tags2: []string{"USD $1,000.00/Yr."}, Tags3: []string{"USD $2,000.00/Yr."}}
	d := description(j)
	if !strings.HasPrefix(d, "Pay range: USD 1000 – 2000 per year\n\nword") || len(d) > srcutil.MaxDescription {
		t.Errorf("len %d, starts %.60q", len(d), d)
	}
}

func TestHomeOffice(t *testing.T) {
	// Every home-office form seen live on 2026-10-20 (AMD's comma-separated
	// ones, PepsiCo's dash-separated one), and office ones from both.
	for name, want := range map[string]bool{
		"US,TX,Home Office": true, "US,CA,Home Office_Area2": true, "RO,Home Office": true,
		"CA,ON,Home Office": true, " us,ny,HOME OFFICE ": true, "Home Office": true,
		"Neu-Isenburg Home Office-DEU": true, "Home Office,US,TX": true, "US,TX,Home  Office": true,
		"US,TX,Austin": false, "US,NJ,Secaucus_Meadowlands": false, "CA,ON,Markham-1CV": false,
		"AB-Red Deer-7429-Office-CAN4499AA": false, "TX-Abilene-650-PBC-TX10017": false, "Jeddah-DC-SAU": false,
		"": false, "US,TX,Home Officers": false, "US,TX,Home Office Depot": false, "Homeoffice": false,
		",": false, "-": false, "Office": false, "My Home Office Supply-DEU": false,
	} {
		if got := homeOffice(name); got != want {
			t.Errorf("%q: %v", name, got)
		}
	}
	jobs := fixtureJobs(t)
	jobs[1]["location_name"] = "US,CA,Home Office_Area1"
	f := &site{t: t, rows: map[query][]map[string]any{{}: jobs}}
	s, _ := newSource(f)
	got, err := s.List(context.Background(), sources.Board{ATS: posting.Jibe, Token: "amd", Host: host})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Remote || !got[1].Remote || got[2].Remote {
		t.Errorf("remote %v %v %v", got[0].Remote, got[1].Remote, got[2].Remote)
	}
}

// A second tenant: PepsiCo's site (rows trimmed from a live "merchandiser"
// search, 2026-10-20) has no client_code, lists Paradox-hosted roles under
// ids like "P1-6635769-1", and uses tags2 for a date, which is no pay range.
func TestSecondTenant(t *testing.T) {
	raw, err := os.ReadFile("testdata/pepsico_paradox.json")
	if err != nil {
		t.Fatal(err)
	}
	const pepsi = "www.pepsicojobs.com"
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if u, err := url.Parse(r.URL); err != nil || u.Host != pepsi || u.Path != "/api/jobs" {
			t.Errorf("unexpected request %s", r.URL)
			return srctest.Reply{}, false
		}
		return srctest.Reply{Status: 200, Body: raw}, true
	})
	s := &source{d: d, now: func() time.Time { return fixedNow },
		wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }}
	got, err := s.List(context.Background(), sources.Board{ATS: posting.Jibe, Token: "pepsico", Company: "PepsiCo", Host: pepsi, Searches: []string{"merchandiser"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d postings", len(got))
	}
	p := got[1]
	if p.Key != "jb:pepsico:p1-6635769-1" || p.URL != "https://www.pepsicojobs.com/jobs/P1-6635769-1" ||
		!strings.HasPrefix(p.ApplyURL, "https://olivia.paradox.ai/") || p.Company != "PepsiCo" {
		t.Errorf("paradox posting: %+v", p)
	}
	if got[0].Key != "jb:pepsico:476135" {
		t.Errorf("numeric posting key %s", got[0].Key)
	}
	for _, p := range got {
		if strings.HasPrefix(p.Description, srcutil.PayLinePrefix) || p.Remote {
			t.Errorf("%s: pay line or remote from another tenant's tags: %.80q remote=%v", p.Key, p.Description, p.Remote)
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
