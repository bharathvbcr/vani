package freehire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// fixture is a real search page, taken from the live API on 2026-10-28 for
// q=ml engineer&work_mode=remote&countries=US&limit=100, reduced to the fields
// Vani reads (testdata/search_ml_engineer.json).
func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/search_ml_engineer.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// page builds a response of n results with the given match total.
func page(t *testing.T, n, total int, start int) []byte {
	t.Helper()
	var data []map[string]string
	for i := 0; i < n; i++ {
		id := fmt.Sprint(start + i)
		data = append(data, map[string]string{"source": "greenhouse", "external_id": "co" + id + ":" + id,
			"url": "https://boards.greenhouse.io/co" + id + "/jobs/" + id, "company": "Co " + id})
	}
	b, err := json.Marshal(map[string]any{"data": data, "meta": map[string]int{"total": total, "limit": PageSize, "offset": start}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func ok(body []byte) srctest.Reply { return srctest.Reply{Status: 200, Body: body} }

// byOffset answers each page from replies, keyed by the request's offset.
func byOffset(replies map[string]srctest.Reply) *srctest.Doer {
	return srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		u, err := url.Parse(r.URL)
		if err != nil || u.Host != Host || u.Path != "/api/v1/jobs/search" {
			return srctest.Reply{}, false
		}
		rep, found := replies[u.Query().Get("offset")]
		return rep, found
	})
}

// noWait is a Client whose page gaps cost no time and are recorded.
func noWait(d *srctest.Doer, pages int, gaps *[]time.Duration) Client {
	return Client{Doer: d, Pages: pages, Sleep: func(_ context.Context, g time.Duration) error {
		if gaps != nil {
			*gaps = append(*gaps, g)
		}
		return nil
	}}
}

var q = Query{Search: "ml engineer"}

func TestSearchFixture(t *testing.T) {
	d := byOffset(map[string]srctest.Reply{"0": ok(fixture(t))})
	res, err := noWait(d, 0, nil).Search(context.Background(), Query{Search: "ml engineer", Country: "us", WorkMode: "Remote"})
	if err != nil {
		t.Fatal(err)
	}
	// One request by default, with every parameter spelled out.
	urls := d.URLs()
	if len(urls) != 1 {
		t.Fatalf("%d requests, want 1: %v", len(urls), urls)
	}
	u, _ := url.Parse(urls[0])
	want := url.Values{"q": {"ml engineer"}, "countries": {"US"}, "work_mode": {"remote"}, "limit": {"100"}, "offset": {"0"}}
	if u.Scheme != "https" || u.Host != Host || u.Path != "/api/v1/jobs/search" || u.Query().Encode() != want.Encode() {
		t.Errorf("request %s", urls[0])
	}
	if len(res.Jobs) != 100 || res.Fetched != 100 || res.Pages != 1 || res.Total != 1340 || !res.Capped || res.Skipped != 0 || res.Repeated != 0 || res.Stopped != "" {
		t.Errorf("result: %d jobs, fetched %d, pages %d, total %d, capped %v, skipped %d, repeated %d, stopped %q",
			len(res.Jobs), res.Fetched, res.Pages, res.Total, res.Capped, res.Skipped, res.Repeated, res.Stopped)
	}
	j := res.Jobs[0]
	if j.Source != "lever" || j.ExternalID != "makpar:a5ed4b49-01ec-4e15-bba5-144b1cf34846" || j.Company != "Makpar" ||
		j.URL != "https://jobs.lever.co/makpar/a5ed4b49-01ec-4e15-bba5-144b1cf34846?utm_source=freehire.me" {
		t.Errorf("first job %+v", j)
	}
}

func TestSearchEmptyResults(t *testing.T) {
	d := byOffset(map[string]srctest.Reply{"0": ok([]byte(`{"data":[],"meta":{"limit":100,"offset":0,"total":0}}`))})
	res, err := noWait(d, 3, nil).Search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Jobs) != 0 || res.Total != 0 || res.Capped || res.Pages != 1 || len(d.URLs()) != 1 {
		t.Errorf("empty result %+v after %d requests", res, len(d.URLs()))
	}
}

