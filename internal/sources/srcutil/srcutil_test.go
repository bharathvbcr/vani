package srcutil

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

const u = "https://example.test/x"

func get(t *testing.T, r srctest.Reply) error {
	t.Helper()
	var v struct{ A int }
	return GetJSON(context.Background(), srctest.New(map[string]srctest.Reply{u: r}), u, &v)
}

// noWait replaces the retry sleep for one test and records each wait asked for.
func noWait(t *testing.T) *[]time.Duration {
	t.Helper()
	old := sleep
	var waits []time.Duration
	sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	t.Cleanup(func() { sleep = old })
	return &waits
}

// seq answers u with replies in order, then fails the test on any extra call.
func seq(t *testing.T, replies ...srctest.Reply) *srctest.Doer {
	t.Helper()
	n := 0
	return srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if r.URL != u || n >= len(replies) {
			t.Errorf("unexpected request %d: %s %s", n+1, r.Method, r.URL)
			return srctest.Reply{}, false
		}
		n++
		return replies[n-1], true
	})
}

func retryAfterHdr(v string) http.Header { return http.Header{"Retry-After": {v}} }

// noJitter makes the backoff its upper bound, so a test can name each wait.
func noJitter(t *testing.T) {
	t.Helper()
	old := jitter
	jitter = func(d time.Duration) time.Duration { return d }
	t.Cleanup(func() { jitter = old })
}

// rateLimited asserts err is a *RateLimitedError for example.test that still
// reads as the 429 status, and returns it.
func rateLimited(t *testing.T, err error) *RateLimitedError {
	t.Helper()
	var rl *RateLimitedError
	var se *egress.StatusError
	if !errors.Is(err, ErrRateLimited) || !errors.As(err, &rl) || !errors.As(err, &se) || se.Code != 429 {
		t.Fatalf("err = %v, want a rate-limited 429", err)
	}
	if rl.Host != "example.test" || !strings.Contains(err.Error(), "rate limited by example.test (HTTP 429), retry next run") {
		t.Errorf("error %q does not name the host and the next step", err)
	}
	return rl
}

func tooMany(n int, h http.Header) []srctest.Reply {
	rs := make([]srctest.Reply, n)
	for i := range rs {
		rs[i] = srctest.Reply{Status: 429, Header: h}
	}
	return rs
}

// A 429 with no Retry-After backs off exponentially, jittered inside
// [d/2, d], and gives up as rate limited after maxAttempts.
func TestRateLimitBackoffWithoutHeader(t *testing.T) {
	var v struct{ A int }
	waits := noWait(t)
	d := seq(t, tooMany(maxAttempts, nil)...)
	rl := rateLimited(t, GetJSON(context.Background(), d, u, &v))
	if rl.Attempts != maxAttempts || !strings.Contains(rl.Why, "gave up after 5 attempts") || len(d.Requests()) != maxAttempts {
		t.Errorf("attempts %d, why %q, %d requests", rl.Attempts, rl.Why, len(d.Requests()))
	}
	if len(*waits) != maxAttempts-1 {
		t.Fatalf("waits %v", *waits)
	}
	// The real jitter: each wait inside [ceil/2, ceil], ceilings doubling.
	ceil := backoffBase
	for i, w := range *waits {
		if w < ceil/2 || w > ceil {
			t.Errorf("wait %d = %v, want within [%v, %v]", i, w, ceil/2, ceil)
		}
		ceil = min(ceil*2, maxBackoff)
	}
	// Jitter is applied, not constant: over many draws the first wait varies.
	seen := map[time.Duration]bool{}
	for range 50 {
		seen[backoff(0)] = true
	}
	if len(seen) < 5 {
		t.Errorf("backoff(0) took %d distinct values in 50 draws; no jitter", len(seen))
	}
	// And it never exceeds the cap, however many retries.
	for n := 0; n < 100; n++ {
		if w := backoff(n); w > maxBackoff || w <= 0 {
			t.Fatalf("backoff(%d) = %v", n, w)
		}
	}
}

