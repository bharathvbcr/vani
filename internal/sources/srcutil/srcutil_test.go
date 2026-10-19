package srcutil

import (
	"context"
	"errors"
	"net/http"
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

	// 503 without Retry-After uses the backoff; three failures give up with
	// the status, never more than maxAttempts requests.
	waits = noWait(t)
	d = seq(t, srctest.Reply{Status: 503}, srctest.Reply{Status: 503}, srctest.Reply{Status: 503})
	err := GetJSON(context.Background(), d, u, &v)
	var se *egress.StatusError
	if !errors.As(err, &se) || se.Code != 503 || !strings.Contains(err.Error(), "gave up after 3 attempts") {
		t.Fatalf("three 503s: %v", err)
	}
	if len(d.Requests()) != maxAttempts || len(*waits) != 2 || (*waits)[0] != retryBackoff[0] || (*waits)[1] != retryBackoff[1] {
		t.Errorf("%d requests, waits %v", len(d.Requests()), *waits)
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
	for _, tok := range []string{"", "   ", "\t\n"} {
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
	for _, bad := range []string{"javascript:alert(1)", "file:///etc/passwd", "data:text/html,x", "//a.test/x", "/relative", "https://", "http://[::1"} {
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
