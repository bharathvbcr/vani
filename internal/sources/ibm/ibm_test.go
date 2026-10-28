package ibm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

var fixedNow = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func ibmBoard(terms ...string) sources.Board {
	return sources.Board{ATS: posting.IBM, Token: "ibm", Company: "IBM", Searches: terms}
}

func confluentBoard(terms ...string) sources.Board {
	return sources.Board{ATS: posting.IBM, Token: "confluent", Company: "Confluent", Searches: terms}
}

// fixtureHits are three real hits (two Confluent's, one IBM's), their bodies
// cut to 1,500 characters: testdata/search.json, from the live API on
// 2026-10-27.
func fixtureHits(t *testing.T) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/search.json")
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Hits struct {
			Hits []map[string]any `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Hits.Hits) != 3 {
		t.Fatalf("fixture has %d hits", len(r.Hits.Hits))
	}
	return r.Hits.Hits
}

// clone is a hit with fields of its _source changed.
func clone(hit map[string]any, set map[string]any) map[string]any {
	src := map[string]any{}
	for k, v := range hit["_source"].(map[string]any) {
		src[k] = v
	}
	for k, v := range set {
		if v == nil {
			delete(src, k)
			continue
		}
		src[k] = v
	}
	return map[string]any{"_id": "x", "_source": src}
}

// numbered is the IBM fixture hit as job number n, titled title.
func numbered(t *testing.T, n int, title string) map[string]any {
	return clone(fixtureHits(t)[2], map[string]any{
		"field_text_01": n, "title": title,
		"url": fmt.Sprintf("https://careers.ibm.com/careers/JobDetail?jobId=%d", n),
	})
}

func reply(total int, relation string, hits []map[string]any) string {
	if hits == nil {
		hits = []map[string]any{}
	}
	raw, _ := json.Marshal(map[string]any{"took": 3, "timed_out": false,
		"hits": map[string]any{"total": map[string]any{"value": total, "relation": relation}, "hits": hits}})
	return string(raw)
}

// searchReq is a request body as the API reads it.
type searchReq struct {
	AppID  string   `json:"appId"`
	Scopes []string `json:"scopes"`
	Query  struct {
		Bool struct {
			Must []struct {
				SQS struct {
					Query           string   `json:"query"`
					Fields          []string `json:"fields"`
					DefaultOperator string   `json:"default_operator"`
				} `json:"simple_query_string"`
			} `json:"must"`
			Filter []struct {
				Term map[string]string `json:"term"`
			} `json:"filter"`
		} `json:"bool"`
	} `json:"query"`
	From   int      `json:"from"`
	Size   int      `json:"size"`
	Sort   []string `json:"sort"`
	Source []string `json:"_source"`
}

// handler answers each request from jobs[term], by from and size, with the
// exact total len(jobs[term]) unless totals overrides it. Every request is
// checked against the verified request shape.
func handler(t *testing.T, jobs map[string][]map[string]any, totals map[string]int) func(srctest.Request) (srctest.Reply, bool) {
	return func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method != http.MethodPost || r.URL != "https://www-api.ibm.com/search/api/v2" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			return srctest.Reply{}, false
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type %q", ct)
		}
		var q searchReq
		if err := json.Unmarshal(r.Body, &q); err != nil {
			t.Errorf("request body: %v", err)
			return srctest.Reply{}, false
		}
		must := q.Query.Bool.Must
		if q.AppID != "careers" || !reflect.DeepEqual(q.Scopes, []string{"careers2"}) || q.Size != 100 ||
			!reflect.DeepEqual(q.Sort, []string{"_id"}) || !reflect.DeepEqual(q.Source, sourceFields) ||
			len(must) != 1 || !reflect.DeepEqual(must[0].SQS.Fields, []string{"title"}) || must[0].SQS.DefaultOperator != "and" ||
			len(q.Query.Bool.Filter) != 1 || !reflect.DeepEqual(q.Query.Bool.Filter[0].Term, map[string]string{"field_keyword_05": "United States"}) {
			t.Errorf("request is not the verified shape: %s", r.Body)
			return srctest.Reply{}, false
		}
		term := must[0].SQS.Query
		all := jobs[term]
		total := len(all)
		if n, ok := totals[term]; ok {
			total = n
		}
		lo := min(q.From, len(all))
		hi := min(q.From+q.Size, len(all))
		return srctest.Reply{Status: 200, Body: []byte(reply(total, "eq", all[lo:hi]))}, true
	}
}

func newSource(h func(srctest.Request) (srctest.Reply, bool)) (*source, *srctest.Doer) {
	d := srctest.NewFunc(h)
	return &source{d: d, now: func() time.Time { return fixedNow }}, d
}

func constant(status int, body string) func(srctest.Request) (srctest.Reply, bool) {
	return func(srctest.Request) (srctest.Reply, bool) {
		return srctest.Reply{Status: status, Body: []byte(body)}, true
	}
}

func keysOf(ps []posting.Posting) []string {
	var out []string
	for _, p := range ps {
		out = append(out, string(p.Key))
	}
	return out
}

// The fixture's first two hits are Confluent's, the third IBM's: each board
// lists only its own, and together they list all three once.
func TestFixtureSplitsBetweenTheBoards(t *testing.T) {
	hits := fixtureHits(t)
	h := handler(t, map[string][]map[string]any{"engineer": hits}, nil)
	s, _ := newSource(h)

	got, err := s.List(context.Background(), ibmBoard("engineer"))
	if err != nil || len(got) != 1 {
		t.Fatalf("ibm board: %d postings, err %v", len(got), err)
	}
	p := got[0]
	if p.Key != "ib:ibm:135532" || p.ATS != posting.IBM || p.Company != "IBM" || p.Title != "Senior Security Engineer" ||
		p.URL != "https://careers.ibm.com/careers/JobDetail?jobId=135532" || p.ApplyURL != "" ||
		!p.PostedAt.Equal(day("2026-10-05")) || !p.UpdatedAt.Equal(day("2026-10-05")) ||
		p.Remote || p.Department != "" || !p.SeenAt.Equal(fixedNow) {
		t.Errorf("ibm posting: %+v", p)
	}
	if p.Location != "Armonk, United States" {
		t.Errorf("location %q: the city list is read before the primary location", p.Location)
	}
	for _, want := range []string{"Role: professional, hybrid\n\n", "Senior Security Engineer, IBM Corporation, Armonk, NY"} {
		if !strings.Contains(p.Description, want) {
			t.Errorf("description lacks %q: %.300s", want, p.Description)
		}
	}

	conf, err := s.List(context.Background(), confluentBoard("engineer"))
	if err != nil || len(conf) != 2 {
		t.Fatalf("confluent board: %d postings, err %v", len(conf), err)
	}
	c := conf[0]
	if c.Key != "ib:confluent:135407" || c.Company != "Confluent" || c.Title != "Director, Engineering - Confluent" ||
		c.Department != "Software Engineering" || !c.Remote || !c.PostedAt.Equal(day("2026-10-13")) ||
		!strings.HasPrefix(c.Location, "Poughkeepsie, United States; Lowell, United States; Rochester, United States; Tucson, United States; Research Triangle Park, United States; Armonk") {
		t.Errorf("confluent posting: %+v", c)
	}
	if !strings.HasPrefix(c.Description, "Role: professional, remote\n\nAt IBM Software, we transform client challenges") ||
		!strings.Contains(c.Description, "Building the world’s leading") || strings.Contains(c.Description, "â€") {
		t.Errorf("description (mangled punctuation not repaired): %.300s", c.Description)
	}
	if conf[1].Key != "ib:confluent:137793" || conf[1].Department != "Sales" {
		t.Errorf("second confluent posting: %+v", conf[1])
	}
	if strings.Contains(conf[1].Location, "No City") {
		t.Errorf("placeholder city kept: %q", conf[1].Location)
	}

	seen := map[string]bool{}
	for _, k := range append(keysOf(got), keysOf(conf)...) {
		id := k[strings.LastIndex(k, ":"):]
		if seen[id] {
			t.Errorf("job %s is on both boards", id)
		}
		seen[id] = true
	}
	if len(seen) != 3 {
		t.Errorf("the boards list %d of the 3 hits", len(seen))
	}
}

// A hit whose title does not name Confluent never reaches the confluent
// board, and one that does never reaches the ibm board, whatever the search.
func TestPartitionByTitleWord(t *testing.T) {
	base := fixtureHits(t)[2]
	titles := []string{"Confluent - Principal Engineer", "Senior Engineer (Confluent)", "CONFLUENT sales", "Staff Engineer - Confluent-Platform",
		"Confluence Administrator", "Confluentist Engineer", "Software Engineer", "Senior Software Developer - Stream Processing"}
	var jobs []map[string]any
	for i, title := range titles {
		jobs = append(jobs, clone(base, map[string]any{"field_text_01": 100 + i, "title": title,
			"url": fmt.Sprintf("https://careers.ibm.com/careers/JobDetail?jobId=%d", 100+i)}))
	}
	s, _ := newSource(handler(t, map[string][]map[string]any{"x": jobs}, nil))
	conf, err := s.List(context.Background(), confluentBoard("x"))
	if err != nil {
		t.Fatal(err)
	}
	own, err := s.List(context.Background(), ibmBoard("x"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ib:confluent:100", "ib:confluent:101", "ib:confluent:102", "ib:confluent:103"}; !reflect.DeepEqual(keysOf(conf), want) {
		t.Errorf("confluent board %v, want %v", keysOf(conf), want)
	}
	if want := []string{"ib:ibm:104", "ib:ibm:105", "ib:ibm:106", "ib:ibm:107"}; !reflect.DeepEqual(keysOf(own), want) {
		t.Errorf("ibm board %v, want %v", keysOf(own), want)
	}
}

func TestConfluentTitle(t *testing.T) {
	for title, want := range map[string]bool{
		"Director, Engineering - Confluent":      true,
		"Confluent - Principal Engineer":         true,
		"(Confluent) Staff Software Engineer II": true,
		"confluent":                              true,
		"Senior Engineer – Confluent":            true,
		"Confluence Administrator":               false,
		"Confluentist":                           false,
		"Conflu ent":                             false,
		"":                                       false,
		"Software Engineer":                      false,
	} {
		if got := confluentTitle(title); got != want {
			t.Errorf("%q: %v, want %v", title, got, want)
		}
	}
}

// Paging by from to the total, the same request shape on every page, and the
// union of terms with each posting once.
func TestPagingAndUnion(t *testing.T) {
	var a, b []map[string]any
	for i := 0; i < 150; i++ {
		a = append(a, numbered(t, 1000+i, "Engineer A"))
	}
	b = append(b, numbered(t, 1000, "Engineer A"), numbered(t, 1149, "Engineer A"), numbered(t, 5000, "Engineer B"))
	s, d := newSource(handler(t, map[string][]map[string]any{"a": a, "b": b}, nil))
	got, err := s.List(context.Background(), ibmBoard("a", "b"))
	if err != nil || len(got) != 151 {
		t.Fatalf("got %d, err %v; want 150 and 1 new", len(got), err)
	}
	var froms []int
	for _, r := range d.Requests() {
		var q searchReq
		if err := json.Unmarshal(r.Body, &q); err != nil {
			t.Fatal(err)
		}
		froms = append(froms, q.From)
	}
	if !reflect.DeepEqual(froms, []int{0, 100, 0}) {
		t.Errorf("from values %v, want 0 and 100 for a, 0 for b", froms)
	}
}

// A total that is an exact multiple of the page size ends on a full page
// without asking for one more.
func TestExactMultipleStopsAtTotal(t *testing.T) {
	var a []map[string]any
	for i := 0; i < 200; i++ {
		a = append(a, numbered(t, 2000+i, "Engineer"))
	}
	s, d := newSource(handler(t, map[string][]map[string]any{"a": a}, nil))
	got, err := s.List(context.Background(), ibmBoard("a"))
	if err != nil || len(got) != 200 || len(d.Requests()) != 2 {
		t.Fatalf("%d postings, %d requests, err %v; want 200 in 2", len(got), len(d.Requests()), err)
	}
}

// A posting that repeats across pages (the listing shifted) is kept once, and
// one skipped posting is within the tolerance; more is not.
func TestShiftedListing(t *testing.T) {
	var a []map[string]any
	for i := 0; i < 150; i++ {
		a = append(a, numbered(t, 3000+i, "Engineer"))
	}
	shifted := append(append([]map[string]any{}, a[:100]...), a[99:149]...) // row 99 again, row 149 never read
	s, _ := newSource(handler(t, map[string][]map[string]any{"a": shifted}, map[string]int{"a": 150}))
	got, err := s.List(context.Background(), ibmBoard("a"))
	if err != nil || len(got) != 149 {
		t.Fatalf("%d postings, err %v; want 149 (one repeat dropped, one lost)", len(got), err)
	}
	lost := append(append([]map[string]any{}, a[:100]...), a[70:120]...) // 30 repeats: 120 distinct of 150
	s, _ = newSource(handler(t, map[string][]map[string]any{"a": lost}, map[string]int{"a": 150}))
	if got, err := s.List(context.Background(), ibmBoard("a")); !errors.Is(err, ErrShape) {
		t.Errorf("30 of 150 postings missing: %d postings, err %v; want ErrShape", len(got), err)
	}
}

func TestZeroTotalIsAnEmptyListing(t *testing.T) {
	s, d := newSource(constant(200, reply(0, "eq", nil)))
	got, err := s.List(context.Background(), ibmBoard("nothing"))
	if err != nil || len(got) != 0 || len(d.Requests()) != 1 {
		t.Errorf("%d postings, %d requests, err %v", len(got), len(d.Requests()), err)
	}
}

// Over the cap fails before any paging, and so does a union over it.
func TestCaps(t *testing.T) {
	s, d := newSource(constant(200, reply(MaxPostings+1, "eq", []map[string]any{numbered(t, 1, "E")})))
	if _, err := s.List(context.Background(), ibmBoard("x")); !errors.Is(err, srcutil.ErrTooManyPostings) || len(d.Requests()) != 1 {
		t.Errorf("total over the cap: err %v after %d requests", err, len(d.Requests()))
	}
	var a, b []map[string]any
	for i := 0; i < MaxPostings; i++ {
		a = append(a, numbered(t, 10000+i, "E"))
	}
	for i := 0; i < 10; i++ {
		b = append(b, numbered(t, 20000+i, "E"))
	}
	s, _ = newSource(handler(t, map[string][]map[string]any{"a": a, "b": b}, nil))
	if _, err := s.List(context.Background(), ibmBoard("a", "b")); !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Errorf("union over the cap: err %v", err)
	}
	s, _ = newSource(handler(t, map[string][]map[string]any{"a": a}, nil))
	if got, err := s.List(context.Background(), ibmBoard("a")); err != nil || len(got) != MaxPostings {
		t.Errorf("exactly the cap: %d postings, err %v", len(got), err)
	}
}

// A listing that never reaches its total ends in an error, not a short list.
func TestIncompleteListingsFail(t *testing.T) {
	one := []map[string]any{numbered(t, 1, "E")}
	var page []map[string]any
	for i := 0; i < 100; i++ {
		page = append(page, numbered(t, 4000+i, "E"))
	}
	for name, tc := range map[string]struct {
		h func(srctest.Request) (srctest.Reply, bool)
	}{
		"empty page before the total": {h: func(r srctest.Request) (srctest.Reply, bool) {
			if strings.Contains(string(r.Body), `"from":0`) {
				return srctest.Reply{Status: 200, Body: []byte(reply(5, "eq", one))}, true
			}
			return srctest.Reply{Status: 200, Body: []byte(reply(5, "eq", nil))}, true
		}},
		"a page of repeats": {h: func(srctest.Request) (srctest.Reply, bool) {
			return srctest.Reply{Status: 200, Body: []byte(reply(MaxPostings, "eq", page))}, true
		}},
		"no hits at all but a total": {h: constant(200, reply(3, "eq", nil))},
	} {
		s, d := newSource(tc.h)
		got, err := s.List(context.Background(), ibmBoard("x"))
		if !errors.Is(err, ErrShape) {
			t.Errorf("%s: %d postings, err %v; want ErrShape", name, len(got), err)
		}
		if len(d.Requests()) > maxPages {
			t.Errorf("%s: %d requests, over the %d page bound", name, len(d.Requests()), maxPages)
		}
	}
}

func TestRefusals(t *testing.T) {
	bad := func(set map[string]any) string {
		return reply(1, "eq", []map[string]any{clone(fixtureHits(t)[2], set)})
	}
	for name, tc := range map[string]struct {
		body string
		want error
	}{
		"lower-bound total":       {body: reply(1, "gte", []map[string]any{numbered(t, 1, "E")}), want: ErrShape},
		"no relation":             {body: reply(1, "", []map[string]any{numbered(t, 1, "E")}), want: ErrShape},
		"no hits object":          {body: `{"took":1}`, want: ErrShape},
		"no total":                {body: `{"hits":{"hits":[]}}`, want: ErrShape},
		"null total value":        {body: `{"hits":{"total":{"value":null,"relation":"eq"},"hits":[]}}`, want: ErrShape},
		"negative total":          {body: reply(-1, "eq", nil), want: ErrShape},
		"hit without _source":     {body: `{"hits":{"total":{"value":1,"relation":"eq"},"hits":[{"_id":"x"}]}}`, want: ErrShape},
		"id missing":              {body: bad(map[string]any{"field_text_01": nil}), want: ErrShape},
		"id not a number":         {body: bad(map[string]any{"field_text_01": "abc"})}, // refused by the decoder
		"id with a sign":          {body: bad(map[string]any{"field_text_01": -5}), want: ErrShape},
		"fractional id":           {body: bad(map[string]any{"field_text_01": 1.5}), want: ErrShape},
		"url on another host":     {body: bad(map[string]any{"url": "https://evil.example/careers/JobDetail?jobId=135532"}), want: ErrShape},
		"url on a lookalike host": {body: bad(map[string]any{"url": "https://careers.ibm.com.evil.example/careers/JobDetail?jobId=135532"}), want: ErrShape},
		"url with userinfo":       {body: bad(map[string]any{"url": "https://careers.ibm.com@evil.example/careers/JobDetail?jobId=135532"}), want: ErrShape},
		"url over http":           {body: bad(map[string]any{"url": "http://careers.ibm.com/careers/JobDetail?jobId=135532"}), want: ErrShape},
		"url names another job":   {body: bad(map[string]any{"url": "https://careers.ibm.com/careers/JobDetail?jobId=1"}), want: ErrShape},
		"url names no job":        {body: bad(map[string]any{"url": "https://careers.ibm.com/careers"}), want: ErrShape},
		"no url":                  {body: bad(map[string]any{"url": nil}), want: ErrShape},
		"empty title":             {body: bad(map[string]any{"title": "  "}), want: ErrShape},
		"bad effectivedate":       {body: bad(map[string]any{"effectivedate": "5 days ago"}), want: ErrShape},
		"bad dcdate":              {body: bad(map[string]any{"dcdate": "2026-13-45"}), want: ErrShape},
		"malformed":               {body: `{"hits":`},
		"empty body":              {body: ``},
	} {
		s, _ := newSource(constant(200, tc.body))
		got, err := s.List(context.Background(), ibmBoard("x"))
		if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("%s: %d postings, err %v; want %v", name, len(got), err, tc.want)
		}
	}
}

// The boards must tell apart "empty" from "could not read".
func TestDependencyDownIsAnError(t *testing.T) {
	for name, tc := range map[string]struct {
		h    func(srctest.Request) (srctest.Reply, bool)
		want func(error) bool
	}{
		"404": {constant(404, `{"errors":[]}`), func(err error) bool { return errors.Is(err, srcutil.ErrBoardNotFound) }},
		"400": {constant(400, `{"errors":[{"msg":"Parameter 'size' has an invalid value"}]}`), func(err error) bool {
			var se *egress.StatusError
			return errors.As(err, &se) && se.Code == 400
		}},
		"500": {constant(500, `oops`), func(err error) bool {
			var se *egress.StatusError
			return errors.As(err, &se) && se.Code == 500
		}},
		"502": {constant(502, ``), func(err error) bool {
			var se *egress.StatusError
			return errors.As(err, &se) && se.Code == 502
		}},
	} {
		s, _ := newSource(tc.h)
		got, err := s.List(context.Background(), ibmBoard("x"))
		if err == nil || !tc.want(err) {
			t.Errorf("%s: %d postings, err %v", name, len(got), err)
		}
	}
}

type failDoer struct{ err error }

func (d failDoer) Do(context.Context, *http.Request) (*http.Response, error) { return nil, d.err }

func TestTimeoutAndCancellation(t *testing.T) {
	s := &source{d: failDoer{context.DeadlineExceeded}, now: time.Now}
	if got, err := s.List(context.Background(), ibmBoard("x")); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout: %d postings, err %v", len(got), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, d := newSource(constant(200, reply(0, "eq", nil)))
	if _, err := s.List(ctx, ibmBoard("x")); !errors.Is(err, context.Canceled) || len(d.Requests()) != 0 {
		t.Errorf("cancelled context: err %v after %d requests", err, len(d.Requests()))
	}
	// A cancellation between pages stops the walk.
	var a []map[string]any
	for i := 0; i < 150; i++ {
		a = append(a, numbered(t, 6000+i, "E"))
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	h := handler(t, map[string][]map[string]any{"a": a}, nil)
	s, d = newSource(func(r srctest.Request) (srctest.Reply, bool) {
		cancel() // after the first page is served
		return h(r)
	})
	if _, err := s.List(ctx, ibmBoard("a")); !errors.Is(err, context.Canceled) || len(d.Requests()) != 1 {
		t.Errorf("cancelled between pages: err %v after %d requests", err, len(d.Requests()))
	}
}

func TestOversizedBody(t *testing.T) {
	s, _ := newSource(constant(200, strings.Repeat("x", srcutil.MaxBody+1)))
	if _, err := s.List(context.Background(), ibmBoard("x")); !errors.Is(err, srcutil.ErrBodyTooLarge) {
		t.Errorf("err %v, want ErrBodyTooLarge", err)
	}
}

// A board the config would never write is refused before any request.
func TestBoardRules(t *testing.T) {
	good := ibmBoard("x")
	for name, b := range map[string]sources.Board{
		"wrong ats":         {ATS: posting.Amazon, Token: "ibm", Searches: []string{"x"}},
		"empty token":       {ATS: posting.IBM, Token: "", Searches: []string{"x"}},
		"blank token":       {ATS: posting.IBM, Token: "  ", Searches: []string{"x"}},
		"dot dot":           {ATS: posting.IBM, Token: "..", Searches: []string{"x"}},
		"slash":             {ATS: posting.IBM, Token: "ibm/confluent", Searches: []string{"x"}},
		"colon":             {ATS: posting.IBM, Token: "ibm:confluent", Searches: []string{"x"}},
		"space":             {ATS: posting.IBM, Token: "ibm confluent", Searches: []string{"x"}},
		"query":             {ATS: posting.IBM, Token: "ibm?x=1", Searches: []string{"x"}},
		"another company":   {ATS: posting.IBM, Token: "redhat", Searches: []string{"x"}},
		"no search":         {ATS: posting.IBM, Token: "ibm"},
		"blank search":      {ATS: posting.IBM, Token: "ibm", Search: "   "},
		"unverified filter": {ATS: posting.IBM, Token: "ibm", Searches: []string{"x"}, Filters: map[string][]string{"country": {"India"}}},
	} {
		s, d := newSource(constant(200, reply(0, "eq", nil)))
		if got, err := s.List(context.Background(), b); err == nil || len(d.Requests()) != 0 {
			t.Errorf("%s: %d postings, err %v, %d requests", name, len(got), err, len(d.Requests()))
		}
	}
	// A token in capitals is the same board; the key is lower case.
	s, _ := newSource(handler(t, map[string][]map[string]any{"x": {numbered(t, 7, "Engineer")}}, nil))
	b := good
	b.Token = " IBM "
	b.Company = ""
	got, err := s.List(context.Background(), b)
	if err != nil || len(got) != 1 || got[0].Key != "ib:ibm:7" || got[0].Company != "IBM" {
		t.Errorf("%v %v", got, err)
	}
	b = confluentBoard("x")
	b.Token, b.Company = "CONFLUENT", ""
	s, _ = newSource(handler(t, map[string][]map[string]any{"x": {numbered(t, 8, "Staff Engineer - Confluent")}}, nil))
	if got, err = s.List(context.Background(), b); err != nil || len(got) != 1 || got[0].Key != "ib:confluent:8" || got[0].Company != "Confluent" {
		t.Errorf("%v %v", got, err)
	}
}

func TestDates(t *testing.T) {
	for name, tc := range map[string]struct {
		set             map[string]any
		posted, updated time.Time
	}{
		"both":          {map[string]any{"effectivedate": "2026-08-11", "dcdate": "2026-10-27"}, day("2026-08-11"), day("2026-10-27")},
		"only posted":   {map[string]any{"effectivedate": "2026-08-11", "dcdate": nil}, day("2026-08-11"), time.Time{}},
		"neither":       {map[string]any{"effectivedate": nil, "dcdate": nil}, time.Time{}, time.Time{}},
		"blank strings": {map[string]any{"effectivedate": "", "dcdate": " "}, time.Time{}, time.Time{}},
	} {
		s, _ := newSource(constant(200, reply(1, "eq", []map[string]any{clone(fixtureHits(t)[2], tc.set)})))
		got, err := s.List(context.Background(), ibmBoard("x"))
		if err != nil || len(got) != 1 || !got[0].PostedAt.Equal(tc.posted) || !got[0].UpdatedAt.Equal(tc.updated) {
			t.Errorf("%s: %+v, err %v", name, got, err)
		}
	}
}

func TestLocation(t *testing.T) {
	for name, tc := range map[string]struct {
		j    job
		want string
	}{
		"cities with the country": {job{Country: "United States", Cities: []string{"Armonk", "Boston"}}, "Armonk, United States; Boston, United States"},
		"capitals tidied":         {job{Country: "United States", Cities: []string{"POUGHKEEPSIE", "RESEARCH TRIANGLE PARK", "San Jose"}}, "Poughkeepsie, United States; Research Triangle Park, United States; San Jose, United States"},
		"placeholder and repeats": {job{Country: "United States", Cities: []string{"No City", "no city", "Austin", "AUSTIN", " Austin "}}, "Austin, United States"},
		"primary when no list":    {job{Country: "United States", Primary: "Austin, US"}, "Austin, US"},
		"multiple cities, none":   {job{Country: "United States", Primary: "Multiple Cities"}, "United States"},
		"nothing at all":          {job{}, ""},
		"no country":              {job{Cities: []string{"Armonk"}}, "Armonk"},
		"only placeholders":       {job{Country: "United States", Primary: "Boston, US", Cities: []string{"No City"}}, "Boston, US"},
	} {
		if got := tc.j.location(); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	var many []string
	for i := 0; i < maxCities+30; i++ {
		many = append(many, fmt.Sprintf("City%d", i))
	}
	if got := strings.Count((job{Country: "United States", Cities: many}).location(), ";") + 1; got != maxCities {
		t.Errorf("%d cities kept, want %d", got, maxCities)
	}
}

func TestRoleLineAndRemote(t *testing.T) {
	for name, tc := range map[string]struct {
		level, arrangement, lead string
		remote                   bool
	}{
		"remote professional": {"Professional", "Remote", "Role: professional, remote\n\nBody", true},
		"hybrid intern":       {"Internship", "Hybrid", "Role: internship, hybrid\n\nBody", false},
		"on site entry":       {"Entry Level", "", "Role: entry level\n\nBody", false},
		"nothing":             {"", "", "Body", false},
		"lower case remote":   {"", "remote", "Role: remote\n\nBody", true},
	} {
		hit := clone(fixtureHits(t)[2], map[string]any{"field_keyword_18": tc.level, "field_keyword_17": tc.arrangement, "body": "Body"})
		s, _ := newSource(constant(200, reply(1, "eq", []map[string]any{hit})))
		got, err := s.List(context.Background(), ibmBoard("x"))
		if err != nil || len(got) != 1 || got[0].Description != tc.lead || got[0].Remote != tc.remote {
			t.Errorf("%s: %+v, err %v", name, got, err)
		}
		if err == nil && srcutil.SourceText(got[0].Description) != "Body" {
			t.Errorf("%s: SourceText keeps the role line: %q", name, srcutil.SourceText(got[0].Description))
		}
	}
}

func TestDescriptionIsBoundedAndPlain(t *testing.T) {
	huge := strings.Repeat("word ", srcutil.MaxDescription)
	hit := clone(fixtureHits(t)[2], map[string]any{"body": "<script>alert(1)</script>Ship &amp; run it. " + huge})
	s, _ := newSource(constant(200, reply(1, "eq", []map[string]any{hit})))
	got, err := s.List(context.Background(), ibmBoard("x"))
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	d := got[0].Description
	if len(d) > srcutil.MaxDescription || strings.Contains(d, "<script") || strings.Contains(d, "alert(1)") || !strings.Contains(d, "Ship & run it.") {
		t.Errorf("description: %d bytes, %.120q", len(d), d)
	}
}

func TestUnmangle(t *testing.T) {
	for in, want := range map[string]string{
		"world’s":               "world’s",
		"worldâ€™s leading":     "world’s leading",
		"â€œquotedâ€\u009d":     "“quoted”",
		"2019 â€“ 2021 â€” now": "2019 – 2021 — now",
		"â€¢ item â€¦":          "• item …",
		"Zoë and â ok":          "Zoë and â ok",
		"â€ alone":              "â€ alone",
		"":                      "",
	} {
		if got := unmangle(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
