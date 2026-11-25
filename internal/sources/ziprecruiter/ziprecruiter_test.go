package ziprecruiter

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/mcphttp"
	"github.com/bharathvbcr/Vani/internal/sources/rowstest"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func TestMain(m *testing.M) { os.Exit(srcutil.RunWithoutWaits(m)) }

var fixedNow = time.Date(2026, 11, 24, 23, 40, 0, 0, time.UTC)

var board = sources.Board{ATS: posting.ZipRecruiter, Token: Token, Company: "ZipRecruiter",
	Search: "software engineer", Filters: map[string][]string{"location": {"Austin, TX"}, "country": {"US"}}}

// fake is the MCP endpoint: it answers initialize and the initialized
// notification, and hands every tools/call's arguments to call. Any other
// request, or a tool other than search_jobs, fails the test.
type fake struct {
	t    *testing.T
	call func(args map[string]any) srctest.Reply // the reply's id is set by fake

	initReply *srctest.Reply

	mu    sync.Mutex
	calls []map[string]any
}

func (f *fake) doer() *srctest.Doer {
	return srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method != http.MethodPost || r.URL != Endpoint {
			f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
			return srctest.Reply{}, false
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal(r.Body, &msg); err != nil {
			f.t.Errorf("request body is not JSON: %v", err)
			return srctest.Reply{}, false
		}
		switch msg.Method {
		case "initialize":
			if f.initReply != nil {
				return *f.initReply, true
			}
			return jsonReply(`{"jsonrpc":"2.0","id":` + string(msg.ID) + `,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{"listChanged":false}},"serverInfo":{"name":"jobseeker-mcp","version":"1.0.0"}}}`), true
		case "notifications/initialized":
			return srctest.Reply{Status: http.StatusAccepted}, true
		case "tools/call":
			if msg.Params.Name != "search_jobs" {
				f.t.Errorf("called tool %q", msg.Params.Name)
				return srctest.Reply{}, false
			}
			f.mu.Lock()
			f.calls = append(f.calls, msg.Params.Arguments)
			f.mu.Unlock()
			rep := f.call(msg.Params.Arguments)
			rep.Body = withID(f.t, rep.Body, msg.ID)
			return rep, true
		}
		f.t.Errorf("unexpected method %q", msg.Method)
		return srctest.Reply{}, false
	})
}

func (f *fake) args() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.calls...)
}

// withID sets a JSON-RPC reply's id to the request's; a body that is not a
// JSON object is returned as is.
func withID(t *testing.T, body []byte, id json.RawMessage) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	m["id"] = id
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func jsonReply(body string) srctest.Reply {
	return srctest.Reply{Status: 200, Body: []byte(body), Header: http.Header{"Content-Type": {"application/json"}}}
}

// result wraps a structuredContent object as a tools/call reply.
func result(sc string) srctest.Reply {
	return jsonReply(`{"jsonrpc":"2.0","id":0,"result":{"content":[{"type":"text","text":"x"}],"structuredContent":` + sc + `}}`)
}

// page is a structuredContent with status, total and rows.
func page(status string, total int, rows []string) string {
	return fmt.Sprintf(`{"request":{"job_role":"x","filters":{}},"results":[%s],"status":%q,"meta":{"count":%d,"limit":5,"total":%d},"warnings":[],"conversation_id":"c"}`,
		strings.Join(rows, ","), status, len(rows), total)
}

