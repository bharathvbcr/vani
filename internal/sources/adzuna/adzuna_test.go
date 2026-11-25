package adzuna

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/rowstest"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func TestMain(m *testing.M) { os.Exit(srcutil.RunWithoutWaits(m)) }

// Synthetic credentials. The key carries '+', '/', '=', '&' and '@' so its
// query-escaped and path-escaped forms differ from the raw one.
const (
	testID  = "ab12cd34"
	testKey = "s3cr+t/K3y=v@lue&x"
)

var fixedNow = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

var board = sources.Board{ATS: posting.Adzuna, Token: "gb", Company: "Adzuna", Search: "machine learning"}

func creds(context.Context) (string, string, error) { return testID, testKey, nil }

func noWait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// newSource is the source under test with a fixed clock and no real waits.
func newSource(t *testing.T, d egress.Doer, o Options) *source {
	t.Helper()
	if o.Credentials == nil {
		o.Credentials = creds
	}
	if o.Wait == nil {
		o.Wait = noWait
	}
	s, err := NewWith(d, o)
	if err != nil {
		t.Fatal(err)
	}
	src := s.(*source)
	src.now = func() time.Time { return fixedNow }
	return src
}

// wire checks one request as it reached the network: the search path and the
// credentials in its query.
func wire(t *testing.T, r srctest.Request) (country string, page int, q url.Values) {
	t.Helper()
	u, err := url.Parse(r.URL)
	if err != nil {
		t.Fatalf("bad request URL: %v", err)
	}
	if u.Scheme != "https" || u.Host != Host {
		t.Fatalf("request to %s://%s", u.Scheme, u.Host)
	}
	rest, ok := strings.CutPrefix(u.Path, "/v1/api/jobs/")
	if !ok {
		t.Fatalf("path %q", u.Path)
	}
	c, p, ok := strings.Cut(rest, "/search/")
	if !ok {
		t.Fatalf("path %q", u.Path)
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 {
		t.Fatalf("page %q", p)
	}
	q = u.Query()
	if q.Get("app_id") != testID || q.Get("app_key") != testKey {
		t.Fatalf("credentials on the wire: app_id %q app_key %q", q.Get("app_id"), q.Get("app_key"))
	}
	if q.Has("content-type") {
		t.Fatalf("content-type in the URL: %s", r.URL)
	}
	if got := r.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept %q", got)
	}
	return c, n, q
}

// rowJSON is one valid result for id.
func rowJSON(id string) string {
	return fmt.Sprintf(`{"id":%q,"title":"Role %s","description":"Body %s","created":"2026-11-20T09:15:00Z",`+
		`"redirect_url":"https://www.adzuna.co.uk/jobs/details/%s?v=H%s&utm_medium=api","location":{"display_name":"London"},`+
		`"category":{"tag":"it-jobs","label":"IT Jobs"},"company":{"display_name":"Acme"},"salary_is_predicted":"0"}`, id, id, id, id, id)
}

func pageBody(count int, rows []string) []byte {
	return []byte(`{"count":` + strconv.Itoa(count) + `,"mean":1,"results":[` + strings.Join(rows, ",") + `]}`)
}

// pager answers every term with count(term) rows, 50 a page; row i of a term
// is rowFor(term, i).
func pager(t *testing.T, count func(term string) int, rowFor func(term string, i int) string) *srctest.Doer {
	return srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		_, p, q := wire(t, r)
		term := q.Get("what")
		n := count(term)
		var rows []string
		for i := (p - 1) * pageSize; i < min(p*pageSize, n); i++ {
			rows = append(rows, rowFor(term, i))
		}
		return srctest.Reply{Status: 200, Body: pageBody(n, rows)}, true
	})
}

func idFor(i int) string { return strconv.Itoa(5000000 + i) }

// leakForms are the forms in which the credentials must never appear.
func leakForms() []string {
	return []string{testID, testKey, url.QueryEscape(testKey), url.PathEscape(testKey),
		strings.ToLower(url.QueryEscape(testKey)), "s3cr", "K3y"}
}

func assertNoLeak(t *testing.T, what, s string) {
	t.Helper()
	for _, f := range leakForms() {
		if strings.Contains(s, f) {
			t.Fatalf("%s leaks %q: %s", what, f, s)
		}
	}
}

func TestFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/search.json")
	if err != nil {
		t.Fatal(err)
	}
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		wire(t, r)
		return srctest.Reply{Status: 200, Body: raw}, true
	})
	got, err := newSource(t, d, Options{}).List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(d.Requests()); n != 1 {
		t.Fatalf("%d requests, want 1", n)
	}
	want := []posting.Posting{
		{
			Key: "ad:gb:4123456789", Company: "Example Analytics Ltd", Title: "Senior Machine Learning Engineer",
			Location: "London, UK",
			URL:      "https://adzuna.co.uk/jobs/land/ad/4123456789?se=abcDEF123&v=712F911142DFEF191FB2F0E068CD2734177B7F7E",
			PostedAt: time.Date(2026, 11, 20, 9, 15, 0, 0, time.UTC),
			Description: "Pay range: GBP 70000 – 90000 per year\nRole: full-time, permanent, IT Jobs\n\n" +
				"Example Analytics is hiring a machine learning engineer to build ranking models & data pipelines. You will work with Python and Go…",
		},
		{
			Key: "ad:gb:4123456790", Company: "Synthetic Labs", Title: "Data Scientist",
			Location:    "Manchester, Greater Manchester",
			URL:         "https://www.adzuna.co.uk/jobs/details/4123456790?v=AAAA1111",
			PostedAt:    time.Date(2026, 11, 19, 18, 7, 39, 0, time.UTC),
			Description: "Role: part-time, Scientific & QA Jobs\n\nSynthetic posting for tests. Estimated salary only.",
		},
		{
			Key: "ad:gb:4123456791", Company: "Placeholder Co", Title: "ML Platform Engineer",
			Location:    "Remote",
			URL:         "https://jobs.example.test/apply/77?ref=feed",
			PostedAt:    time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC),
			Description: "Pay range: GBP 500 per year\nRole: contract, IT Jobs\n\nContract role.",
		},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d postings, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		w.ATS = posting.Adzuna
		w.ApplyURL = w.URL
		w.SeenAt = fixedNow
		if got[i] != w {
			t.Errorf("posting %d:\n got %+v\nwant %+v", i, got[i], w)
		}
		if !strings.HasPrefix(string(got[i].Key), board.KeyPrefix()) {
			t.Errorf("key %q outside %q", got[i].Key, board.KeyPrefix())
		}
		assertNoLeak(t, "posting URL", got[i].URL)
	}
}