// Retry-After in seconds, then in HTTP-date form, is waited exactly (against
// the fake clock the date is measured from) and then the request succeeds.
func TestRateLimitHonoursRetryAfterForms(t *testing.T) {
	var v struct{ A int }
	ok := srctest.Reply{Status: 200, Body: []byte(`{"A":9}`)}
	clock := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	old := now
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = old })

	for name, tc := range map[string]struct {
		hdr  string
		want time.Duration
	}{
		"seconds":   {"7", 7 * time.Second},
		"http date": {clock.Add(12 * time.Second).UTC().Format(http.TimeFormat), 12 * time.Second},
		"zero":      {"0", 0},
		"past date": {clock.Add(-time.Hour).UTC().Format(http.TimeFormat), 0},
	} {
		waits := noWait(t)
		d := seq(t, srctest.Reply{Status: 429, Header: retryAfterHdr(tc.hdr)}, ok)
		if err := GetJSON(context.Background(), d, u, &v); err != nil || v.A != 9 {
			t.Fatalf("%s: v=%+v err=%v", name, v, err)
		}
		if len(*waits) != 1 || (*waits)[0] != tc.want {
			t.Errorf("%s: waits %v, want [%v]", name, *waits, tc.want)
		}
	}
}

// A Retry-After over the cap, as seconds or as a date, is a rate-limited
// error at once and is never slept.
func TestRateLimitRetryAfterOverCapIsNotSlept(t *testing.T) {
	var v struct{ A int }
	clock := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	old := now
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = old })
	for _, h := range []string{"3600", clock.Add(time.Hour).UTC().Format(http.TimeFormat), "31"} {
		waits := noWait(t)
		d := seq(t, srctest.Reply{Status: 429, Header: retryAfterHdr(h)})
		rl := rateLimited(t, GetJSON(context.Background(), d, u, &v))
		if len(*waits) != 0 || len(d.Requests()) != 1 || !strings.Contains(rl.Why, "over the 30s limit") {
			t.Errorf("Retry-After %q: waits %v, requests %d, why %q", h, *waits, len(d.Requests()), rl.Why)
		}
	}
	// Exactly the cap is waited.
	waits := noWait(t)
	d := seq(t, srctest.Reply{Status: 429, Header: retryAfterHdr("30")}, srctest.Reply{Status: 200, Body: []byte(`{}`)})
	if err := GetJSON(context.Background(), d, u, &v); err != nil || len(*waits) != 1 || (*waits)[0] != maxRetryWait {
		t.Errorf("Retry-After at the cap: err %v waits %v", err, *waits)
	}
}

// All the waits of one request are bounded: four Retry-After 30 waits would be
// two minutes, so the request gives up once the next wait would pass the limit.
func TestRateLimitPerRequestWaitIsBounded(t *testing.T) {
	var v struct{ A int }
	waits := noWait(t)
	d := seq(t, tooMany(maxAttempts, retryAfterHdr("30"))...)
	rl := rateLimited(t, GetJSON(context.Background(), d, u, &v))
	var total time.Duration
	for _, w := range *waits {
		total += w
	}
	if total > maxRequestWait || len(*waits) != 2 || !strings.Contains(rl.Why, "limit for one request") {
		t.Errorf("waited %v (total %v), why %q; want at most %v in all", *waits, total, rl.Why, maxRequestWait)
	}
}

// The board's budget is shared by its requests: once it cannot cover a wait the
// request gives up without sleeping, whatever the per-request bound allows.
func TestRateLimitBoardBudget(t *testing.T) {
	var v struct{ A int }
	waits := noWait(t)
	ctx := WithRetryBudget(context.Background(), 10*time.Second)

	// First request: 429 (Retry-After 6s) then 200, leaving 4s.
	d := seq(t, srctest.Reply{Status: 429, Header: retryAfterHdr("6")}, srctest.Reply{Status: 200, Body: []byte(`{}`)})
	if err := GetJSON(ctx, d, u, &v); err != nil || len(*waits) != 1 {
		t.Fatalf("first: err %v waits %v", err, *waits)
	}
	// Second request: another 6s would overdraw the 4s left.
	d = seq(t, srctest.Reply{Status: 429, Header: retryAfterHdr("6")})
	rl := rateLimited(t, GetJSON(ctx, d, u, &v))
	if len(*waits) != 1 || !strings.Contains(rl.Why, "retry budget is spent") {
		t.Errorf("waits %v, why %q: the overdrawn wait was taken", *waits, rl.Why)
	}
	// A fresh budget is a fresh board.
	d = seq(t, srctest.Reply{Status: 429, Header: retryAfterHdr("6")}, srctest.Reply{Status: 200, Body: []byte(`{}`)})
	if err := GetJSON(WithRetryBudget(context.Background(), 10*time.Second), d, u, &v); err != nil {
		t.Errorf("fresh budget: %v", err)
	}
	// A zero budget never sleeps at all.
	waits = noWait(t)
	d = seq(t, srctest.Reply{Status: 429})
	rateLimited(t, GetJSON(WithRetryBudget(context.Background(), 0), d, u, &v))
	if len(*waits) != 0 {
		t.Errorf("zero budget waited %v", *waits)
	}
}