// key is a 22-character base64url listing key for name.
func key(name string) string {
	sum := sha256.Sum256([]byte(name))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

// zrRow is one valid row for the listing key k.
func zrRow(k, title string) string {
	return fmt.Sprintf(`{"listing_key":%q,"title":%q,"company":"Acme Corp","location":"Austin, Texas","is_remote":false,"salary":{"min_annual":100000,"max_annual":120000},"company_logo":null,"job_redirect_url":"https://www.ziprecruiter.com/job-redirect?match_token=abc%s","job_type":"Full-time · On-site","benefits":"Medical","days_ago":3,"application_status":null,"is_external":false}`,
		k, title, k)
}

// fixture reads a captured search_jobs reply.
func fixture(t *testing.T, name string) srctest.Reply {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return jsonReply(string(raw))
}

func offsetOf(args map[string]any) int {
	if v, ok := args["offset"].(float64); ok {
		return int(v)
	}
	return 0
}

// window answers every term with an endless listing of distinct rows, five a
// call, named by the term and the row's position.
func window(args map[string]any) srctest.Reply {
	term, _ := args["job_role"].(string)
	off := offsetOf(args)
	var rows []string
	for i := off; i < off+PageSize; i++ {
		rows = append(rows, zrRow(key(term+strconv.Itoa(i)), fmt.Sprintf("%s %d", term, i)))
	}
	return result(page("OK", 900, rows))
}

type waits struct {
	mu sync.Mutex
	d  []time.Duration
}

func (w *waits) wait(ctx context.Context, d time.Duration) error {
	w.mu.Lock()
	w.d = append(w.d, d)
	w.mu.Unlock()
	return ctx.Err()
}

type reports struct {
	n     int
	stats Stats
}

func newSource(t *testing.T, f *fake, w *waits, rep *reports) *source {
	t.Helper()
	if w == nil {
		w = &waits{}
	}
	o := Options{Wait: w.wait}
	if rep != nil {
		o.Report = func(b sources.Board, st Stats) {
			if b.Token != Token {
				t.Errorf("report for board %q", b.Token)
			}
			rep.n++
			rep.stats = st
		}
	}
	src, err := NewWith(f.doer(), o)
	if err != nil {
		t.Fatal(err)
	}
	s := src.(*source)
	s.now = func() time.Time { return fixedNow }
	return s
}

func TestFixture(t *testing.T) {
	s0, s25, s30 := fixture(t, "search_s0.json"), fixture(t, "search_s25.json"), fixture(t, "search_s30.json")
	f := &fake{t: t, call: func(args map[string]any) srctest.Reply {
		switch offsetOf(args) {
		case 0:
			return s0
		case 5:
			return s25 // the rows offset 25 served live
		case 10:
			return s30 // offset 30 served offset 25's rows again
		}
		t.Errorf("unexpected offset in %v", args)
		return srctest.Reply{Status: 500}
	}}
	var rep reports
	got, err := newSource(t, f, nil, &rep).List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []map[string]any{
		{"job_role": "software engineer", "location": "Austin, TX", "country_admin_code": "US"},
		{"job_role": "software engineer", "location": "Austin, TX", "country_admin_code": "US", "offset": float64(5)},
		{"job_role": "software engineer", "location": "Austin, TX", "country_admin_code": "US", "offset": float64(10)},
	}
	if a := f.args(); !reflect.DeepEqual(a, wantArgs) {
		t.Fatalf("arguments %v, want %v (the repeat at offset 10 ends the term)", a, wantArgs)
	}
	if len(got) != 10 {
		t.Fatalf("got %d postings, want the 10 distinct rows", len(got))
	}
	if rep.n != 1 || rep.stats != (Stats{Listed: 10, Requests: 3, Matched: 920, Windowed: true}) {
		t.Fatalf("report %+v", rep)
	}
	byTitle := map[string]posting.Posting{}
	for _, p := range got {
		byTitle[p.Title] = p
		if p.ATS != posting.ZipRecruiter || !strings.HasPrefix(string(p.Key), board.KeyPrefix()) || !p.SeenAt.Equal(fixedNow) {
			t.Errorf("posting %+v", p)
		}
		for _, field := range []string{p.URL, p.ApplyURL, p.Description, p.Location, p.Company} {
			if strings.Contains(field, "match_token") || strings.Contains(field, "job-redirect") || strings.Contains(field, "days") {
				t.Errorf("posting %q carries a per-call or relative field: %q", p.Title, field)
			}
		}
		if p.ApplyURL != "" {
			t.Errorf("posting %q apply url %q, want none", p.Title, p.ApplyURL)
		}
	}
	rk, _ := posting.ZipRecruiterKey(Token, "MBgmmSHgNLagbiJrIbkQMQ")
	want := posting.Posting{
		Key:         rk,
		ATS:         posting.ZipRecruiter,
		Company:     "Raytheon",
		Title:       "Software Engineer II - Test Solutions Center",
		Location:    "Austin, Texas",
		URL:         "https://www.ziprecruiter.com/jobs//j?lvk=MBgmmSHgNLagbiJrIbkQMQ",
		PostedAt:    time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC),
		Description: "Pay range: USD 68900 – 131100 per year\nRole: full-time, on-site",
		SeenAt:      fixedNow,
	}
	if p := byTitle[want.Title]; !reflect.DeepEqual(p, want) {
		t.Errorf("got  %+v\nwant %+v", p, want)
	}
	da := byTitle["Software Engineer (part-time work from home) - AI Trainer"]
	if !da.Remote || da.Company != "DataAnnotation" || !da.PostedAt.Equal(time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)) ||
		da.Description != "Pay range: USD 80000 – 250000 per year\nRole: contract, remote, remote optional" {
		t.Errorf("DataAnnotation row: %+v", da)
	}
	soft := byTitle["Software Engineer (Rust , Node.JS & GraphQL)"]
	if soft.Description != "Pay range: USD 121400 – 160000 per year\nRole: on-site" || soft.URL != "https://www.ziprecruiter.com/jobs//j?lvk=bNg2_XGCZZdoM8hkO2DV2Q" {
		t.Errorf("SoftPath row: %+v", soft)
	}
	if mid := byTitle["Mid-Level Software Engineer - Remote (Austin, TX)"]; mid.Description != "Pay range: USD 110000 – 130000 per year\nRole: full-time, remote" {
		t.Errorf("Trophy Scan row description %q", mid.Description)
	}
	// A description made only of lead lines has no board text to compare.
	for _, p := range got {
		if st := srcutil.SourceText(p.Description); st != "" {
			t.Errorf("posting %q source text %q, want none", p.Title, st)
		}
	}
}