func TestRequestURL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		filters map[string][]string
		want    map[string]string
	}{
		{"none", nil, map[string]string{}},
		{"all", map[string][]string{
			"where": {"London"}, "distance_km": {"25"}, "max_days_old": {"7"}, "category": {"it-jobs"},
			"salary_min": {"50000"}, "full_time": {"true"}, "permanent": {"true"}, "what_exclude": {"recruiter"},
			"company": {"Example Analytics"},
		}, map[string]string{
			"where": "London", "distance": "25", "max_days_old": "7", "category": "it-jobs", "salary_min": "50000",
			"full_time": "1", "permanent": "1", "what_exclude": "recruiter", "company": "Example Analytics",
		}},
		{"part time contract", map[string][]string{"part_time": {"true"}, "contract": {" true "}},
			map[string]string{"part_time": "1", "contract": "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := board
			b.Token = "us"
			b.Filters = tc.filters
			d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
				c, p, q := wire(t, r)
				if c != "us" || p != 1 {
					t.Errorf("country %q page %d", c, p)
				}
				want := map[string]string{"what": "machine learning", "sort_by": "date", "results_per_page": "50",
					"app_id": testID, "app_key": testKey}
				for k, v := range tc.want {
					want[k] = v
				}
				if len(q) != len(want) {
					t.Errorf("query %v, want exactly %v", q, want)
				}
				for k, v := range want {
					if got := q[k]; len(got) != 1 || got[0] != v {
						t.Errorf("param %s = %v, want %q", k, got, v)
					}
				}
				return srctest.Reply{Status: 200, Body: pageBody(0, nil)}, true
			})
			got, err := newSource(t, d, Options{}).List(context.Background(), b)
			if err != nil || len(got) != 0 {
				t.Fatalf("got %v, %v", got, err)
			}
			if len(d.Requests()) != 1 {
				t.Fatalf("%d requests", len(d.Requests()))
			}
		})
	}
}

// recorder sits between srcutil and the key-adding Doer and records the URLs
// srcutil built.
type recorder struct {
	next egress.Doer
	mu   sync.Mutex
	urls []string
}

func (r *recorder) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.urls = append(r.urls, req.URL.String())
	r.mu.Unlock()
	return r.next.Do(ctx, req)
}

func TestKeyOnlyOnTheWire(t *testing.T) {
	d := pager(t, func(string) int { return 70 }, func(_ string, i int) string { return rowJSON(idFor(i)) })
	s := newSource(t, d, Options{})
	st := Stats{}
	kd := &keyDoer{inner: d, src: s, id: testID, key: testKey, sec: newSecrets(testID, testKey), st: &st}
	rec := &recorder{next: kd}
	rows, _, err := search(context.Background(), rec, "gb", "machine learning", url.Values{}, MaxPostings)
	if err != nil || len(rows) != 70 {
		t.Fatalf("got %d rows, %v", len(rows), err)
	}
	if len(rec.urls) != 2 || len(d.URLs()) != 2 {
		t.Fatalf("srcutil sent %d, wire saw %d", len(rec.urls), len(d.URLs()))
	}
	for i, u := range rec.urls {
		if strings.Contains(u, "app_id") || strings.Contains(u, "app_key") {
			t.Errorf("srcutil's URL carries a credential parameter: %s", u)
		}
		assertNoLeak(t, "srcutil URL", u)
		if !strings.Contains(d.URLs()[i], "app_key=") {
			t.Errorf("wire URL lacks the key: %s", d.URLs()[i])
		}
		want := pageURL("gb", "machine learning", url.Values{}, i+1)
		if u != want {
			t.Errorf("srcutil URL %s, want %s", u, want)
		}
	}
	if st.Requests != 2 {
		t.Errorf("Requests %d", st.Requests)
	}
}

type timeoutErr struct{ msg string }

func (e timeoutErr) Error() string   { return e.msg }
func (e timeoutErr) Timeout() bool   { return true }
func (e timeoutErr) Temporary() bool { return true }

// bodyErrDoer answers 200 with a body whose read fails with an error naming
// the request's URL.
type bodyErrDoer struct{ n int }

type failBody struct{ msg string }

func (b failBody) Read([]byte) (int, error) { return 0, errors.New(b.msg) }
func (failBody) Close() error               { return nil }

func (d *bodyErrDoer) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	d.n++
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: failBody{"read " + req.URL.String() + ": broken"}, Request: req}, nil
}