// Cancelling during the backoff ends the wait with the context's error, which
// is not reported as rate limiting.
func TestRateLimitCancelledMidWait(t *testing.T) {
	var v struct{ A int }
	ctx, cancel := context.WithCancel(context.Background())
	old := sleep
	sleep = func(ctx context.Context, d time.Duration) error {
		cancel() // the run is stopped while the request waits
		<-ctx.Done()
		return ctx.Err()
	}
	t.Cleanup(func() { sleep = old })
	d := seq(t, srctest.Reply{Status: 429})
	err := GetJSON(ctx, d, u, &v)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrRateLimited) || len(d.Requests()) != 1 {
		t.Fatalf("err = %v (%d requests), want the context's cancellation only", err, len(d.Requests()))
	}
}

// 429 then 200 succeeds without a rate-limited error, through GetBody and
// GetJSONLimit as well: they share the one request loop.
func TestRateLimitThenOKOnEveryEntryPoint(t *testing.T) {
	ok := srctest.Reply{Status: 200, Body: []byte(`{"A":1}`)}
	noWait(t)
	var v struct{ A int }
	if err := GetJSONLimit(context.Background(), seq(t, srctest.Reply{Status: 429}, ok), u, 1<<10, &v); err != nil || v.A != 1 {
		t.Errorf("GetJSONLimit: %v", err)
	}
	if b, err := GetBody(context.Background(), seq(t, srctest.Reply{Status: 429}, srctest.Reply{Status: 429}, ok), u); err != nil || string(b) != `{"A":1}` {
		t.Errorf("GetBody: %q %v", b, err)
	}
}

func TestRetryOnRateLimit(t *testing.T) {
	var v struct{ A int }
	okReply := srctest.Reply{Status: 200, Body: []byte(`{"A":7}`)}

	// 429 then 200: the board survives, after the server's Retry-After.
	waits := noWait(t)
	d := seq(t, srctest.Reply{Status: 429, Header: retryAfterHdr("3")}, okReply)
	if err := GetJSON(context.Background(), d, u, &v); err != nil || v.A != 7 {
		t.Fatalf("429 then 200: v=%+v err=%v", v, err)
	}
	if len(*waits) != 1 || (*waits)[0] != 3*time.Second {
		t.Errorf("waits %v, want [3s] from Retry-After", *waits)
	}

	// 503 without Retry-After uses the backoff; maxAttempts failures give up
	// with the status, never more requests. A 503 is not "rate limited".
	waits = noWait(t)
	noJitter(t)
	replies := make([]srctest.Reply, maxAttempts)
	for i := range replies {
		replies[i] = srctest.Reply{Status: 503}
	}
	d = seq(t, replies...)
	err := GetJSON(context.Background(), d, u, &v)
	var se *egress.StatusError
	if !errors.As(err, &se) || se.Code != 503 || !strings.Contains(err.Error(), "gave up after 5 attempts") || errors.Is(err, ErrRateLimited) {
		t.Fatalf("%d 503s: %v", maxAttempts, err)
	}
	if want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}; len(d.Requests()) != maxAttempts || !slices.Equal(*waits, want) {
		t.Errorf("%d requests, waits %v, want %v", len(d.Requests()), *waits, want)
	}

	// A Retry-After over the cap is honoured by giving up, at once.
	waits = noWait(t)
	d = seq(t, srctest.Reply{Status: 429, Header: retryAfterHdr("3600")})
	if err := GetJSON(context.Background(), d, u, &v); !errors.As(err, &se) || se.Code != 429 || !strings.Contains(err.Error(), "over the") {
		t.Fatalf("huge Retry-After: %v", err)
	}
	if len(*waits) != 0 {
		t.Errorf("waited %v for a Retry-After over the cap", *waits)
	}

	// Other statuses are not retried; a 404 stays ErrBoardNotFound.
	for _, code := range []int{400, 401, 403, 404, 500, 502} {
		waits = noWait(t)
		d = seq(t, srctest.Reply{Status: code})
		err := GetJSON(context.Background(), d, u, &v)
		if err == nil || len(d.Requests()) != 1 || len(*waits) != 0 {
			t.Errorf("status %d: err=%v requests=%d waits=%v", code, err, len(d.Requests()), *waits)
		}
		if (code == 404) != errors.Is(err, ErrBoardNotFound) {
			t.Errorf("status %d: ErrBoardNotFound mismatch: %v", code, err)
		}
	}
}

