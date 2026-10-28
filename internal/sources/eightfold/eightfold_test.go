package eightfold

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

const host = "apply.careers.microsoft.com"

func board(terms ...string) sources.Board {
	return sources.Board{ATS: posting.Eightfold, Token: "microsoft.com", Company: "Microsoft", Host: host, Searches: terms}
}

func readJSON(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// site is a fake careers site: search pages are sliced from positions by
// start and num, details are the fixture with the id swapped in.
type site struct {
	t         *testing.T
	positions map[string][]string // term -> ids
	count     map[string]int      // term -> reported count (default len)
	detail    map[string]any
	override  map[string]func(map[string]any)
	// robots replaces the robots.txt answer; nil is msRobots.
	robots *srctest.Reply
}

// msRobots is the apply.careers.microsoft.com robots.txt as read 2026-10-28,
// without the login, candidate and career-hub Allow lines.
const msRobots = "User-agent: *\nDisallow: /\nAllow: /$\nAllow: /careers\nAllow: /api/apply\nAllow: /api/pcsx\nAllow: /api/events\nAllow: /static/gen\nAllow: /gen\n"

func (f *site) handle(r srctest.Request) (srctest.Reply, bool) {
	u, err := url.Parse(r.URL)
	if err != nil || u.Host != host || r.Method != "GET" {
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		return srctest.Reply{}, false
	}
	if u.Path == "/robots.txt" {
		if f.robots != nil {
			return *f.robots, true
		}
		return srctest.Reply{Status: 200, Body: []byte(msRobots)}, true
	}
	q := u.Query()
	if q.Get("domain") != "microsoft.com" {
		f.t.Errorf("domain %q", q.Get("domain"))
	}
	var data any
	switch u.Path {
	case "/api/pcsx/search":
		term := q.Get("query")
		ids := f.positions[term]
		start := 0
		fmt.Sscan(q.Get("start"), &start)
		count, ok := f.count[term]
		if !ok {
			count = len(ids)
		}
		var page []map[string]any
		for i := start; i < len(ids) && i < start+10; i++ {
			page = append(page, map[string]any{"id": json.Number(ids[i]), "name": "Role " + ids[i]})
		}
		data = map[string]any{"count": count, "positions": page}
	case "/api/pcsx/position_details":
		id := q.Get("position_id")
		d := map[string]any{}
		for k, v := range f.detail {
			d[k] = v
		}
		d["id"], d["publicUrl"] = json.Number(id), "https://"+host+"/careers/job/"+id
		if o := f.override[id]; o != nil {
			o(d)
		}
		data = d
	default:
		f.t.Errorf("unexpected path %s", u.Path)
		return srctest.Reply{}, false
	}
	raw, _ := json.Marshal(map[string]any{"status": 200, "error": map[string]any{"message": "", "body": ""}, "data": data})
	return srctest.Reply{Status: 200, Body: raw}, true
}

func newSource(f *site) (*source, *srctest.Doer) {
	d := srctest.NewFunc(f.handle)
	return &source{d: d, now: func() time.Time { return fixedNow }, wait: noWait}, d
}

// noWait stands in for the pacer's sleep.
func noWait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func fixtureSite(t *testing.T) *site {
	search := readJSON(t, "search.json")
	var ids []string
	for _, p := range search["data"].(map[string]any)["positions"].([]any) {
		ids = append(ids, string(p.(map[string]any)["id"].(json.Number)))
	}
	return &site{t: t, positions: map[string][]string{"LLM inference": ids}, detail: readJSON(t, "detail.json")["data"].(map[string]any)}
}

func TestFixture(t *testing.T) {
	f := fixtureSite(t)
	s, _ := newSource(f)
	got, err := s.List(context.Background(), board("LLM inference"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d postings", len(got))
	}
	p := got[0]
	if p.Key != "ef:microsoft.com:1970393556991825" || p.ATS != posting.Eightfold || p.Company != "Microsoft" ||
		p.Title != "Senior Applied Scientist(Agentic RL & LLM Post-Training)" || p.Department != "Applied Sciences" ||
		p.Location != "China, Jiangsu, Suzhou; China, Beijing, Beijing" || p.Remote ||
		p.URL != "https://apply.careers.microsoft.com/careers/job/1970393556991825" ||
		!p.PostedAt.Equal(time.Unix(1791734400, 0).UTC()) || !p.SeenAt.Equal(fixedNow) {
		t.Errorf("posting: %+v", p)
	}
	// The role line leads with the employment type in the shared wording, and
	// SourceText leaves the board's own text.
	if !strings.HasPrefix(p.Description, "Role: Individual Contributor, full-time, Fully on-site\n\nOverview") ||
		strings.Contains(p.Description, "<b>") || !strings.HasPrefix(srcutil.SourceText(p.Description), "Overview") {
		t.Errorf("description: %.300s", p.Description)
	}
}

// Every request after the first waits paceEvery, and a board that would wait
// past paceBudget fails before it does.
func TestPacing(t *testing.T) {
	f := fixtureSite(t)
	var waits []time.Duration
	s, d := newSource(f)
	s.wait = func(ctx context.Context, w time.Duration) error { waits = append(waits, w); return ctx.Err() }
	if _, err := s.List(context.Background(), board("LLM inference")); err != nil {
		t.Fatal(err)
	}
	if n := len(d.Requests()); n < 2 || len(waits) != n-1 {
		t.Fatalf("%d requests, %d waits", n, len(waits))
	}
	for _, w := range waits {
		if w != paceEvery {
			t.Fatalf("wait %s, want %s", w, paceEvery)
		}
	}
	if paceBudget < time.Duration(MaxPostings+maxPages)*paceEvery {
		t.Errorf("a full board (%d requests at %s) does not fit the %s budget", MaxPostings+maxPages, paceEvery, paceBudget)
	}
	// Cancelled while waiting: the board ends with the context, not a pass.
	ctx, cancel := context.WithCancel(context.Background())
	s.wait = func(context.Context, time.Duration) error { cancel(); return context.Canceled }
	if _, err := s.List(ctx, board("LLM inference")); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled wait: %v", err)
	}
}

// Paging by start in tens to the count, the union of terms, one details
// request per position, filters on every search request.
func TestPagingUnionAndFilters(t *testing.T) {
	f := fixtureSite(t)
	var a []string
	for i := 0; i < 25; i++ {
		a = append(a, fmt.Sprint(5000000000+i))
	}
	f.positions = map[string][]string{"a": a, "b": {a[0], "6000000000"}}
	var searches, details int
	s, _ := newSource(&site{t: t, positions: f.positions, detail: f.detail})
	inner := s.d.(*srctest.Doer).Handler
	s.d = srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		u, _ := url.Parse(r.URL)
		if u.Path == "/api/pcsx/search" {
			searches++
			if u.Query().Get("location") != "United States" || u.Query().Get("num") != "10" {
				t.Errorf("search query %v", u.Query())
			}
		} else if u.Path == "/api/pcsx/position_details" {
			details++
		}
		return inner(r)
	})
	b := board("a", "b")
	b.Filters = map[string][]string{"location": {"United States"}}
	got, err := s.List(context.Background(), b)
	if err != nil || len(got) != 26 {
		t.Fatalf("got %d, err %v; want 26", len(got), err)
	}
	if searches != 4 || details != 26 {
		t.Errorf("%d search and %d details requests, want 4 and 26", searches, details)
	}
}