func TestKeyNeverLeaks(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reply     func(r srctest.Request) srctest.Reply
		requests  int
		check     func(t *testing.T, err error)
		transient bool
	}{
		{
			name: "url.Error with the keyed URL",
			reply: func(r srctest.Request) srctest.Reply {
				return srctest.Reply{Err: &url.Error{Op: "Get", URL: r.URL, Err: errors.New("dial tcp: connection refused for " + r.URL)}}
			},
			requests: 1,
			check: func(t *testing.T, err error) {
				var ue *url.Error
				if !errors.As(err, &ue) {
					t.Fatalf("no *url.Error in the chain: %v", err)
				}
				assertNoLeak(t, "url.Error.URL", ue.URL)
				if !strings.Contains(ue.URL, "/v1/api/jobs/gb/search/1") {
					t.Errorf("url.Error lost its URL: %q", ue.URL)
				}
			},
		},
		{
			name: "transport timeout",
			reply: func(r srctest.Request) srctest.Reply {
				return srctest.Reply{Err: &url.Error{Op: "Get", URL: r.URL, Err: timeoutErr{"i/o timeout after " + testKey}}}
			},
			requests:  2, // srcutil's one transport retry
			transient: true,
		},
		{
			name: "401 AUTH_FAIL",
			reply: func(srctest.Request) srctest.Reply {
				return srctest.Reply{Status: 401, Body: []byte(`{"doc":"https://api.adzuna.com/v1/doc","__CLASS__":"Adzuna::API::Response::Exception","exception":"AUTH_FAIL","display":"Authorisation failed"}`)}
			},
			requests: 1,
			check: func(t *testing.T, err error) {
				if !errors.Is(err, ErrAuth) || !strings.Contains(err.Error(), "Adzuna refused the app id and key (AUTH_FAIL)") {
					t.Fatalf("err %v, want ErrAuth", err)
				}
			},
		},
		{
			name: "410 auth fail",
			reply: func(srctest.Request) srctest.Reply {
				return srctest.Reply{Status: 410, Body: []byte(`not json ` + testKey)}
			},
			requests: 1,
			check: func(t *testing.T, err error) {
				if !errors.Is(err, ErrAuth) {
					t.Fatalf("err %v, want ErrAuth", err)
				}
			},
		},
		{
			name: "400 names its exception",
			reply: func(srctest.Request) srctest.Reply {
				return srctest.Reply{Status: 400, Body: []byte(`{"exception":"UNSUPPORTED_SEARCH","display":"` + testKey + `"}`)}
			},
			requests: 1,
			check: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "UNSUPPORTED_SEARCH") || errors.Is(err, ErrAuth) {
					t.Fatalf("err %v", err)
				}
			},
		},
		{
			name: "429",
			reply: func(srctest.Request) srctest.Reply {
				return srctest.Reply{Status: 429, Body: []byte(`{"exception":"TOO_MANY"}`)}
			},
			requests: 5,
			check: func(t *testing.T, err error) {
				var rl *srcutil.RateLimitedError
				if !errors.As(err, &rl) || !errors.Is(err, srcutil.ErrRateLimited) {
					t.Fatalf("err %v, want a *srcutil.RateLimitedError", err)
				}
				assertNoLeak(t, "RateLimitedError", rl.Error()+rl.Path)
			},
		},
		{
			name:     "500",
			reply:    func(srctest.Request) srctest.Reply { return srctest.Reply{Status: 500, Body: []byte(`{}`)} },
			requests: 1,
			check: func(t *testing.T, err error) {
				var se *egress.StatusError
				if !errors.As(err, &se) || se.Code != 500 {
					t.Fatalf("err %v, want HTTP 500", err)
				}
			},
		},
		{
			name:      "502 after retries",
			reply:     func(srctest.Request) srctest.Reply { return srctest.Reply{Status: 502} },
			requests:  5,
			transient: true,
		},
		{
			name: "302 redirect carrying the key",
			reply: func(r srctest.Request) srctest.Reply {
				return srctest.Reply{Status: 302, Header: http.Header{"Location": {r.URL + "&next=1"}}}
			},
			requests: 1,
		},
		{
			name:     "malformed JSON",
			reply:    func(srctest.Request) srctest.Reply { return srctest.Reply{Status: 200, Body: []byte(`{"count":`)} },
			requests: 1,
		},
		{
			name: "200 with AUTH_FAIL exception",
			reply: func(srctest.Request) srctest.Reply {
				return srctest.Reply{Status: 200, Body: []byte(`{"exception":"AUTH_FAIL"}`)}
			},
			requests: 1,
			check: func(t *testing.T, err error) {
				if !errors.Is(err, ErrAuth) {
					t.Fatalf("err %v, want ErrAuth", err)
				}
			},
		},
		{
			name: "srctest's own unexpected-request error",
			reply: func(r srctest.Request) srctest.Reply {
				return srctest.Reply{Err: fmt.Errorf("unexpected request %s (key %s, escaped %s)", r.URL, testKey, url.QueryEscape(testKey))}
			},
			requests: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
				wire(t, r)
				return tc.reply(r), true
			})
			var reports []Stats
			got, err := newSource(t, d, Options{Report: func(_ sources.Board, st Stats) { reports = append(reports, st) }}).
				List(context.Background(), board)
			if err == nil {
				t.Fatalf("got %d postings and no error", len(got))
			}
			assertNoLeak(t, "error", err.Error())
			assertNoLeak(t, "error %+v", fmt.Sprintf("%+v %#v", err, err.Error()))
			for e := errors.Unwrap(err); e != nil; e = errors.Unwrap(e) {
				assertNoLeak(t, "wrapped error", e.Error())
			}
			if n := len(d.Requests()); n != tc.requests {
				t.Errorf("%d requests, want %d", n, tc.requests)
			}
			if got := srcutil.Transient(err); got != tc.transient {
				t.Errorf("Transient = %v, want %v (%v)", got, tc.transient, err)
			}
			if len(reports) != 1 || reports[0].Requests != tc.requests {
				t.Errorf("reports %+v, want one with %d requests", reports, tc.requests)
			}
			if tc.check != nil {
				tc.check(t, err)
			}
		})
	}
}

func TestBodyReadErrorScrubbed(t *testing.T) {
	d := &bodyErrDoer{}
	_, err := newSource(t, d, Options{}).List(context.Background(), board)
	if err == nil {
		t.Fatal("no error")
	}
	assertNoLeak(t, "error", err.Error())
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("error lost its cause: %v", err)
	}
}