// A cancelled context ends the retry wait (real sleep, short deadline).
func TestRetryWaitHonoursContext(t *testing.T) {
	var v struct{ A int }
	d := seq(t, srctest.Reply{Status: 429, Header: retryAfterHdr("20")})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := GetJSON(ctx, d, u, &v)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context's deadline", err)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("retry wait ignored the context: %v", el)
	}
}

// PostJSON resends the identical body and headers on a retry.
func TestPostJSONRetryResendsBody(t *testing.T) {
	noWait(t)
	var v struct{ A int }
	d := seq(t, srctest.Reply{Status: 429}, srctest.Reply{Status: 200, Body: []byte(`{"A":1}`)})
	body := map[string]any{"searchText": "inference", "limit": 20}
	if err := PostJSON(context.Background(), d, u, body, http.Header{"Accept-Language": {"en-US"}}, &v); err != nil {
		t.Fatal(err)
	}
	reqs := d.Requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want 2", len(reqs))
	}
	for i, r := range reqs {
		if r.Method != http.MethodPost || string(r.Body) != `{"limit":20,"searchText":"inference"}` ||
			r.Header.Get("Accept-Language") != "en-US" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("request %d: %s body=%q header=%v", i, r.Method, r.Body, r.Header)
		}
	}
	if err := PostJSON(context.Background(), d, u, func() {}, nil, &v); err == nil {
		t.Error("an unencodable body was sent")
	}
}

// GetBody returns an HTML body under the same status, cap and retry rules,
// asking for HTML rather than JSON.
func TestGetBody(t *testing.T) {
	noWait(t)
	d := seq(t, srctest.Reply{Status: 429}, srctest.Reply{Status: 200, Body: []byte("<html>ok</html>")})
	body, err := GetBody(context.Background(), d, u)
	if err != nil || string(body) != "<html>ok</html>" {
		t.Fatalf("body %q, err %v", body, err)
	}
	if acc := d.Requests()[0].Header.Get("Accept"); !strings.Contains(acc, "text/html") {
		t.Errorf("Accept %q, want text/html", acc)
	}
	if _, err := GetBody(context.Background(), seq(t, srctest.Reply{Status: 404}), u); !errors.Is(err, ErrBoardNotFound) {
		t.Errorf("404: %v", err)
	}
	old := maxBody
	maxBody = 8
	t.Cleanup(func() { maxBody = old })
	if _, err := GetBody(context.Background(), seq(t, srctest.Reply{Status: 200, Body: []byte("123456789")}), u); !errors.Is(err, ErrBodyTooLarge) {
		t.Errorf("over cap: %v", err)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	for v, want := range map[string]struct {
		d  time.Duration
		ok bool
	}{
		"":                              {0, false},
		"0":                             {0, true},
		" 7 ":                           {7 * time.Second, true},
		"-1":                            {0, false},
		"1.5":                           {0, false},
		"soon":                          {0, false},
		"99999999999999999999":          {0, false},
		"9223372036854775807":           {time.Hour, true},
		"Mon, 19 Oct 2026 12:00:10 GMT": {10 * time.Second, true},
		"Mon, 19 Oct 2026 11:00:00 GMT": {0, true},
	} {
		d, ok := retryAfter(http.Header{"Retry-After": {v}}, now)
		if d != want.d || ok != want.ok {
			t.Errorf("Retry-After %q = %v, %v; want %v, %v", v, d, ok, want.d, want.ok)
		}
	}
}

func TestGetJSON(t *testing.T) {
	noWait(t)
	if err := get(t, srctest.Reply{Status: 200, Body: []byte(`{"A":1}`)}); err != nil {
		t.Fatal(err)
	}
	if err := get(t, srctest.Reply{Status: 404}); !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("404: %v", err)
	}
	for _, status := range []int{204, 301, 401, 429, 500, 503} {
		err := get(t, srctest.Reply{Status: status, Body: []byte(`{"A":1}`)})
		if err == nil || errors.Is(err, ErrBoardNotFound) {
			t.Fatalf("status %d: want a plain error, got %v", status, err)
		}
	}
	for name, body := range map[string]string{"empty": "", "truncated": `{"A":`, "trailing": `{"A":1} x`, "wrong type": `{"A":"s"}`, "html": "<html>"} {
		if err := get(t, srctest.Reply{Status: 200, Body: []byte(body)}); err == nil {
			t.Errorf("%s body: want error", name)
		}
	}
}