func TestWindowIsSixCalls(t *testing.T) {
	f := &fake{t: t, call: window}
	var rep reports
	w := &waits{}
	got, err := newSource(t, f, w, &rep).List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	var offs []int
	for _, a := range f.args() {
		offs = append(offs, offsetOf(a))
	}
	if !reflect.DeepEqual(offs, []int{0, 5, 10, 15, 20, 25}) || len(got) != 30 {
		t.Fatalf("offsets %v, %d postings; want 0..25 and 30", offs, len(got))
	}
	if rep.n != 1 || !rep.stats.Windowed || rep.stats.Listed != 30 || rep.stats.Requests != 6 || rep.stats.Matched != 900 {
		t.Fatalf("report %+v", rep)
	}
	// The handshake goes first; every call waits at least a second.
	if len(w.d) != 6 {
		t.Fatalf("waits %v, want one before each of the 6 calls", w.d)
	}
	for _, d := range w.d {
		if d < time.Second {
			t.Fatalf("wait %v, under a second", d)
		}
	}
}

func TestPagingEndsEarly(t *testing.T) {
	rows := func(names ...string) []string {
		var out []string
		for _, n := range names {
			out = append(out, zrRow(key(n), n))
		}
		return out
	}
	cases := []struct {
		name  string
		pages map[int][]string
		calls int
		want  int
	}{
		{"short page", map[int][]string{0: rows("a", "b", "c", "d", "e"), 5: rows("f", "g")}, 2, 7},
		{"empty page", map[int][]string{0: rows("a", "b", "c", "d", "e"), 5: nil}, 2, 5},
		{"repeat only", map[int][]string{0: rows("a", "b", "c", "d", "e"), 5: rows("a", "b", "c", "d", "e")}, 2, 5},
		{"shifted page", map[int][]string{0: rows("a", "b", "c", "d", "e"), 5: rows("d", "e", "f", "g", "h")}, 2, 8},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{t: t, call: func(args map[string]any) srctest.Reply {
				return result(page("OK", 50, c.pages[offsetOf(args)]))
			}}
			got, err := newSource(t, f, nil, nil).List(context.Background(), board)
			if err != nil {
				t.Fatal(err)
			}
			if len(f.args()) != c.calls || len(got) != c.want {
				t.Fatalf("%d calls, %d postings; want %d and %d", len(f.args()), len(got), c.calls, c.want)
			}
		})
	}
}

