package srcutil

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// errBody is a response body whose Read fails after data, as a connection cut
// or a timeout does part-way through a 200.
type errBody struct {
	data   []byte
	err    error
	closed bool
}

func (b *errBody) Read(p []byte) (int, error) {
	if len(b.data) > 0 {
		n := copy(p, b.data)
		b.data = b.data[n:]
		return n, nil
	}
	return 0, b.err
}

func (b *errBody) Close() error { b.closed = true; return nil }

// cancelBody ends the run's context, then fails the read.
type cancelBody struct{ cancel context.CancelFunc }

func (b *cancelBody) Read([]byte) (int, error) { b.cancel(); return 0, io.ErrUnexpectedEOF }
func (b *cancelBody) Close() error             { return nil }

// bodyStep is one answer of a bodyDoer: a 200 with body, or a Do error.
type bodyStep struct {
	body io.ReadCloser
	err  error
}

// bodyDoer answers each request with the next step, then errors.
type bodyDoer struct {
	mu    sync.Mutex
	steps []bodyStep
	n     int
}

func (d *bodyDoer) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.n++
	if len(d.steps) == 0 {
		return nil, errors.New("bodyDoer: unexpected request")
	}
	s := d.steps[0]
	d.steps = d.steps[1:]
	if s.err != nil {
		return nil, s.err
	}
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: s.body, Request: req}, nil
}

func (d *bodyDoer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.n
}

func okBody(s string) io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }

// byHost answers each host's requests from its own queue; an extra request
// fails the test.
func byHost(t *testing.T, queues map[string][]srctest.Reply) *srctest.Doer {
	t.Helper()
	var mu sync.Mutex
	return srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		mu.Lock()
		defer mu.Unlock()
		h := strings.SplitN(strings.TrimPrefix(r.URL, "https://"), "/", 2)[0]
		q := queues[h]
		if len(q) == 0 {
			t.Errorf("unexpected request to %s", r.URL)
			return srctest.Reply{}, false
		}
		queues[h] = q[1:]
		return q[0], true
	})
}

const other = "https://other.test/x"

func TestTransportRetrySkippedWhenHostBecameRateLimited(t *testing.T) {
	waits := noWait(t)
	th := NewHostThrottle(nil, nil)
	ctx := WithHostThrottle(context.Background(), th)
	n := 0
	d := srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) {
		n++
		if n == 1 {
			// Another board's request on the shared host is answered 429 while
			// this one is out.
			th.limited("example.test", false)
		}
		return srctest.Reply{Err: headerTimeout()}, true
	})
	if _, err := GetBody(ctx, d, u); !Transient(err) {
		t.Fatalf("err = %v, want the timeout", err)
	}
	if n != 1 || len(*waits) != 0 {
		t.Errorf("%d requests, waits %v; a rate-limited host must not be retried", n, *waits)
	}
}

// Any HTTP answer, not only a 200, shows the host is answering: a 404 (a
// posting gone) or a 500 from a silent host restores its transport retry. A
// rate-limit refusal that sent nothing is not an answer.
func TestAnyHTTPAnswerRestoresASilentHostsRetry(t *testing.T) {
	for _, code := range []int{404, 500} {
		noWait(t)
		ctx := WithHostThrottle(context.Background(), NewHostThrottle(nil, nil))
		to := srctest.Reply{Err: headerTimeout()}
		d := byHost(t, map[string][]srctest.Reply{
			"example.test": {to, to, {Status: code}, to, {Status: 200, Body: []byte(`{}`)}},
		})
		if _, err := GetBody(ctx, d, u); !Transient(err) || len(d.URLs()) != 2 {
			t.Fatalf("%d: first: err %v, %d requests", code, err, len(d.URLs()))
		}
		if _, err := GetBody(ctx, d, u); err == nil || Transient(err) || len(d.URLs()) != 3 {
			t.Fatalf("%d: the answer: err %v, %d requests", code, err, len(d.URLs()))
		}
		if _, err := GetBody(ctx, d, u); err != nil || len(d.URLs()) != 5 {
			t.Fatalf("%d: after an HTTP %d a timeout is retried: err %v, %d requests total, want 5", code, code, err, len(d.URLs()))
		}
	}
}