func TestRefusals(t *testing.T) {
	f := fixtureSite(t)
	id := f.positions["LLM inference"][0]
	for name, tc := range map[string]struct {
		count    map[string]int
		override map[string]func(map[string]any)
		want     error
	}{
		"over the cap":       {count: map[string]int{"LLM inference": MaxPostings + 1}, want: srcutil.ErrTooManyPostings},
		"short of count":     {count: map[string]int{"LLM inference": 30}, want: ErrShape},
		"details for other":  {override: map[string]func(map[string]any){id: func(d map[string]any) { d["id"] = json.Number("1") }}, want: ErrShape},
		"offsite public url": {override: map[string]func(map[string]any){id: func(d map[string]any) { d["publicUrl"] = "https://evil.example/careers/job/" + id }}, want: ErrShape},
		"empty name":         {override: map[string]func(map[string]any){id: func(d map[string]any) { d["name"] = " " }}, want: ErrShape},
	} {
		s, _ := newSource(&site{t: t, positions: f.positions, count: tc.count, detail: f.detail, override: tc.override})
		got, err := s.List(context.Background(), board("LLM inference"))
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: got %d postings, err %v; want %v", name, len(got), err, tc.want)
		}
	}
	// Envelope failures.
	for name, body := range map[string]string{
		"status 500 in envelope": `{"status":500,"error":{"message":"boom"},"data":null}`,
		"no data":                `{"status":200,"error":{},"data":null}`,
		"no count":               `{"status":200,"error":{},"data":{"positions":[]}}`,
		"bad id":                 `{"status":200,"error":{},"data":{"count":1,"positions":[{"id":"x1"}]}}`,
	} {
		s := &source{d: srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) {
			return srctest.Reply{Status: 200, Body: []byte(body)}, true
		}), now: time.Now, wait: noWait}
		if _, err := s.List(context.Background(), board("x")); !errors.Is(err, ErrShape) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Board checks: token, host, search, filter, context.
	s, _ := newSource(f)
	for name, b := range map[string]sources.Board{
		"token not a domain": func() sources.Board { b := board("x"); b.Token = "microsoft"; return b }(),
		"host missing":       func() sources.Board { b := board("x"); b.Host = ""; return b }(),
		"host with path":     func() sources.Board { b := board("x"); b.Host = "evil.example/x"; return b }(),
		"no search":          board(),
		"unverified filter":  func() sources.Board { b := board("x"); b.Filters = map[string][]string{"department": {"y"}}; return b }(),
	} {
		if _, err := s.List(context.Background(), b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.List(ctx, board("LLM inference")); err == nil {
		t.Error("cancelled context accepted")
	}
	// Zero results is an honest empty term.
	s, _ = newSource(&site{t: t, positions: map[string][]string{}, detail: f.detail})
	if got, err := s.List(context.Background(), board("nothing")); err != nil || len(got) != 0 {
		t.Errorf("zero results: %d %v", len(got), err)
	}
}

func TestRemote(t *testing.T) {
	f := fixtureSite(t)
	id := f.positions["LLM inference"][0]
	f.override = map[string]func(map[string]any){id: func(d map[string]any) { d["workLocationOption"] = "Remote" }}
	s, _ := newSource(f)
	got, err := s.List(context.Background(), board("LLM inference"))
	if err != nil || !got[0].Remote || got[1].Remote {
		t.Fatalf("remote flags: %v %v", got, err)
	}
	// Microsoft's remote roles say so only in the work site (live 2026-10-20,
	// position 1970393557019355); the description still names it.
	f.override = map[string]func(map[string]any){id: func(d map[string]any) {
		d["workLocationOption"] = "onsite"
		d["efcustomTextWorkSite"] = []any{"0 days / week in-office – remote"}
	}}
	s, _ = newSource(f)
	got, err = s.List(context.Background(), board("LLM inference"))
	if err != nil || !got[0].Remote || got[1].Remote || !strings.Contains(got[0].Description, "0 days / week in-office – remote") {
		t.Fatalf("work-site remote: %v %v", got, err)
	}
}

func TestRemoteRule(t *testing.T) {
	for _, c := range []struct {
		option string
		site   []string
		want   bool
	}{
		{"remote", nil, true},
		{" REMOTE ", nil, true},
		{"onsite", []string{"0 days / week in-office – remote"}, true},
		{"", []string{"3 days / week in-office", "0 days / week in-office — Remote"}, true},
		{"onsite", []string{"Remote"}, true},
		{"onsite", []string{"0 days / week in-office - remote "}, true},
		{"onsite", []string{"3 days / week in-office"}, false},
		{"onsite", []string{"fully on-site"}, false},
		{"hybrid", []string{"remote-friendly hub"}, false},
		{"onsite", []string{"not remote"}, false},
		{"onsite", []string{"remote eligible after 6 months – onsite"}, false},
		{"onsite", []string{"", "–", "-"}, false},
		{"", nil, false},
		{"remotely", nil, false},
	} {
		if got := remote(details{WorkLocationOption: c.option, WorkSite: c.site}); got != c.want {
			t.Errorf("%q %q: %v", c.option, c.site, got)
		}
	}
}