func TestTermsUnion(t *testing.T) {
	b := board
	b.Search = "go developer"
	b.Searches = []string{"Go Developer", "backend engineer"} // a case-insensitive repeat is one term
	shared := zrRow(key("shared"), "Shared role")
	f := &fake{t: t, call: func(args map[string]any) srctest.Reply {
		switch args["job_role"] {
		case "go developer":
			return result(page("OK", 40, []string{zrRow(key("g1"), "Go 1"), shared}))
		case "backend engineer":
			return result(page("OK", 60, []string{shared, zrRow(key("b1"), "Backend 1"), zrRow(key("b2"), "Backend 2")}))
		}
		t.Errorf("unexpected arguments %v", args)
		return srctest.Reply{Status: 500}
	}}
	var rep reports
	got, err := newSource(t, f, nil, &rep).List(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, p := range got {
		titles = append(titles, p.Title)
	}
	if !reflect.DeepEqual(titles, []string{"Go 1", "Shared role", "Backend 1", "Backend 2"}) {
		t.Fatalf("titles %v", titles)
	}
	if rep.stats != (Stats{Listed: 4, Requests: 2, Matched: 100, Windowed: true}) {
		t.Fatalf("stats %+v", rep.stats)
	}
}

func TestFiltersMapped(t *testing.T) {
	b := board
	b.Filters = map[string][]string{
		"location":           {" Toronto, ON "},
		"country":            {"ca"},
		"radius_miles":       {"25"},
		"posted_within_days": {"3"},
		"location_type":      {"Remote", "HYBRID"},
		"employment_type":    {"full_time", "contract-to-hire"},
		"seniority":          {"senior", "mid"},
	}
	f := &fake{t: t, call: func(map[string]any) srctest.Reply {
		return result(page("OK", 2, []string{zrRow(key("x"), "X")}))
	}}
	got, err := newSource(t, f, nil, nil).List(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"job_role":               "software engineer",
		"location":               "Toronto, ON",
		"country_admin_code":     "CA",
		"radius_miles":           float64(25),
		"max_posted_minutes_ago": float64(4320),
		"location_types":         []any{"HYBRID", "REMOTE"},
		"employment_types":       []any{"CONTRACT_TO_HIRE", "FULL_TIME"},
		"seniority_classes":      []any{"MID", "SENIOR"},
	}
	if a := f.args(); len(a) != 1 || !reflect.DeepEqual(a[0], want) {
		t.Fatalf("arguments %v, want %v", a, want)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0].Description, "Pay range: CAD 100000 – 120000 per year\n") {
		t.Fatalf("postings %+v; want a CAD pay line for a CA board", got)
	}
}