func TestSilentHostGetsNoTransportRetryUntilItAnswers(t *testing.T) {
	waits := noWait(t)
	ctx := WithHostThrottle(context.Background(), NewHostThrottle(nil, nil))
	to := srctest.Reply{Err: headerTimeout()}
	ok := srctest.Reply{Status: 200, Body: []byte(`{}`)}
	d := byHost(t, map[string][]srctest.Reply{
		"example.test": {to, to, to, ok, to, ok},
		"other.test":   {to, ok},
	})
	count := func() int { return len(d.URLs()) }

	// The first request fails, is retried, and fails again: the host is silent.
	if _, err := GetBody(ctx, d, u); !Transient(err) || count() != 2 {
		t.Fatalf("first: err %v, %d requests, want the timeout after 2", err, count())
	}
	// The next request to it gets exactly one attempt.
	if _, err := GetBody(ctx, d, u); !Transient(err) || count() != 3 {
		t.Fatalf("silent host: err %v, %d requests total, want 3", err, count())
	}
	// Another host still gets its retry.
	if _, err := GetBody(ctx, d, other); err != nil || count() != 5 {
		t.Fatalf("other host: err %v, %d requests total, want a retry (5)", err, count())
	}
	// The silent host is still asked, not refused or cooled: one attempt, 200.
	if _, err := GetBody(ctx, d, u); err != nil || count() != 6 {
		t.Fatalf("silent host's answer: err %v, %d requests total, want 6", err, count())
	}
	// Its answer restored the retry.
	if _, err := GetBody(ctx, d, u); err != nil || count() != 8 {
		t.Fatalf("after an answer a timeout is retried: err %v, %d requests total, want 8", err, count())
	}
	if len(*waits) != 3 {
		t.Errorf("waits %v, want one backoff for each of the three retried requests", *waits)
	}
}

func TestSilentHostsAreBounded(t *testing.T) {
	th := NewHostThrottle(nil, nil)
	for i := range 3 * maxThrottleHosts {
		th.markSilent(string(rune('a'+i%26)) + string(rune('a'+i/26)))
	}
	if len(th.silent) > maxThrottleHosts {
		t.Fatalf("%d silent hosts remembered, want at most %d", len(th.silent), maxThrottleHosts)
	}
}

func TestNoThrottleKeepsTheSingleTransportRetry(t *testing.T) {
	noWait(t)
	to := srctest.Reply{Err: headerTimeout()}
	d := seq(t, to, to)
	if _, err := GetBody(context.Background(), d, u); !Transient(err) || len(d.URLs()) != 2 {
		t.Fatalf("err %v, %d requests, want the retry", err, len(d.URLs()))
	}
	// Nothing was remembered: a fresh request in the same context-free setting
	// is retried again.
	d = seq(t, to, srctest.Reply{Status: 200, Body: []byte(`{}`)})
	if _, err := GetBody(context.Background(), d, u); err != nil {
		t.Fatalf("second request: %v", err)
	}
}

func TestBodyReadTimeoutIsRetriedOnce(t *testing.T) {
	waits := noWait(t)
	first := &errBody{data: []byte(`{"A"`), err: headerTimeout()}
	d := &bodyDoer{steps: []bodyStep{{body: first}, {body: okBody(`{"A":7}`)}}}
	var v struct{ A int }
	if err := GetJSON(context.Background(), d, u, &v); err != nil || v.A != 7 {
		t.Fatalf("got %+v, %v; want the second body", v, err)
	}
	if d.count() != 2 || len(*waits) != 1 || (*waits)[0] <= 0 {
		t.Errorf("%d requests, waits %v, want a retry after one backoff", d.count(), *waits)
	}
	if !first.closed {
		t.Error("the failed body was not closed")
	}
}

func TestBodyReadUnexpectedEOFIsRetried(t *testing.T) {
	noWait(t)
	d := &bodyDoer{steps: []bodyStep{{body: &errBody{err: io.ErrUnexpectedEOF}}, {body: okBody(`ok`)}}}
	b, err := GetBody(context.Background(), d, u)
	if err != nil || !bytes.Equal(b, []byte("ok")) {
		t.Fatalf("got %q, %v", b, err)
	}
}

