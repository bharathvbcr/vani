package srcutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// headerTimeout is the error net/http gives when a response's headers do not
// arrive within the client's timeout (Apple's and JPMorgan's detail pages,
// 2026-11-16), as a *url.Error that reports Timeout.
func headerTimeout() error {
	return &url.Error{Op: "Get", URL: u, Err: fmt.Errorf("http2: timeout awaiting response headers: %w", os.ErrDeadlineExceeded)}
}

func reset() error {
	return &url.Error{Op: "Get", URL: u, Err: &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}}
}

func TestTransientClassifiesRequestFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"nil":                {nil, false},
		"header timeout":     {headerTimeout(), true},
		"wrapped timeout":    {fmt.Errorf("sources: get x: %w", headerTimeout()), true},
		"connection reset":   {reset(), true},
		"unexpected EOF":     {fmt.Errorf("read: %w", io.ErrUnexpectedEOF), true},
		"bad gateway":        {fmt.Errorf("x: %w", &egress.StatusError{Code: 502}), true},
		"unavailable":        {&egress.StatusError{Code: 503}, true},
		"gateway timeout":    {&egress.StatusError{Code: 504}, true},
		"cloudflare 524":     {&egress.StatusError{Code: 524}, true},
		"server error 500":   {&egress.StatusError{Code: 500}, false},
		"forbidden":          {&egress.StatusError{Code: 403}, false},
		"rate limited":       {&RateLimitedError{Host: "h", Status: &egress.StatusError{Code: 429}}, false},
		"429 status":         {&egress.StatusError{Code: 429}, false},
		"not found":          {ErrBoardNotFound, false},
		"too large":          {ErrBodyTooLarge, false},
		"cancelled":          {context.Canceled, false},
		"wrapped cancel":     {&url.Error{Op: "Get", URL: u, Err: context.Canceled}, false},
		"host not allowed":   {fmt.Errorf("x: %w", egress.ErrNotAllowed), false},
		"blocked address":    {egress.ErrBlockedAddress, false},
		"too many redirects": {egress.ErrTooManyRedirects, false},
		"plain error":        {errors.New("unexpected page layout"), false},
	} {
		if got := Transient(tc.err); got != tc.want {
			t.Errorf("%s: Transient(%v) = %v, want %v", name, tc.err, got, tc.want)
		}
	}
}

// The timeouts the tests above build by hand must classify the way net/http's
// own do: a stalled server under the client's Timeout (what egress sets) and
// under the transport's ResponseHeaderTimeout, with the caller's context live.
func TestTransientMatchesRealNetHTTPTimeouts(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	for name, c := range map[string]*http.Client{
		"client timeout":          {Timeout: 50 * time.Millisecond},
		"response header timeout": {Transport: &http.Transport{ResponseHeaderTimeout: 50 * time.Millisecond}},
	} {
		ctx := context.Background()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Do(req)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("%s: the stalled server answered", name)
		}
		if ctx.Err() != nil || !Transient(err) || !Transient(fmt.Errorf("sources: get x: %w", err)) {
			t.Errorf("%s: Transient(%v) = false, want true", name, err)
		}
	}
}

func TestGetRetriesATimedOutRequestOnce(t *testing.T) {
	waits := noWait(t)
	d := seq(t, srctest.Reply{Err: headerTimeout()}, srctest.Reply{Status: 200, Body: []byte(`{"A":1}`)})
	var v struct{ A int }
	if err := GetJSON(context.Background(), d, u, &v); err != nil || v.A != 1 {
		t.Fatalf("got %+v, %v; want the second attempt's answer", v, err)
	}
	if len(*waits) != 1 || (*waits)[0] <= 0 {
		t.Errorf("waits %v, want one backoff before the retry", *waits)
	}
}