func TestNoCountryNoPayLine(t *testing.T) {
	b := board
	b.Filters = nil
	f := &fake{t: t, call: func(args map[string]any) srctest.Reply {
		return result(page("OK", 1, []string{zrRow(key("x"), "X")}))
	}}
	got, err := newSource(t, f, nil, nil).List(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if a := f.args(); !reflect.DeepEqual(a, []map[string]any{{"job_role": "software engineer"}}) {
		t.Fatalf("arguments %v", a)
	}
	if len(got) != 1 || got[0].Description != "Role: full-time, on-site" {
		t.Fatalf("postings %+v; want no pay line without a currency", got)
	}
}

func TestInvalidBoards(t *testing.T) {
	with := func(mod func(b *sources.Board)) sources.Board {
		b := board
		b.Filters = map[string][]string{}
		for k, v := range board.Filters {
			b.Filters[k] = v
		}
		mod(&b)
		return b
	}
	filter := func(k string, vs ...string) sources.Board {
		return with(func(b *sources.Board) { b.Filters[k] = vs })
	}
	cases := map[string]sources.Board{
		"no search":                 with(func(b *sources.Board) { b.Search = " " }),
		"too many searches":         with(func(b *sources.Board) { b.Searches = strings.Split("a b c d e f g h i j k", " ") }),
		"long search":               with(func(b *sources.Board) { b.Search = strings.Repeat("x", 101) }),
		"other token":               with(func(b *sources.Board) { b.Token = "zr" }),
		"other ats":                 with(func(b *sources.Board) { b.ATS = posting.Dice }),
		"host":                      with(func(b *sources.Board) { b.Host = "api.ziprecruiter.com" }),
		"max postings":              with(func(b *sources.Board) { b.MaxPostings = 10 }),
		"unknown filter":            filter("salary_min", "100000"),
		"conversation id":           filter("conversation_id", "x"),
		"empty filter":              filter("location"),
		"blank value":               filter("location", "  "),
		"control character":         filter("location", "Austin\x00"),
		"long value":                filter("location", strings.Repeat("x", 101)),
		"two locations":             filter("location", "Austin, TX", "Dallas, TX"),
		"two countries":             filter("country", "US", "CA"),
		"country gb":                filter("country", "GB"),
		"country name":              filter("country", "United States"),
		"radius zero":               filter("radius_miles", "0"),
		"radius over":               filter("radius_miles", "501"),
		"radius negative":           filter("radius_miles", "-5"),
		"radius signed":             filter("radius_miles", "+5"),
		"radius fraction":           filter("radius_miles", "2.5"),
		"radius words":              filter("radius_miles", "ten"),
		"radius no location":        with(func(b *sources.Board) { delete(b.Filters, "location"); b.Filters["radius_miles"] = []string{"10"} }),
		"days zero":                 filter("posted_within_days", "0"),
		"days over":                 filter("posted_within_days", "31"),
		"days huge":                 filter("posted_within_days", "99999999999999999999"),
		"location type unknown":     filter("location_type", "onsite"),
		"location type unspecified": filter("location_type", "location_type_unspecified"),
		"location type repeated":    filter("location_type", "remote", "REMOTE"),
		"employment unknown":        filter("employment_type", "fulltime"),
		"employment repeated":       filter("employment_type", "full_time", "full-time"),
		"seniority unknown":         filter("seniority", "principal"),
		"seniority unspecified":     filter("seniority", "SENIORITY_CLASS_UNSPECIFIED"),
		"too many values":           filter("seniority", "junior", "mid", "senior", "no_experience", "junior", "mid", "senior", "junior", "mid", "senior", "mid"),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fake{t: t, call: window}
			d := f.doer()
			src, _ := NewWith(d, Options{Wait: (&waits{}).wait})
			if got, err := src.List(context.Background(), b); err == nil {
				t.Fatalf("listed %d postings", len(got))
			}
			if n := len(d.Requests()); n != 0 {
				t.Fatalf("%d requests sent for an invalid board", n)
			}
		})
	}
	if err := CheckFilters(map[string][]string{"seniority": {"Senior"}, "location_type": {"remote"}}); err != nil {
		t.Fatalf("CheckFilters: %v", err)
	}
	if err := CheckFilters(map[string][]string{"salary_max": {"1"}}); err == nil {
		t.Fatal("CheckFilters accepted salary_max")
	}
}

func TestStatuses(t *testing.T) {
	for _, status := range []string{"ERROR", "UNSUPPORTED_COUNTRY", "UNSUPPORTED_CONSTRAINTS"} {
		t.Run(status, func(t *testing.T) {
			f := &fake{t: t, call: func(map[string]any) srctest.Reply {
				return result(`{"request":{"filters":{}},"results":[],"status":"` + status + `","errorMessage":"Bad\u0007 thing\n\nhappened","meta":{"count":0,"limit":5,"total":0},"warnings":[],"conversation_id":"c"}`)
			}}
			var rep reports
			got, err := newSource(t, f, nil, &rep).List(context.Background(), board)
			if !errors.Is(err, ErrStatus) || got != nil {
				t.Fatalf("got %d postings, %v; want ErrStatus", len(got), err)
			}
			if !strings.Contains(err.Error(), status) || !strings.Contains(err.Error(), "Bad thing happened") {
				t.Fatalf("error %q does not name the status and its cleaned message", err)
			}
			if rep.n != 0 {
				t.Fatalf("a board that failed while listing was reported: %+v", rep)
			}
		})
	}
	t.Run("unknown status", func(t *testing.T) {
		f := &fake{t: t, call: func(map[string]any) srctest.Reply { return result(page("PARTIAL", 5, nil)) }}
		if _, err := newSource(t, f, nil, nil).List(context.Background(), board); !errors.Is(err, ErrShape) {
			t.Fatalf("err %v, want ErrShape", err)
		}
	})
}