func TestSearchRefusesAMalformedResponse(t *testing.T) {
	for name, body := range map[string]string{
		"empty body":          ``,
		"not JSON":            `<html>blocked</html>`,
		"truncated":           `{"data":[{"source":"lever"`,
		"trailing garbage":    `{"data":[],"meta":{"total":0}} {"x":1}`,
		"an array":            `[]`,
		"no data":             `{"meta":{"total":3}}`,
		"data is not a list":  `{"data":{"a":1},"meta":{"total":3}}`,
		"null data":           `{"data":null,"meta":{"total":3}}`,
		"no meta":             `{"data":[]}`,
		"no total":            `{"data":[],"meta":{"limit":100}}`,
		"negative total":      `{"data":[],"meta":{"total":-1}}`,
		"total is a string":   `{"data":[],"meta":{"total":"3"}}`,
		"error envelope":      `{"error":{"code":"bad_request","message":"q is required"}}`,
		"JSON null":           `null`,
		"page over the limit": `{"data":[` + strings.Repeat(`{},`, PageSize) + `{}],"meta":{"total":500}}`,
	} {
		t.Run(name, func(t *testing.T) {
			d := byOffset(map[string]srctest.Reply{"0": ok([]byte(body))})
			if res, err := noWait(d, 1, nil).Search(context.Background(), q); err == nil {
				t.Fatalf("no error; result %+v", res)
			}
		})
	}
}