func TestGetRetriesAResetConnectionOnce(t *testing.T) {
	noWait(t)
	d := seq(t, srctest.Reply{Err: reset()}, srctest.Reply{Status: 200, Body: []byte(`{}`)})
	if _, err := GetBody(context.Background(), d, u); err != nil {
		t.Fatalf("reset then 200: %v", err)
	}
}

func TestGetGivesUpAfterOneTransportRetry(t *testing.T) {
	noWait(t)
	d := seq(t, srctest.Reply{Err: headerTimeout()}, srctest.Reply{Err: headerTimeout()})
	_, err := GetBody(context.Background(), d, u)
	if !Transient(err) {
		t.Fatalf("err = %v, want the transient timeout", err)
	}
	if n := len(d.URLs()); n != 2 {
		t.Errorf("%d requests, want 2", n)
	}
}

func TestTransportRetryAfterAGatewayRetryStillHappensOnce(t *testing.T) {
	noWait(t)
	d := seq(t,
		srctest.Reply{Status: 503},
		srctest.Reply{Err: headerTimeout()},
		srctest.Reply{Status: 200, Body: []byte(`{}`)},
	)
	if _, err := GetBody(context.Background(), d, u); err != nil {
		t.Fatalf("503, timeout, 200: %v", err)
	}
}

func TestPostIsNotRetriedOnATransportFailure(t *testing.T) {
	waits := noWait(t)
	d := seq(t, srctest.Reply{Err: headerTimeout()})
	var v struct{}
	if err := PostJSON(context.Background(), d, u, map[string]int{"a": 1}, nil, &v); !Transient(err) {
		t.Fatalf("err = %v, want the timeout", err)
	}
	if len(*waits) != 0 || len(d.URLs()) != 1 {
		t.Errorf("a POST was retried: waits %v, requests %d", *waits, len(d.URLs()))
	}
}

func TestNonTransientTransportFailureIsNotRetried(t *testing.T) {
	noWait(t)
	d := seq(t, srctest.Reply{Err: fmt.Errorf("x: %w", egress.ErrNotAllowed)})
	if _, err := GetBody(context.Background(), d, u); !errors.Is(err, egress.ErrNotAllowed) {
		t.Fatalf("err = %v", err)
	}
}

func TestTransportFailureUnderADoneContextIsNotRetried(t *testing.T) {
	noWait(t)
	ctx, cancel := context.WithCancel(context.Background())
	d := srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) {
		cancel()
		return srctest.Reply{Err: headerTimeout()}, true
	})
	if _, err := GetBody(ctx, d, u); err == nil {
		t.Fatal("want an error")
	}
	if n := len(d.URLs()); n != 1 {
		t.Errorf("%d requests after the context ended, want 1", n)
	}
}

func TestTransportRetryIsChargedToTheBoardBudget(t *testing.T) {
	noWait(t)
	ctx := WithRetryBudget(context.Background(), 0)
	d := seq(t, srctest.Reply{Err: headerTimeout()})
	if _, err := GetBody(ctx, d, u); !Transient(err) {
		t.Fatalf("err = %v, want the timeout with no retry", err)
	}
	if n := len(d.URLs()); n != 1 {
		t.Errorf("%d requests with a spent budget, want 1", n)
	}
}

func TestDetailMissesDefersNewAndReusesStoredRows(t *testing.T) {
	ctx := context.Background()
	// 40 fetched (10 new), none reused: four may miss, capped at three.
	pl := DetailPlan{Fetch: make([]int, 40), New: 10}
	m := NewDetailMisses(pl, true)
	if !m.Take(ctx, 0, headerTimeout()) || !m.Take(ctx, 12, reset()) || !m.Take(ctx, 39, headerTimeout()) {
		t.Fatal("the first three transient misses must be absorbed")
	}
	if m.Take(ctx, 20, headerTimeout()) {
		t.Fatal("a fourth miss must fail the board")
	}
	if m.New != 1 || m.Stored != 2 || m.Total() != 3 {
		t.Errorf("new %d stored %d total %d", m.New, m.Stored, m.Total())
	}
}