func TestScrubKeepsChain(t *testing.T) {
	sec := newSecrets(testID, testKey)
	base := fmt.Errorf("asked with %s: %w", url.QueryEscape(testKey), srcutil.ErrBoardNotFound)
	err := sec.scrub(fmt.Errorf("outer %s: %w", testID, base))
	assertNoLeak(t, "scrubbed", err.Error())
	if !errors.Is(err, srcutil.ErrBoardNotFound) {
		t.Errorf("errors.Is lost the sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), redacted) {
		t.Errorf("no %s marker: %v", redacted, err)
	}
	clean := errors.New("nothing secret")
	if sec.scrub(clean) != clean {
		t.Error("a clean error was replaced")
	}
	var ue *url.Error
	timeout := sec.scrub(&url.Error{Op: "Get", URL: "https://api.adzuna.com/x?app_key=" + url.QueryEscape(testKey), Err: timeoutErr{"slow " + testKey}})
	if !errors.As(timeout, &ue) || !ue.Timeout() || !srcutil.Transient(timeout) {
		t.Errorf("timeout lost through scrub: %v", timeout)
	}
	assertNoLeak(t, "url.Error", timeout.Error())
}

func TestKeyDoerRefuses(t *testing.T) {
	inner := srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) {
		t.Error("inner Doer reached")
		return srctest.Reply{}, false
	})
	s := newSource(t, inner, Options{})
	for _, raw := range []string{
		"https://evil.test/v1/api/jobs/gb/search/1",
		"http://api.adzuna.com/v1/api/jobs/gb/search/1",
		"https://api.adzuna.com/v1/other",
		"https://u@api.adzuna.com/v1/api/jobs/gb/search/1",
		"https://api.adzuna.com/v1/api/jobs/gb/search/1?app_id=x",
	} {
		st := Stats{}
		kd := &keyDoer{inner: inner, src: s, id: testID, key: testKey, sec: newSecrets(testID, testKey), st: &st}
		req, _ := http.NewRequest(http.MethodGet, raw, nil)
		if _, err := kd.Do(context.Background(), req); err == nil {
			t.Errorf("%s: sent", raw)
		}
	}
	st := Stats{Requests: MaxRequests}
	kd := &keyDoer{inner: inner, src: s, id: testID, key: testKey, sec: newSecrets(testID, testKey), st: &st}
	req, _ := http.NewRequest(http.MethodGet, pageURL("gb", "x", nil, 1), nil)
	if _, err := kd.Do(context.Background(), req); !errors.Is(err, ErrRequestBudget) {
		t.Errorf("over budget: %v", err)
	}
}

func refuseAll(t *testing.T) *srctest.Doer {
	return srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		t.Errorf("unexpected request %s", r.URL)
		return srctest.Reply{}, false
	})
}

func TestNoCredentials(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(context.Context) (string, string, error)
		want error
	}{
		{"nil", nil, ErrNoCredentials},
		{"empty id", func(context.Context) (string, string, error) { return "", testKey, nil }, ErrNoCredentials},
		{"blank key", func(context.Context) (string, string, error) { return testID, " \t", nil }, ErrNoCredentials},
		{"lookup failed", func(context.Context) (string, string, error) { return "", "", errors.New("keychain locked") }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := refuseAll(t)
			s, _ := NewWith(d, Options{Credentials: tc.fn, Wait: noWait})
			if tc.fn == nil {
				s = New(d)
			}
			reported := false
			if src, ok := s.(*source); ok && tc.fn != nil {
				src.opt.Report = func(sources.Board, Stats) { reported = true }
			}
			_, err := s.List(context.Background(), board)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("err %v, want %v", err, tc.want)
			}
			if len(d.Requests()) != 0 || reported {
				t.Fatalf("requests %d, reported %v", len(d.Requests()), reported)
			}
		})
	}
}

func TestBadCredentials(t *testing.T) {
	for _, tc := range []struct{ name, id, key string }{
		{"space inside id", "ab12 cd34", testKey},
		{"control char in key", testID, "abc\x01def"},
		{"newline in key", testID, "abc\ndef"},
		{"long key", testID, strings.Repeat("k", 129)},
		{"invalid utf-8", testID, "abc\xffdef"},
		{"no-break space", testID, "abc\u00a0def"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := refuseAll(t)
			s := newSource(t, d, Options{Credentials: func(context.Context) (string, string, error) { return tc.id, tc.key, nil }})
			_, err := s.List(context.Background(), board)
			if !errors.Is(err, ErrBadCredentials) {
				t.Fatalf("err %v, want ErrBadCredentials", err)
			}
			for _, v := range []string{tc.id, tc.key} {
				if strings.Contains(err.Error(), v) {
					t.Fatalf("error echoes a credential: %v", err)
				}
			}
			if len(d.Requests()) != 0 {
				t.Fatal("a request was sent")
			}
		})
	}
	t.Run("trimmed", func(t *testing.T) {
		d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
			wire(t, r) // asserts the trimmed values
			return srctest.Reply{Status: 200, Body: pageBody(0, nil)}, true
		})
		s := newSource(t, d, Options{Credentials: func(context.Context) (string, string, error) { return " " + testID + "\n", "\t" + testKey + " ", nil }})
		if _, err := s.List(context.Background(), board); err != nil {
			t.Fatal(err)
		}
	})
}