func TestSearchRefusesAnOversizedBody(t *testing.T) {
	big := append([]byte(`{"data":[],"meta":{"total":0},"pad":"`), make([]byte, MaxBody)...)
	d := byOffset(map[string]srctest.Reply{"0": ok(append(big, `"}`...))})
	_, err := noWait(d, 1, nil).Search(context.Background(), q)
	if !errors.Is(err, srcutil.ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
}

// A result that fails validation is counted, not read and not silently lost.
func TestSearchCountsMalformedResults(t *testing.T) {
	long := strings.Repeat("x", maxURL)
	body := `{"meta":{"total":13},"data":[
	 {"source":"lever","external_id":"a:1","url":"https://jobs.lever.co/a/1","company":"A"},
	 {"source":"","external_id":"a:2","url":"https://jobs.lever.co/a/2","company":"A"},
	 {"external_id":"a:3","url":"https://jobs.lever.co/a/3","company":"A"},
	 {"source":"lever","external_id":"a:4","url":"","company":"A"},
	 {"source":"lever","external_id":"a:5","url":"javascript:alert(1)","company":"A"},
	 {"source":"lever","external_id":"a:6","url":"http://jobs.lever.co/a/6","company":"A"},
	 {"source":"lever","external_id":"a:7","url":"https://jobs.lever.co@evil.test/a/7","company":"A"},
	 {"source":"lever","external_id":"a:8","url":"https://jobs.lever.co/` + long + `","company":"A"},
	 {"source":"lever","external_id":"a:9\u0007","url":"https://jobs.lever.co/a/9","company":"A"},
	 {"source":"Lever Jobs","external_id":"a:10","url":"https://jobs.lever.co/a/10","company":"A"},
	 {"source":"lever","external_id":"a:11","url":12,"company":"A"},
	 7,
	 {"source":"LEVER","external_id":"a:13","url":"https://jobs.lever.co/a/13","company":"A"}
	]}`
	d := byOffset(map[string]srctest.Reply{"0": ok([]byte(body))})
	res, err := noWait(d, 1, nil).Search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, j := range res.Jobs {
		ids = append(ids, j.ExternalID)
	}
	// Only the first and the last (its source lower-cased) are well formed.
	if strings.Join(ids, " ") != "a:1 a:13" || res.Skipped != 11 || res.Fetched != 13 || res.Capped {
		t.Errorf("kept %v, skipped %d, fetched %d, capped %v", ids, res.Skipped, res.Fetched, res.Capped)
	}
	if res.Jobs[1].Source != "lever" {
		t.Errorf("source %q not lower-cased", res.Jobs[1].Source)
	}
}

func TestSearchCleansCompanyNames(t *testing.T) {
	body := `{"meta":{"total":6},"data":[
	 {"source":"lever","url":"https://jobs.lever.co/a/1","company":"Müller 数据 Ltd"},
	 {"source":"lever","url":"https://jobs.lever.co/a/2","company":"  Acme \t\n Corp  "},
	 {"source":"lever","url":"https://jobs.lever.co/a/3","company":"Evil\u001b[2J ‮Corp​"},
	 {"source":"lever","url":"https://jobs.lever.co/a/4","company":"","company_slug":"slug-name"},
	 {"source":"lever","url":"https://jobs.lever.co/a/5","company":"   "},
	 {"source":"lever","url":"https://jobs.lever.co/a/6","company":"` + strings.Repeat("é", 500) + `"}
	]}`
	d := byOffset(map[string]srctest.Reply{"0": ok([]byte(body))})
	res, err := noWait(d, 1, nil).Search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(res.Jobs))
	for i, j := range res.Jobs {
		got[i] = j.Company
	}
	want := []string{"Müller 数据 Ltd", "Acme Corp", "Evil[2J Corp", "slug-name", "", strings.Repeat("é", maxName)}
	if len(got) != len(want) {
		t.Fatalf("%d jobs, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("company %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSearchCountsRepeatedResults(t *testing.T) {
	// A server that ignores the offset would hand page 1 back as page 2.
	p := page(t, 100, 500, 0)
	d := byOffset(map[string]srctest.Reply{"0": ok(p), "100": ok(p)})
	res, err := noWait(d, 2, nil).Search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Jobs) != 100 || res.Repeated != 100 || res.Fetched != 200 || res.Pages != 2 {
		t.Errorf("jobs %d, repeated %d, fetched %d, pages %d", len(res.Jobs), res.Repeated, res.Fetched, res.Pages)
	}
}

func TestSearchStopsAtThePageCapAndSaysSo(t *testing.T) {
	d := byOffset(map[string]srctest.Reply{
		"0": ok(page(t, 100, 1340, 0)), "100": ok(page(t, 100, 1340, 100)), "200": ok(page(t, 100, 1340, 200)),
		"300": ok(page(t, 100, 1340, 300)),
	})
	var gaps []time.Duration
	res, err := noWait(d, MaxPages, &gaps).Search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(d.URLs()); n != MaxPages {
		t.Fatalf("%d requests, want the cap %d", n, MaxPages)
	}
	if res.Fetched != 300 || len(res.Jobs) != 300 || res.Total != 1340 || !res.Capped || res.Pages != 3 {
		t.Errorf("fetched %d, jobs %d, total %d, capped %v, pages %d", res.Fetched, len(res.Jobs), res.Total, res.Capped, res.Pages)
	}
	// The pages are spaced: a wait before the second and the third, none before the first.
	if len(gaps) != 2 || gaps[0] != PageGap || gaps[1] != PageGap {
		t.Errorf("gaps %v, want two of %v", gaps, PageGap)
	}
}

func TestSearchStopsWhenEverythingWasRead(t *testing.T) {
	d := byOffset(map[string]srctest.Reply{"0": ok(page(t, 100, 130, 0)), "100": ok(page(t, 30, 130, 100))})
	res, err := noWait(d, MaxPages, nil).Search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(d.URLs()); n != 2 || res.Capped || res.Fetched != 130 {
		t.Errorf("%d requests, capped %v, fetched %d", n, res.Capped, res.Fetched)
	}
	// A page that comes back empty ends the search too, whatever the total says.
	d = byOffset(map[string]srctest.Reply{"0": ok(page(t, 100, 900, 0)), "100": ok(page(t, 0, 900, 100))})
	res, err = noWait(d, MaxPages, nil).Search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(d.URLs()); n != 2 || !res.Capped || res.Fetched != 100 {
		t.Errorf("empty page: %d requests, capped %v, fetched %d", n, res.Capped, res.Fetched)
	}
}

func TestSearchDefaultsToOnePageAndRefusesMoreThanTheCap(t *testing.T) {
	d := byOffset(map[string]srctest.Reply{"0": ok(page(t, 100, 900, 0))})
	if _, err := noWait(d, 0, nil).Search(context.Background(), q); err != nil || len(d.URLs()) != 1 {
		t.Fatalf("default pages: err %v, %d requests", err, len(d.URLs()))
	}
	for _, pages := range []int{-1, MaxPages + 1, 50} {
		d := byOffset(map[string]srctest.Reply{"0": ok(page(t, 1, 1, 0))})
		if _, err := noWait(d, pages, nil).Search(context.Background(), q); err == nil || len(d.URLs()) != 0 {
			t.Errorf("pages %d: err %v after %d requests, want a refusal before any", pages, err, len(d.URLs()))
		}
	}
}

func TestSearchKeepsWhatWasReadWhenALaterPageFails(t *testing.T) {
	for name, rep := range map[string]srctest.Reply{
		"429": {Status: 429, Header: http.Header{"Retry-After": {"3600"}}},
		"500": {Status: 500},
		"bad": ok([]byte(`not json`)),
	} {
		t.Run(name, func(t *testing.T) {
			d := byOffset(map[string]srctest.Reply{"0": ok(page(t, 100, 900, 0)), "100": rep})
			res, err := noWait(d, 2, nil).Search(context.Background(), q)
			if err != nil {
				t.Fatalf("err = %v, want the first page kept", err)
			}
			if len(res.Jobs) != 100 || !res.Capped || res.Pages != 1 || !strings.HasPrefix(res.Stopped, "page 2 was not read") {
				t.Errorf("jobs %d, capped %v, pages %d, stopped %q", len(res.Jobs), res.Capped, res.Pages, res.Stopped)
			}
		})
	}
}

func TestSearchFailsWhenTheFirstPageFails(t *testing.T) {
	for name, tc := range map[string]struct {
		rep  srctest.Reply
		want error
	}{
		"429 with a long Retry-After": {srctest.Reply{Status: 429, Header: http.Header{"Retry-After": {"3600"}}}, srcutil.ErrRateLimited},
		"429 without Retry-After":     {srctest.Reply{Status: 429}, srcutil.ErrRateLimited},
		"404":                         {srctest.Reply{Status: 404}, ErrNotFound},
		"500":                         {srctest.Reply{Status: 500}, nil},
		"503":                         {srctest.Reply{Status: 503}, nil},
		"redirect":                    {srctest.Reply{Status: 302, Header: http.Header{"Location": {"https://evil.test/api"}}}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			d := byOffset(map[string]srctest.Reply{"0": tc.rep})
			// A budget too small for any wait: a retry is given up on, not slept.
			ctx := srcutil.WithRetryBudget(context.Background(), time.Nanosecond)
			_, err := noWait(d, 1, nil).Search(ctx, q)
			if err == nil {
				t.Fatal("no error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if n := len(d.URLs()); n != 1 {
				t.Errorf("%d requests, want 1 (a refusal is not retried past its budget, a redirect is not followed): %v", n, d.URLs())
			}
		})
	}
}

// A 429 that names a short wait is waited out once, within the budget.
func TestSearchRetriesAShortRetryAfter(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	d := srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return srctest.Reply{Status: 429, Header: http.Header{"Retry-After": {"0"}}}, true
		}
		return ok(page(t, 3, 3, 0)), true
	})
	res, err := noWait(d, 1, nil).Search(context.Background(), q)
	if err != nil || len(res.Jobs) != 3 || calls != 2 {
		t.Fatalf("err %v, %d jobs, %d calls", err, len(res.Jobs), calls)
	}
}