func TestBodyReadFailingTwiceReturnsTheTransientError(t *testing.T) {
	noWait(t)
	d := &bodyDoer{steps: []bodyStep{
		{body: &errBody{err: io.ErrUnexpectedEOF}},
		{body: &errBody{err: io.ErrUnexpectedEOF}},
		{body: okBody(`never`)},
	}}
	_, err := GetBody(context.Background(), d, u)
	if !Transient(err) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want the transient read error", err)
	}
	if d.count() != 2 {
		t.Errorf("%d requests, want 2 (one retry in all)", d.count())
	}
}

func TestBodyReadAfterATransportRetryIsNotRetriedAgain(t *testing.T) {
	noWait(t)
	d := &bodyDoer{steps: []bodyStep{
		{err: headerTimeout()},
		{body: &errBody{err: io.ErrUnexpectedEOF}},
		{body: okBody(`never`)},
	}}
	if _, err := GetBody(context.Background(), d, u); !Transient(err) {
		t.Fatalf("err = %v", err)
	}
	if d.count() != 2 {
		t.Errorf("%d requests, want the one transport retry to be the only one", d.count())
	}
}

func TestPostBodyReadFailureIsNotRetried(t *testing.T) {
	waits := noWait(t)
	d := &bodyDoer{steps: []bodyStep{{body: &errBody{err: io.ErrUnexpectedEOF}}, {body: okBody(`{}`)}}}
	var v struct{}
	if err := PostJSON(context.Background(), d, u, map[string]int{"a": 1}, nil, &v); !Transient(err) {
		t.Fatalf("err = %v, want the read error", err)
	}
	if d.count() != 1 || len(*waits) != 0 {
		t.Errorf("a POST was retried: %d requests, waits %v", d.count(), *waits)
	}
}

func TestOversizedBodyIsNotRetried(t *testing.T) {
	waits := noWait(t)
	d := &bodyDoer{steps: []bodyStep{{body: okBody(strings.Repeat("x", 100))}, {body: okBody(`{}`)}}}
	_, err := GetBodyLimit(context.Background(), d, u, "text/html", 10)
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
	if d.count() != 1 || len(*waits) != 0 {
		t.Errorf("an oversized body was retried: %d requests, waits %v", d.count(), *waits)
	}
}

func TestBodyReadRetryHonoursTheBudgetAndADoneContext(t *testing.T) {
	noWait(t)
	ctx := WithRetryBudget(context.Background(), 0)
	d := &bodyDoer{steps: []bodyStep{{body: &errBody{err: io.ErrUnexpectedEOF}}, {body: okBody(`ok`)}}}
	if _, err := GetBody(ctx, d, u); !Transient(err) || d.count() != 1 {
		t.Fatalf("spent budget: err %v, %d requests, want 1", err, d.count())
	}

	cctx, cancel := context.WithCancel(context.Background())
	d = &bodyDoer{steps: []bodyStep{{body: &cancelBody{cancel: cancel}}, {body: okBody(`ok`)}}}
	if _, err := GetBody(cctx, d, u); err == nil || d.count() != 1 {
		t.Fatalf("done context: err %v, %d requests, want 1", err, d.count())
	}
}

func TestBodyReadFailureMarksTheHostSilentAfterItsRetry(t *testing.T) {
	noWait(t)
	ctx := WithHostThrottle(context.Background(), NewHostThrottle(nil, nil))
	d := &bodyDoer{steps: []bodyStep{
		{body: &errBody{err: io.ErrUnexpectedEOF}},
		{body: &errBody{err: io.ErrUnexpectedEOF}},
		{body: &errBody{err: io.ErrUnexpectedEOF}},
		{body: okBody(`never`)},
	}}
	for i, want := range []int{2, 3} {
		if _, err := GetBody(ctx, d, u); !Transient(err) || d.count() != want {
			t.Fatalf("request %d: err %v, %d requests total, want %d", i+1, err, d.count(), want)
		}
	}
}