func TestInvalidBoards(t *testing.T) {
	mod := func(f func(*sources.Board)) sources.Board {
		b := board
		f(&b)
		return b
	}
	filters := func(f map[string][]string) sources.Board { return mod(func(b *sources.Board) { b.Filters = f }) }
	for name, b := range map[string]sources.Board{
		"other ats":              mod(func(b *sources.Board) { b.ATS = posting.Himalayas }),
		"upper-case country":     mod(func(b *sources.Board) { b.Token = "GB" }),
		"unknown country":        mod(func(b *sources.Board) { b.Token = "uk" }),
		"empty token":            mod(func(b *sources.Board) { b.Token = "" }),
		"no search":              mod(func(b *sources.Board) { b.Search = " " }),
		"too many searches":      mod(func(b *sources.Board) { b.Searches = strings.Split("a b c d e f g h i j", " ") }),
		"control char search":    mod(func(b *sources.Board) { b.Search = "ml\x00x" }),
		"oversized search":       mod(func(b *sources.Board) { b.Search = strings.Repeat("x", sources.MaxSearchLen+1) }),
		"max postings over":      mod(func(b *sources.Board) { b.MaxPostings = MaxPostingsCeiling + 1 }),
		"max postings negative":  mod(func(b *sources.Board) { b.MaxPostings = -1 }),
		"unknown filter":         filters(map[string][]string{"country": {"gb"}}),
		"two values":             filters(map[string][]string{"where": {"London", "Leeds"}}),
		"no value":               filters(map[string][]string{"where": {}}),
		"blank value":            filters(map[string][]string{"where": {"  "}}),
		"where too long":         filters(map[string][]string{"where": {strings.Repeat("w", 101)}}),
		"where control":          filters(map[string][]string{"where": {"Lon\x7fdon"}}),
		"distance no where":      filters(map[string][]string{"distance_km": {"10"}}),
		"distance zero":          filters(map[string][]string{"where": {"London"}, "distance_km": {"0"}}),
		"distance over":          filters(map[string][]string{"where": {"London"}, "distance_km": {"501"}}),
		"distance word":          filters(map[string][]string{"where": {"London"}, "distance_km": {"ten"}}),
		"distance plus":          filters(map[string][]string{"where": {"London"}, "distance_km": {"+5"}}),
		"days zero":              filters(map[string][]string{"max_days_old": {"0"}}),
		"days over":              filters(map[string][]string{"max_days_old": {"366"}}),
		"days fraction":          filters(map[string][]string{"max_days_old": {"1.5"}}),
		"category spaced":        filters(map[string][]string{"category": {"IT Jobs"}}),
		"category underscore":    filters(map[string][]string{"category": {"it_jobs"}}),
		"category long":          filters(map[string][]string{"category": {strings.Repeat("a", 65)}}),
		"salary negative":        filters(map[string][]string{"salary_min": {"-1"}}),
		"salary zero":            filters(map[string][]string{"salary_min": {"0"}}),
		"salary exp":             filters(map[string][]string{"salary_min": {"1e5"}}),
		"salary huge":            filters(map[string][]string{"salary_min": {"999999999"}}),
		"full_time yes":          filters(map[string][]string{"full_time": {"yes"}}),
		"contract 1":             filters(map[string][]string{"contract": {"1"}}),
		"full and part time":     filters(map[string][]string{"full_time": {"true"}, "part_time": {"true"}}),
		"permanent and contract": filters(map[string][]string{"permanent": {"true"}, "contract": {"true"}}),
		"company too long":       filters(map[string][]string{"company": {strings.Repeat("c", 101)}}),
		"exclude invalid utf8":   filters(map[string][]string{"what_exclude": {"a\xffb"}}),
	} {
		t.Run(name, func(t *testing.T) {
			d := refuseAll(t)
			called := false
			s := newSource(t, d, Options{Credentials: func(context.Context) (string, string, error) {
				called = true
				return testID, testKey, nil
			}})
			if got, err := s.List(context.Background(), b); err == nil {
				t.Fatalf("accepted: %d postings", len(got))
			}
			if called || len(d.Requests()) != 0 {
				t.Fatalf("credentials read %v, requests %d", called, len(d.Requests()))
			}
		})
	}
}

func TestValidCountry(t *testing.T) {
	if len(Countries) != 19 {
		t.Fatalf("%d countries", len(Countries))
	}
	for _, c := range Countries {
		if !ValidCountry(c) || len(currencies[c]) != 3 {
			t.Errorf("%s: valid %v currency %q", c, ValidCountry(c), currencies[c])
		}
	}
	for _, c := range []string{"", "GB", "uk", "eu", "gb ", "g"} {
		if ValidCountry(c) {
			t.Errorf("%q accepted", c)
		}
	}
	if len(currencies) != len(Countries) {
		t.Error("currencies and Countries disagree")
	}
}

func TestOverCapBeforePage2(t *testing.T) {
	for _, tc := range []struct {
		name  string
		max   int
		count int
	}{{"default", 0, MaxPostings + 1}, {"board cap", 100, 101}} {
		t.Run(tc.name, func(t *testing.T) {
			d := pager(t, func(string) int { return tc.count }, func(_ string, i int) string { return rowJSON(idFor(i)) })
			var reports []Stats
			b := board
			b.MaxPostings = tc.max
			_, err := newSource(t, d, Options{Report: func(_ sources.Board, st Stats) { reports = append(reports, st) }}).
				List(context.Background(), b)
			if !errors.Is(err, srcutil.ErrTooManyPostings) || !strings.Contains(err.Error(), "where, max_days_old or category") {
				t.Fatalf("err %v", err)
			}
			if len(d.Requests()) != 1 {
				t.Fatalf("%d requests, want 1", len(d.Requests()))
			}
			if len(reports) != 1 || reports[0].Matched != tc.count || reports[0].Requests != 1 {
				t.Fatalf("reports %+v", reports)
			}
		})
	}
	t.Run("union", func(t *testing.T) {
		b := board
		b.Searches = []string{"data"}
		d := pager(t, func(string) int { return 300 }, func(term string, i int) string {
			if term == "data" {
				return rowJSON(idFor(1000 + i))
			}
			return rowJSON(idFor(i))
		})
		_, err := newSource(t, d, Options{}).List(context.Background(), b)
		if !errors.Is(err, srcutil.ErrTooManyPostings) {
			t.Fatalf("err %v", err)
		}
	})
}