type doerFunc func(ctx context.Context, req *http.Request) (*http.Response, error)

func (f doerFunc) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	return f(ctx, req)
}

func TestSearchTimesOut(t *testing.T) {
	d := doerFunc(func(ctx context.Context, _ *http.Request) (*http.Response, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := Client{Doer: d}.Search(ctx, q)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline", err)
	}
}

// A search cancelled between pages is an error, not a quiet short result.
func TestSearchCancelledBetweenPagesIsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := byOffset(map[string]srctest.Reply{"0": ok(page(t, 100, 900, 0)), "100": ok(page(t, 100, 900, 100))})
	c := Client{Doer: d, Pages: 2, Sleep: func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }}
	if res, err := c.Search(ctx, q); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v (stopped %q), want cancellation", err, res.Stopped)
	}
}

func TestSearchWithoutADoer(t *testing.T) {
	if _, err := (Client{}).Search(context.Background(), q); err == nil {
		t.Fatal("no error")
	}
}

func TestQueryValidation(t *testing.T) {
	long := strings.Repeat("a", MaxSearch+1)
	for name, bad := range map[string]Query{
		"no search":         {},
		"blank search":      {Search: "  \t "},
		"long search":       {Search: long},
		"control character": {Search: "ml\x00engineer"},
		"newline":           {Search: "ml\nengineer"},
		"country name":      {Search: "x", Country: "USA"},
		"country digits":    {Search: "x", Country: "1"},
		"one bad country":   {Search: "x", Country: "US,CAN"},
		"work mode":         {Search: "x", WorkMode: "anywhere"},
		"seniority":         {Search: "x", Seniority: "boss"},
		"too many values":   {Search: "x", Country: "AA,AB,AC,AD,AE,AF,AG,AH,AI,AJ,AK"},
	} {
		if err := bad.Check(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	v, err := Query{Search: " ml engineer ", Country: "us, ca", WorkMode: "Remote,hybrid", Seniority: "Senior"}.values()
	if err != nil {
		t.Fatal(err)
	}
	want := url.Values{"q": {"ml engineer"}, "countries": {"US,CA"}, "work_mode": {"remote,hybrid"}, "seniority": {"senior"}}
	if v.Encode() != want.Encode() {
		t.Errorf("values %v, want %v", v, want)
	}
	// A refused query makes no request.
	d := byOffset(nil)
	if _, err := noWait(d, 1, nil).Search(context.Background(), Query{}); err == nil || len(d.URLs()) != 0 {
		t.Errorf("err %v after %d requests", err, len(d.URLs()))
	}
}

// The search text is encoded, never spliced into the URL.
func TestSearchTextCannotChangeTheRequest(t *testing.T) {
	d := byOffset(map[string]srctest.Reply{"0": ok(page(t, 1, 1, 0))})
	if _, err := noWait(d, 1, nil).Search(context.Background(), Query{Search: "a&limit=5000#frag?x=1 ../b"}); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(d.URLs()[0])
	if u.Query().Get("limit") != "100" || u.Query().Get("q") != "a&limit=5000#frag?x=1 ../b" || u.Fragment != "" || u.Path != "/api/v1/jobs/search" {
		t.Errorf("request %s", d.URLs()[0])
	}
}