func TestNoResults(t *testing.T) {
	f := &fake{t: t, call: func(map[string]any) srctest.Reply { return result(page("NO_RESULTS", 0, nil)) }}
	var rep reports
	got, err := newSource(t, f, nil, &rep).List(context.Background(), board)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %d postings, %v; want an empty listing", len(got), err)
	}
	if len(f.args()) != 1 || rep.n != 1 || rep.stats != (Stats{Requests: 1, Windowed: true}) {
		t.Fatalf("%d calls, report %+v", len(f.args()), rep)
	}
	// NO_RESULTS on a later page ends the term.
	f = &fake{t: t, call: func(args map[string]any) srctest.Reply {
		if offsetOf(args) == 0 {
			return window(args)
		}
		return result(page("NO_RESULTS", 0, nil))
	}}
	if got, err := newSource(t, f, nil, nil).List(context.Background(), board); err != nil || len(got) != 5 || len(f.args()) != 2 {
		t.Fatalf("got %d postings in %d calls, %v", len(got), len(f.args()), err)
	}
}

func TestShapeErrors(t *testing.T) {
	r := zrRow(key("a"), "A")
	cases := map[string]string{
		"no results with a total":    page("NO_RESULTS", 12, nil),
		"no results with rows":       `{"results":[` + r + `],"status":"NO_RESULTS","meta":{"count":0,"limit":5,"total":0}}`,
		"empty first page of a hit":  page("OK", 12, nil),
		"results missing":            `{"status":"OK","meta":{"count":0,"limit":5,"total":0}}`,
		"results null":               `{"results":null,"status":"OK","meta":{"count":0,"limit":5,"total":0}}`,
		"meta missing":               `{"results":[` + r + `],"status":"OK"}`,
		"count missing":              `{"results":[` + r + `],"status":"OK","meta":{"limit":5,"total":3}}`,
		"total missing":              `{"results":[` + r + `],"status":"OK","meta":{"count":1,"limit":5}}`,
		"count mismatch":             `{"results":[` + r + `],"status":"OK","meta":{"count":2,"limit":5,"total":3}}`,
		"negative total":             `{"results":[` + r + `],"status":"OK","meta":{"count":1,"limit":5,"total":-1}}`,
		"fractional total":           `{"results":[` + r + `],"status":"OK","meta":{"count":1,"limit":5,"total":2.5}}`,
		"too many rows":              page("OK", 50, []string{r, r, r, r, r, r}),
		"wrong type for a row field": `{"results":[{"listing_key":5}],"status":"OK","meta":{"count":1,"limit":5,"total":1}}`,
		"structuredContent array":    `[]`,
	}
	for name, sc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fake{t: t, call: func(map[string]any) srctest.Reply { return result(sc) }}
			got, err := newSource(t, f, nil, nil).List(context.Background(), board)
			if err == nil || got != nil {
				t.Fatalf("got %d postings, %v; want an error", len(got), err)
			}
			if !errors.Is(err, ErrShape) && !errors.Is(err, mcphttp.ErrProtocol) {
				t.Fatalf("err %v, want ErrShape or mcphttp.ErrProtocol", err)
			}
		})
	}
	t.Run("empty board", func(t *testing.T) {
		f := &fake{t: t, call: func(map[string]any) srctest.Reply { return result(page("OK", 0, nil)) }}
		if got, err := newSource(t, f, nil, nil).List(context.Background(), board); err != nil || len(got) != 0 {
			t.Fatalf("got %d postings, %v", len(got), err)
		}
	})
}

func TestToolError(t *testing.T) {
	f := &fake{t: t, call: func(map[string]any) srctest.Reply {
		return jsonReply(`{"jsonrpc":"2.0","id":0,"result":{"content":[{"type":"text","text":"Invalid enum value. Expected 'PHYSICAL' | 'REMOTE'"}],"isError":true}}`)
	}}
	got, err := newSource(t, f, nil, nil).List(context.Background(), board)
	var te *mcphttp.ToolError
	if !errors.As(err, &te) || te.Tool != "search_jobs" || got != nil {
		t.Fatalf("got %d postings, %v; want a ToolError", len(got), err)
	}
}