func TestGetJSONBodyCap(t *testing.T) {
	old := maxBody
	maxBody = 32
	t.Cleanup(func() { maxBody = old })
	ok := `{"A":1}` + strings.Repeat(" ", 25) // exactly 32 bytes
	if err := get(t, srctest.Reply{Status: 200, Body: []byte(ok)}); err != nil {
		t.Fatalf("body at the cap: %v", err)
	}
	err := get(t, srctest.Reply{Status: 200, Body: []byte(ok + " ")})
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("body over the cap: %v", err)
	}
}

func TestGetJSONLimit(t *testing.T) {
	const u = "https://x.test/one"
	ok := `{"A":1}` + strings.Repeat(" ", 9) // exactly 16 bytes
	limited := func(body string, limit int64) error {
		d := srctest.New(map[string]srctest.Reply{u: {Status: 200, Body: []byte(body)}})
		var v struct{ A int }
		return GetJSONLimit(context.Background(), d, u, limit, &v)
	}
	if err := limited(ok, 16); err != nil {
		t.Fatalf("body at the limit: %v", err)
	}
	if err := limited(ok+" ", 16); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("body over the limit: %v", err)
	}
	// A limit above MaxBody, or none, is MaxBody: never wider.
	old := maxBody
	maxBody = 16
	t.Cleanup(func() { maxBody = old })
	for _, limit := range []int64{0, -1, 1 << 40} {
		if err := limited(ok+" ", limit); !errors.Is(err, ErrBodyTooLarge) {
			t.Errorf("limit %d: %v", limit, err)
		}
	}
	d := srctest.New(map[string]srctest.Reply{u: {Status: 404}})
	if err := GetJSONLimit(context.Background(), d, u, 16, &struct{}{}); !errors.Is(err, ErrBoardNotFound) {
		t.Errorf("404: %v", err)
	}
}

type failDoer struct{}

func (failDoer) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("dial refused")
}

func TestGetJSONTransportAndNilDoer(t *testing.T) {
	var v struct{}
	if err := GetJSON(context.Background(), failDoer{}, u, &v); err == nil || !strings.Contains(err.Error(), "dial refused") {
		t.Fatalf("transport error not surfaced: %v", err)
	}
	if err := GetJSON(context.Background(), nil, u, &v); err == nil {
		t.Fatal("nil doer must error")
	}
	if err := GetJSON(context.Background(), failDoer{}, "http://[::1", &v); err == nil {
		t.Fatal("bad url must error")
	}
}

func TestCheckBoard(t *testing.T) {
	good := sources.Board{ATS: posting.Lever, Token: "acme"}
	if err := CheckBoard(good, posting.Lever); err != nil {
		t.Fatal(err)
	}
	if err := CheckBoard(good, posting.Ashby); err == nil {
		t.Error("wrong ats accepted")
	}
	// ':' would collide with the key separator: "acme:x" cannot key a posting
	// and makes the board's key prefix ambiguous.
	for _, tok := range []string{"", "   ", "\t\n", "acme:x", ":", "acme:"} {
		if err := CheckBoard(sources.Board{ATS: posting.Lever, Token: tok}, posting.Lever); err == nil {
			t.Errorf("token %q accepted", tok)
		}
	}
}

func TestCheckCountAndURLAndTime(t *testing.T) {
	if CheckCount(MaxPostings) != nil {
		t.Error("cap itself must pass")
	}
	if !errors.Is(CheckCount(MaxPostings+1), ErrTooManyPostings) {
		t.Error("over the cap must fail")
	}
	for _, ok := range []string{"", "https://a.test/x", "http://a.test"} {
		if err := CheckURL("f", ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"javascript:alert(1)", "file:///etc/passwd", "data:text/html,x", "//a.test/x", "/relative", "https://", "http://[::1",
		"https://jobs.acme.test@evil.test/x", "https://u:p@a.test/x", "https://@a.test/x"} {
		if err := CheckURL("f", bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if tm, err := ParseTime("f", ""); err != nil || !tm.IsZero() {
		t.Errorf("empty time: %v %v", tm, err)
	}
	if _, err := ParseTime("f", "2026-08-03T23:05:52.543+00:00"); err != nil {
		t.Errorf("ashby format: %v", err)
	}
	if _, err := ParseTime("f", "yesterday"); err == nil {
		t.Error("garbage time accepted")
	}
}