func TestPagingAndDedupe(t *testing.T) {
	// Page 2 starts one row early (the listing shifted under the walk): row 49
	// repeats and is dropped; the 120 distinct rows end the walk on page 3.
	pages := map[int][2]int{1: {0, 50}, 2: {49, 99}, 3: {99, 120}}
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		_, p, _ := wire(t, r)
		span, ok := pages[p]
		if !ok {
			t.Errorf("page %d requested", p)
			return srctest.Reply{}, false
		}
		var rows []string
		for i := span[0]; i < span[1]; i++ {
			rows = append(rows, rowJSON(idFor(i)))
		}
		return srctest.Reply{Status: 200, Body: pageBody(120, rows)}, true
	})
	var waits []time.Duration
	var reports []Stats
	s := newSource(t, d, Options{
		Wait:   func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return ctx.Err() },
		Report: func(_ sources.Board, st Stats) { reports = append(reports, st) },
	})
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 120 || len(d.Requests()) != 3 {
		t.Fatalf("%d postings in %d requests", len(got), len(d.Requests()))
	}
	keys := map[posting.Key]bool{}
	for _, p := range got {
		if keys[p.Key] {
			t.Fatalf("duplicate %s", p.Key)
		}
		keys[p.Key] = true
	}
	if want := (Stats{Listed: 120, Requests: 3, Matched: 120}); len(reports) != 1 || reports[0] != want {
		t.Fatalf("reports %+v, want %+v", reports, want)
	}
	// The clock is fixed, so each wait is the whole gap.
	if len(waits) != 2 || waits[0] != MinGap || waits[1] != MinGap {
		t.Fatalf("waits %v, want two of %s", waits, MinGap)
	}
}

func TestShapeFailures(t *testing.T) {
	for name, pages := range map[string][]string{
		"page adds nothing": {string(pageBody(60, rowsOf(0, 50))), string(pageBody(60, nil))},
		// A page of repeats moves on; the listing then ends 10 short of 60.
		"page only repeats": {string(pageBody(60, rowsOf(0, 50))), string(pageBody(60, rowsOf(0, 10))), string(pageBody(60, nil))},
		// Page 1 promised 120; page 2 answers a count of 0 and no rows.
		"later count shrinks":       {string(pageBody(120, rowsOf(0, 50))), string(pageBody(0, nil))},
		"count below rows":          {string(pageBody(2, rowsOf(0, 3)))},
		"no count":                  {`{"results":[]}`},
		"negative count":            {`{"count":-1,"results":[]}`},
		"null results":              {`{"count":0,"results":null}`},
		"results not a list":        {`{"count":1,"results":{}}`},
		"other exception":           {`{"exception":"<script>"}`},
		"repeats leave count unmet": {string(pageBody(3, []string{rowJSON("1"), rowJSON("1")})), string(pageBody(3, nil))},
	} {
		t.Run(name, func(t *testing.T) {
			i := 0
			d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
				wire(t, r)
				if i >= len(pages) {
					t.Errorf("request %d beyond the script", i+1)
					return srctest.Reply{}, false
				}
				i++
				return srctest.Reply{Status: 200, Body: []byte(pages[i-1])}, true
			})
			got, err := newSource(t, d, Options{}).List(context.Background(), board)
			if err == nil {
				t.Fatalf("accepted %d postings", len(got))
			}
			if strings.Contains(err.Error(), "<script>") {
				t.Fatalf("untrusted exception echoed: %v", err)
			}
		})
	}
	t.Run("empty listing is valid", func(t *testing.T) {
		d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
			wire(t, r)
			return srctest.Reply{Status: 200, Body: pageBody(0, nil)}, true
		})
		got, err := newSource(t, d, Options{}).List(context.Background(), board)
		if err != nil || got == nil && err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	t.Run("a repeat within a page", func(t *testing.T) {
		d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
			wire(t, r)
			return srctest.Reply{Status: 200, Body: pageBody(1, []string{rowJSON("1"), rowJSON("1")})}, true
		})
		got, err := newSource(t, d, Options{}).List(context.Background(), board)
		if err != nil || len(got) != 1 {
			t.Fatalf("got %d, %v", len(got), err)
		}
	})
}

func rowsOf(from, to int) []string {
	var out []string
	for i := from; i < to; i++ {
		out = append(out, rowJSON(idFor(i)))
	}
	return out
}

func TestAcrossTerms(t *testing.T) {
	b := board
	b.Searches = []string{"data", "DATA"} // the repeat is one term
	d := pager(t, func(string) int { return 3 }, func(term string, i int) string {
		if term == "data" {
			return rowJSON(idFor(i + 1)) // rows 1..3; 1 and 2 overlap the first term's
		}
		return rowJSON(idFor(i))
	})
	var reports []Stats
	got, err := newSource(t, d, Options{Report: func(_ sources.Board, st Stats) { reports = append(reports, st) }}).
		List(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || len(d.Requests()) != 2 {
		t.Fatalf("%d postings in %d requests", len(got), len(d.Requests()))
	}
	if want := (Stats{Listed: 4, Requests: 2, Matched: 6}); len(reports) != 1 || reports[0] != want {
		t.Fatalf("reports %+v, want %+v", reports, want)
	}
}

func TestPredictedSalary(t *testing.T) {
	for _, tc := range []struct {
		raw string
		pay bool
	}{
		{`"0"`, true}, {`0`, true}, {`false`, true}, {`null`, true}, {``, true},
		{`"1"`, false}, {`1`, false}, {`true`, false}, {`"maybe"`, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			field := ""
			if tc.raw != "" {
				field = `,"salary_is_predicted":` + tc.raw
			}
			row := `{"id":"1","title":"T","company":{"display_name":"C"},"redirect_url":"https://www.adzuna.com/details/1","salary_min":100000,"salary_max":120000` + field + `}`
			d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
				wire(t, r)
				return srctest.Reply{Status: 200, Body: pageBody(1, []string{row})}, true
			})
			b := board
			b.Token = "us"
			got, err := newSource(t, d, Options{}).List(context.Background(), b)
			if err != nil || len(got) != 1 {
				t.Fatalf("got %v, %v", got, err)
			}
			hasPay := strings.HasPrefix(got[0].Description, "Pay range: USD 100000 – 120000 per year")
			if hasPay != tc.pay {
				t.Fatalf("description %q, want pay line %v", got[0].Description, tc.pay)
			}
		})
	}
}