func TestDetailMissesRefusesWhatIsNotTransient(t *testing.T) {
	m := NewDetailMisses(DetailPlan{Fetch: make([]int, 30), New: 30}, true)
	for _, err := range []error{
		&egress.StatusError{Code: 500},
		&RateLimitedError{Status: &egress.StatusError{Code: 429}},
		errors.New("detail id does not match"),
	} {
		if m.Take(context.Background(), 0, err) {
			t.Errorf("%v was absorbed", err)
		}
	}
	if m.Total() != 0 {
		t.Errorf("total %d", m.Total())
	}
}

func TestDetailMissesRefusesANewRowThatCannotBeDeferred(t *testing.T) {
	m := NewDetailMisses(DetailPlan{Fetch: make([]int, 30), New: 10}, false)
	if m.Take(context.Background(), 3, headerTimeout()) {
		t.Fatal("a new row was dropped though the source cannot tell it from a stored one")
	}
	if !m.Take(context.Background(), 15, headerTimeout()) {
		t.Fatal("a stored row's failed re-read keeps its stored posting")
	}
}

func TestDetailMissesRefusesUnderADoneContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := NewDetailMisses(DetailPlan{Fetch: make([]int, 30), New: 0}, true)
	if m.Take(ctx, 1, headerTimeout()) {
		t.Fatal("a cancelled run is not a missed detail")
	}
}

func TestDetailMissesNeedsOneAnswerWhenNothingIsReused(t *testing.T) {
	// One detail request and no reused row: missing it would answer the board
	// with nothing the endpoint confirmed.
	m := NewDetailMisses(DetailPlan{Fetch: []int{0}, New: 1}, true)
	if m.Take(context.Background(), 0, headerTimeout()) {
		t.Fatal("the only detail request missed, with nothing reused")
	}
	m = NewDetailMisses(DetailPlan{Fetch: []int{0}, New: 1, Reuse: []int{1, 2}}, true)
	if !m.Take(context.Background(), 0, headerTimeout()) {
		t.Fatal("one new row missed beside reused rows is deferred")
	}
}

func TestDetailMissesDoneRefusesABoardOfMissesAlone(t *testing.T) {
	m := NewDetailMisses(DetailPlan{Fetch: []int{0, 1}, New: 2}, true)
	if !m.Take(context.Background(), 1, headerTimeout()) {
		t.Fatal("one of two misses is absorbed")
	}
	// The other row came back gone: nothing answered, nothing reused.
	if err := m.Done(0); !errors.Is(err, ErrNothingAnswered) {
		t.Fatalf("Done(0) = %v, want ErrNothingAnswered", err)
	}
	if err := m.Done(1); err != nil {
		t.Fatalf("Done(1) = %v", err)
	}
	if err := NewDetailMisses(DetailPlan{Fetch: []int{0}, New: 1}, true).Done(0); err != nil {
		t.Fatalf("a step with no misses is not judged here: %v", err)
	}
	m = NewDetailMisses(DetailPlan{Fetch: []int{0}, New: 1, Reuse: []int{1}}, true)
	m.Take(context.Background(), 0, headerTimeout())
	if err := m.Done(0); err != nil {
		t.Fatalf("a reused row answers the board: %v", err)
	}
}

func TestMaxMissedBounds(t *testing.T) {
	for _, tc := range []struct{ fetch, reused, want int }{
		{0, 0, 0}, {1, 0, 0}, {1, 5, 1}, {2, 0, 1}, {5, 0, 1}, {20, 0, 2}, {46, 0, 3}, {100, 9, 3},
	} {
		if got := MaxMissed(tc.fetch, tc.reused); got != tc.want {
			t.Errorf("MaxMissed(%d, %d) = %d, want %d", tc.fetch, tc.reused, got, tc.want)
		}
	}
}