func TestRateLimited(t *testing.T) {
	limited := srctest.Reply{Status: http.StatusTooManyRequests, Body: []byte("slow down"), Header: http.Header{"Retry-After": {"1"}}}
	t.Run("search", func(t *testing.T) {
		f := &fake{t: t, call: func(map[string]any) srctest.Reply { return limited }}
		got, err := newSource(t, f, nil, nil).List(context.Background(), board)
		var rl *srcutil.RateLimitedError
		if !errors.As(err, &rl) || !errors.Is(err, srcutil.ErrRateLimited) || got != nil {
			t.Fatalf("got %d postings, %v; want a RateLimitedError", len(got), err)
		}
	})
	t.Run("later page", func(t *testing.T) {
		f := &fake{t: t, call: func(args map[string]any) srctest.Reply {
			if offsetOf(args) == 0 {
				return window(args)
			}
			return limited
		}}
		if got, err := newSource(t, f, nil, nil).List(context.Background(), board); !errors.Is(err, srcutil.ErrRateLimited) || got != nil {
			t.Fatalf("got %d postings, %v; want the board to fail, not list one page", len(got), err)
		}
	})
	t.Run("initialize", func(t *testing.T) {
		f := &fake{t: t, call: window, initReply: &limited}
		if got, err := newSource(t, f, nil, nil).List(context.Background(), board); !errors.Is(err, srcutil.ErrRateLimited) || got != nil {
			t.Fatalf("got %d postings, %v", len(got), err)
		}
	})
}

func TestEndpointGone(t *testing.T) {
	gone := srctest.Reply{Status: http.StatusNotFound, Body: []byte("not found")}
	for name, f := range map[string]*fake{
		"initialize": {t: t, call: window, initReply: &gone},
		"search":     {t: t, call: func(map[string]any) srctest.Reply { return gone }},
	} {
		t.Run(name, func(t *testing.T) {
			f.t = t
			got, err := newSource(t, f, nil, nil).List(context.Background(), board)
			if !errors.Is(err, mcphttp.ErrEndpointGone) || errors.Is(err, srcutil.ErrBoardNotFound) || got != nil {
				t.Fatalf("got %d postings, %v; want ErrEndpointGone and not ErrBoardNotFound", len(got), err)
			}
		})
	}
}

func TestGarbageReplies(t *testing.T) {
	cases := map[string]srctest.Reply{
		"not json":    jsonReply("<html>oops</html>"),
		"empty":       jsonReply(""),
		"batch":       jsonReply("[]"),
		"no result":   jsonReply(`{"jsonrpc":"2.0","id":0}`),
		"server 500":  {Status: 500, Body: []byte("boom")},
		"oversized":   jsonReply(`{"jsonrpc":"2.0","id":0,"result":{"structuredContent":{"x":"` + strings.Repeat("a", mcphttp.MaxReply) + `"}}}`),
		"rpc error":   jsonReply(`{"jsonrpc":"2.0","id":0,"error":{"code":-32602,"message":"bad params"}}`),
		"transport":   {Err: errors.New("connection reset")},
		"sse garbage": {Status: 200, Body: []byte("data: nope\n\n"), Header: http.Header{"Content-Type": {"text/event-stream"}}},
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fake{t: t, call: func(map[string]any) srctest.Reply { return reply }}
			if got, err := newSource(t, f, nil, nil).List(context.Background(), board); err == nil || got != nil {
				t.Fatalf("got %d postings, %v; want an error", len(got), err)
			}
		})
	}
}

func TestCanceled(t *testing.T) {
	t.Run("before", func(t *testing.T) {
		f := &fake{t: t, call: window}
		d := f.doer()
		src, _ := NewWith(d, Options{Wait: (&waits{}).wait})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := src.List(ctx, board); !errors.Is(err, context.Canceled) || len(d.Requests()) != 0 {
			t.Fatalf("err %v after %d requests", err, len(d.Requests()))
		}
	})
	t.Run("during", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f := &fake{t: t, call: func(args map[string]any) srctest.Reply {
			cancel()
			return window(args)
		}}
		got, err := newSource(t, f, nil, nil).List(ctx, board)
		if !errors.Is(err, context.Canceled) || got != nil || len(f.args()) != 1 {
			t.Fatalf("got %d postings in %d calls, %v", len(got), len(f.args()), err)
		}
	})
}