func TestStableLink(t *testing.T) {
	sec := newSecrets(testID, testKey)
	for _, tc := range []struct{ in, want string }{
		{"http://adzuna.co.uk/jobs/land/ad/1?v=AB&utm_medium=api&utm_source=x", "https://adzuna.co.uk/jobs/land/ad/1?v=AB"},
		{"http://www.adzuna.com.au/details/1?UTM_Source=x&v=1", "https://www.adzuna.com.au/details/1?v=1"},
		{"http://jobs.example.test/1?utm_term=x", "http://jobs.example.test/1"},
		{"http://adzuna.co.uk.evil.test/1", "http://adzuna.co.uk.evil.test/1"},
		{"http://notadzuna.com/1", "http://notadzuna.com/1"},
		{"https://www.adzuna.de/details/1?b=2&a=1&utm_campaign=z#frag", "https://www.adzuna.de/details/1?b=2&a=1#frag"},
		{"https://www.adzuna.com/details/1?src=" + testID + "&v=9", "https://www.adzuna.com/details/1?v=9"},
		{"https://www.adzuna.com/details/1?k=" + url.QueryEscape(testKey) + "&v=9", "https://www.adzuna.com/details/1?v=9"},
		{"https://www.adzuna.com/details/1?utm_source=a", "https://www.adzuna.com/details/1"},
	} {
		got, err := stableLink(tc.in, sec)
		if err != nil || got != tc.want {
			t.Errorf("stableLink(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "javascript:alert(1)", "https://user@adzuna.com/1", "/relative", "ftp://adzuna.com/1",
		"https://www.adzuna.com/" + testID + "/1", "https://www.adzuna.com/" + strings.Repeat("x", srcutil.MaxURL)} {
		if got, err := stableLink(bad, sec); err == nil {
			t.Errorf("stableLink(%q) = %q, accepted", bad, got)
		}
	}
}

func TestBadRows(t *testing.T) {
	rowstest.Check(t, rowstest.Case{
		List: func(ctx context.Context, payload string) ([]posting.Posting, error) {
			d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
				wire(t, r)
				return srctest.Reply{Status: 200, Body: []byte(payload)}, true
			})
			return newSource(t, d, Options{}).List(ctx, board)
		},
		Payload: func(n int, bad map[int]string) string {
			rows := make([]string, n)
			for i := range rows {
				rows[i] = rowJSON(idFor(i))
				if r, ok := bad[i]; ok {
					rows[i] = r
				}
			}
			return string(pageBody(n, rows))
		},
		BadRows: map[string]string{
			"empty title":      `{"id":"77` + rowstest.Index + `","title":"<strong></strong>","company":{"display_name":"C"},"redirect_url":"https://www.adzuna.com/d/1"}`,
			"letters id":       `{"id":"abc` + rowstest.Index + `","title":"T","company":{"display_name":"C"},"redirect_url":"https://www.adzuna.com/d/1"}`,
			"long id":          `{"id":"123456789012345678901` + rowstest.Index + `","title":"T","company":{"display_name":"C"},"redirect_url":"https://www.adzuna.com/d/1"}`,
			"no id":            `{"title":"T` + rowstest.Index + `","company":{"display_name":"C"},"redirect_url":"https://www.adzuna.com/d/1"}`,
			"script url":       `{"id":"77` + rowstest.Index + `","title":"T","company":{"display_name":"C"},"redirect_url":"javascript:alert(1)"}`,
			"no url":           `{"id":"77` + rowstest.Index + `","title":"T","company":{"display_name":"C"}}`,
			"bad created":      `{"id":"77` + rowstest.Index + `","title":"T","company":{"display_name":"C"},"redirect_url":"https://www.adzuna.com/d/1","created":"yesterday"}`,
			"not an object":    `"row ` + rowstest.Index + `"`,
			"company a string": `{"id":"77` + rowstest.Index + `","title":"T","company":"C","redirect_url":"https://www.adzuna.com/d/1"}`,
			"salary a string":  `{"id":"77` + rowstest.Index + `","title":"T","company":{"display_name":"C"},"redirect_url":"https://www.adzuna.com/d/1","salary_min":"lots"}`,
		},
	})
}

func TestDeterministic(t *testing.T) {
	raw, err := os.ReadFile("testdata/search.json")
	if err != nil {
		t.Fatal(err)
	}
	list := func(now time.Time) []posting.Posting {
		d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
			wire(t, r)
			return srctest.Reply{Status: 200, Body: raw}, true
		})
		s := newSource(t, d, Options{})
		s.now = func() time.Time { return now }
		got, err := s.List(context.Background(), board)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	a, b := list(fixedNow), list(fixedNow.Add(49*time.Hour))
	if len(a) != len(b) || len(a) == 0 {
		t.Fatalf("%d vs %d postings", len(a), len(b))
	}
	for i := range a {
		if a[i].SeenAt.Equal(b[i].SeenAt) {
			t.Fatal("SeenAt did not follow the clock")
		}
		a[i].SeenAt, b[i].SeenAt = time.Time{}, time.Time{}
		if a[i] != b[i] {
			t.Errorf("posting %d differs across runs:\n%+v\n%+v", i, a[i], b[i])
		}
	}
}

func TestWindowed(t *testing.T) {
	for _, tc := range []struct {
		filters map[string][]string
		want    bool
	}{
		{nil, false},
		{map[string][]string{"where": {"Leeds"}, "category": {"it-jobs"}}, false},
		{map[string][]string{"max_days_old": {"14"}}, true},
	} {
		d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
			wire(t, r)
			return srctest.Reply{Status: 200, Body: pageBody(1, []string{rowJSON("1")})}, true
		})
		var reports []Stats
		b := board
		b.Filters = tc.filters
		if _, err := newSource(t, d, Options{Report: func(_ sources.Board, st Stats) { reports = append(reports, st) }}).
			List(context.Background(), b); err != nil {
			t.Fatal(err)
		}
		if len(reports) != 1 || reports[0].Windowed != tc.want {
			t.Errorf("filters %v: reports %+v, want Windowed %v", tc.filters, reports, tc.want)
		}
	}
}

func TestCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := refuseAll(t)
	if _, err := newSource(t, d, Options{}).List(ctx, board); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	t.Run("during the wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		d := pager(t, func(string) int { return 120 }, func(_ string, i int) string { return rowJSON(idFor(i)) })
		s := newSource(t, d, Options{Wait: func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err()
		}})
		if _, err := s.List(ctx, board); !errors.Is(err, context.Canceled) {
			t.Fatalf("err %v", err)
		}
		if len(d.Requests()) != 1 {
			t.Fatalf("%d requests", len(d.Requests()))
		}
	})
}

// TestPacingAcrossBoards lists two boards on one source: every request, the
// second board's first included, starts at least MinGap after the one before.
func TestPacingAcrossBoards(t *testing.T) {
	var mu sync.Mutex
	clock := fixedNow
	var sent []time.Time
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		_, p, _ := wire(t, r)
		mu.Lock()
		sent = append(sent, clock)
		clock = clock.Add(200 * time.Millisecond) // the request itself takes a while
		mu.Unlock()
		var rows []string
		for i := (p - 1) * pageSize; i < min(p*pageSize, 60); i++ {
			rows = append(rows, rowJSON(idFor(i)))
		}
		return srctest.Reply{Status: 200, Body: pageBody(60, rows)}, true
	})
	s := newSource(t, d, Options{Wait: func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		clock = clock.Add(d)
		mu.Unlock()
		return ctx.Err()
	}})
	s.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	for _, c := range []string{"gb", "us"} {
		b := board
		b.Token = c
		if _, err := s.List(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	if len(sent) != 4 {
		t.Fatalf("%d requests", len(sent))
	}
	for i := 1; i < len(sent); i++ {
		if gap := sent[i].Sub(sent[i-1]); gap < MinGap {
			t.Errorf("request %d came %s after the one before, under %s", i+1, gap, MinGap)
		}
	}
}

func TestNewDefaults(t *testing.T) {
	s := New(refuseAll(t))
	if s.ATS() != posting.Adzuna {
		t.Fatalf("ATS %q", s.ATS())
	}
	if _, err := s.List(context.Background(), board); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("err %v", err)
	}
	if _, err := NewWith(nil, Options{Credentials: creds}); err != nil {
		t.Fatal(err)
	}
	s2, _ := NewWith(nil, Options{Credentials: creds})
	if _, err := s2.List(context.Background(), board); err == nil {
		t.Fatal("a nil Doer listed")
	}
}

// An advert that names no employer cannot be a posting: it is skipped and
// counted, never a bad row, so a board of agency adverts is not failed past
// MaxGone, with or without a bad-row sink.
func TestNoCompanyRowsAreSkippedNotBad(t *testing.T) {
	var st Stats
	d := pager(t, func(string) int { return 10 }, func(_ string, i int) string {
		if i%2 == 0 {
			return `{"id":"` + idFor(i) + `","title":"Role","redirect_url":"https://www.adzuna.com/d/1","company":{"display_name":"  "}}`
		}
		return rowJSON(idFor(i))
	})
	got, err := newSource(t, d, Options{Report: func(_ sources.Board, s Stats) { st = s }}).List(context.Background(), board)
	if err != nil || len(got) != 5 || st.NoCompany != 5 {
		t.Fatalf("got %d postings, err %v, stats %+v; want 5, nil, NoCompany 5", len(got), err, st)
	}
}

// Paging under sort_by=date shifts while it is read: an advert posted
// mid-walk repeats a row on the next page and adds one at the end, one
// withdrawn leaves the walk one short. Both are kept, as a whole listing.
func TestPagingToleratesAListingThatShiftsMidWalk(t *testing.T) {
	for name, pages := range map[string][]string{
		"one posted mid-walk":    {string(pageBody(60, rowsOf(0, 50))), string(pageBody(61, rowsOf(49, 61))), string(pageBody(61, nil))},
		"one withdrawn mid-walk": {string(pageBody(60, rowsOf(0, 50))), string(pageBody(59, rowsOf(51, 60))), string(pageBody(59, nil))},
	} {
		t.Run(name, func(t *testing.T) {
			i := 0
			d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
				wire(t, r)
				if i >= len(pages) {
					t.Errorf("request %d beyond the script", i+1)
					return srctest.Reply{Status: 500}, true
				}
				i++
				return srctest.Reply{Status: 200, Body: []byte(pages[i-1])}, true
			})
			got, err := newSource(t, d, Options{}).List(context.Background(), board)
			if err != nil || len(got) < 59 {
				t.Fatalf("got %d postings, %v", len(got), err)
			}
		})
	}
}

// An advert already stored keeps its stored link, whatever token the listing's
// redirect_url carries this time, so a relist writes no field update.
func TestStoredAdvertKeepsItsLink(t *testing.T) {
	call := 0
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		wire(t, r)
		call++
		row := strings.Replace(rowJSON(idFor(1)), "?v=H", fmt.Sprintf("?se=run%d&v=H", call), 1)
		return srctest.Reply{Status: 200, Body: pageBody(2, []string{row, rowJSON(idFor(2))})}, true
	})
	first, err := newSource(t, d, Options{}).List(context.Background(), board)
	if err != nil || len(first) != 2 {
		t.Fatalf("first: %d %v", len(first), err)
	}
	stored := func(sources.Board) []posting.Posting { return first }
	again, err := newSource(t, d, Options{Stored: stored}).List(context.Background(), board)
	if err != nil || len(again) != 2 {
		t.Fatalf("again: %d %v", len(again), err)
	}
	for i := range again {
		if again[i].URL != first[i].URL || again[i].ApplyURL != first[i].ApplyURL {
			t.Fatalf("posting %d link moved: %q -> %q", i, first[i].URL, again[i].URL)
		}
	}
	// Without the stored postings the new token would have moved the link.
	fresh, _ := newSource(t, d, Options{}).List(context.Background(), board)
	if fresh[0].URL == first[0].URL {
		t.Fatal("the test's token did not change the link; it proves nothing")
	}
}