func TestDeterministic(t *testing.T) {
	raw, err := os.ReadFile("testdata/search_s0.json")
	if err != nil {
		t.Fatal(err)
	}
	// The next day: every days_ago one more, every match_token new.
	var next map[string]any
	if err := json.Unmarshal(raw, &next); err != nil {
		t.Fatal(err)
	}
	for _, r := range next["result"].(map[string]any)["structuredContent"].(map[string]any)["results"].([]any) {
		row := r.(map[string]any)
		row["days_ago"] = row["days_ago"].(float64) + 1
		row["job_redirect_url"] = "https://www.ziprecruiter.com/job-redirect?match_token=NEWTOKEN&tsid=1"
	}
	nextRaw, _ := json.Marshal(next)
	list := func(body []byte, now time.Time) []posting.Posting {
		t.Helper()
		f := &fake{t: t, call: func(args map[string]any) srctest.Reply {
			if offsetOf(args) == 0 {
				return jsonReply(string(body))
			}
			return result(page("OK", 920, nil))
		}}
		s := newSource(t, f, nil, nil)
		s.now = func() time.Time { return now }
		got, err := s.List(context.Background(), board)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	day1 := list(raw, fixedNow)
	day2 := list(nextRaw, time.Date(2026, 11, 25, 9, 15, 0, 0, time.UTC))
	if len(day1) != 5 || len(day2) != 5 {
		t.Fatalf("%d and %d postings", len(day1), len(day2))
	}
	for i := range day1 {
		a, b := day1[i], day2[i]
		a.SeenAt, b.SeenAt = time.Time{}, time.Time{}
		if !reflect.DeepEqual(a, b) {
			t.Errorf("day 1 %+v\nday 2 %+v", a, b)
		}
	}
}

func TestPostedAt(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	day := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		days *float64
		want time.Time
	}{
		{nil, time.Time{}},
		{f(0), day},
		{f(2.7), day.AddDate(0, 0, -2)},
		{f(365), day.AddDate(0, 0, -365)},
		{f(366), time.Time{}},
		{f(-1), time.Time{}},
	} {
		if got := (row{DaysAgo: c.days}).postedAt(fixedNow.In(time.FixedZone("x", 5*3600))); !got.Equal(c.want) {
			t.Errorf("days_ago %v: %v, want %v", c.days, got, c.want)
		}
	}
}

func TestBadRows(t *testing.T) {
	rowstest.Check(t, rowstest.Case{
		List: func(ctx context.Context, payload string) ([]posting.Posting, error) {
			var rows []json.RawMessage
			if err := json.Unmarshal([]byte(payload), &rows); err != nil {
				t.Fatal(err)
			}
			f := &fake{t: t, call: func(args map[string]any) srctest.Reply {
				off := offsetOf(args)
				var pg []string
				for i := off; i < min(off+PageSize, len(rows)); i++ {
					pg = append(pg, string(rows[i]))
				}
				return result(page("OK", len(rows), pg))
			}}
			return newSource(t, f, nil, nil).List(ctx, board)
		},
		Payload: func(n int, bad map[int]string) string {
			rows := make([]string, n)
			for i := range rows {
				rows[i] = zrRow(key(strconv.Itoa(i)), "Role "+strconv.Itoa(i))
				if raw, ok := bad[i]; ok {
					rows[i] = raw
				}
			}
			return "[" + strings.Join(rows, ",") + "]"
		},
		BadRows: map[string]string{
			"missing listing_key": `{"title":"T ` + rowstest.Index + `","company":"C","location":"L","is_remote":false}`,
			"bad base64 key":      `{"listing_key":"not base64!` + rowstest.Index + `","title":"T","company":"C"}`,
			"over-long key":       `{"listing_key":"` + strings.Repeat("A", 120) + rowstest.Index + `","title":"T","company":"C"}`,
			"empty title":         `{"listing_key":"` + key("t") + rowstest.Index + `","title":"  ","company":"C"}`,
			"empty company":       `{"listing_key":"` + key("c") + rowstest.Index + `","title":"T","company":"\t"}`,
		},
	})
}

func TestNewWith(t *testing.T) {
	if _, err := NewWith(nil, Options{}); err == nil {
		t.Fatal("NewWith accepted a nil doer")
	}
	if s := New(srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) { return srctest.Reply{}, false })); s.ATS() != posting.ZipRecruiter {
		t.Fatalf("ATS %q", s.ATS())
	}
	if MaxPostings != 30*sources.MaxSearches {
		t.Fatalf("MaxPostings %d", MaxPostings)
	}
}
