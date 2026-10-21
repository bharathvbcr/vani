package eightfold

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

type hookDoer struct {
	inner *srctest.Doer
	pre   func(r *http.Request) (*http.Response, error) // non-nil result short-circuits
}

func (d hookDoer) Do(ctx context.Context, r *http.Request) (*http.Response, error) {
	if d.pre != nil {
		if resp, err := d.pre(r); resp != nil || err != nil {
			return resp, err
		}
	}
	return d.inner.Do(ctx, r)
}

type recorder struct {
	mu    sync.Mutex
	calls []Stats
}

func (r *recorder) opt() *Options {
	return &Options{Report: func(_ sources.Board, st Stats) {
		r.mu.Lock()
		r.calls = append(r.calls, st)
		r.mu.Unlock()
	}}
}

func detailReq(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/position_details") }

// Cancelling between details returns the context error, not "N of M gone",
// counts nothing gone for the details never fetched, and still reports once.
func TestCancelBetweenDetails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := goneSite(t, 20, nil)
	served := 0
	s.d = hookDoer{inner: s.d.(*srctest.Doer), pre: func(r *http.Request) (*http.Response, error) {
		if detailReq(r) {
			if served++; served == 3 {
				cancel() // the third detail is still served; the loop must stop before the fourth
			}
		}
		return nil, nil
	}}
	rec := &recorder{}
	s.opt = rec.opt()
	ps, err := s.List(ctx, board("t"))
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "gone") {
		t.Fatalf("err %v, want context.Canceled and no gone message (got %d postings)", err, len(ps))
	}
	if served != 3 {
		t.Errorf("%d details fetched after cancel, want 3", served)
	}
	if len(rec.calls) != 1 || rec.calls[0].Gone != 0 || rec.calls[0].Listed != 20 {
		t.Errorf("reports %+v, want one {20 0}", rec.calls)
	}
}

// The transport failing with the context error mid-detail is a cancel, not a
// gone posting.
func TestCancelMidDetailRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := goneSite(t, 20, nil)
	s.d = hookDoer{inner: s.d.(*srctest.Doer), pre: func(r *http.Request) (*http.Response, error) {
		if detailReq(r) {
			cancel()
			return nil, ctx.Err()
		}
		return nil, nil
	}}
	rec := &recorder{}
	s.opt = rec.opt()
	_, err := s.List(ctx, board("t"))
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "are gone") {
		t.Fatalf("err %v", err)
	}
	if len(rec.calls) != 1 || rec.calls[0].Gone != 0 {
		t.Errorf("reports %+v", rec.calls)
	}
}

// A panic in a detail decode path (here, the transport) is not recovered, but
// Report still fires once on the way out.
func TestPanicStillReportsOnce(t *testing.T) {
	s := goneSite(t, 12, nil)
	s.d = hookDoer{inner: s.d.(*srctest.Doer), pre: func(r *http.Request) (*http.Response, error) {
		if detailReq(r) {
			panic("boom")
		}
		return nil, nil
	}}
	rec := &recorder{}
	s.opt = rec.opt()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic was swallowed")
			}
		}()
		s.List(context.Background(), board("t"))
	}()
	if len(rec.calls) != 1 {
		t.Errorf("reports %+v, want one", rec.calls)
	}
}

// Report is called exactly once per List that reached the detail step, on
// success and on every failure path; Gone never exceeds Listed.
func TestReportExactlyOnce(t *testing.T) {
	for name, tc := range map[string]struct {
		n       int
		replies map[string]srctest.Reply
	}{
		"success":       {12, nil},
		"one gone":      {12, map[string]srctest.Reply{id(0): notFound()}},
		"over bound":    {12, map[string]srctest.Reply{id(0): notFound(), id(1): notFound()}},
		"all gone":      {1, map[string]srctest.Reply{id(0): notFound()}},
		"500":           {12, map[string]srctest.Reply{id(4): {Status: 500, Body: []byte("{}")}}},
		"malformed":     {12, map[string]srctest.Reply{id(4): {Status: 200, Body: []byte("{")}}},
		"gone then 500": {12, map[string]srctest.Reply{id(0): notFound(), id(1): {Status: 500}}},
	} {
		s := goneSite(t, tc.n, tc.replies)
		rec := &recorder{}
		s.opt = rec.opt()
		s.List(context.Background(), board("t"))
		if len(rec.calls) != 1 {
			t.Errorf("%s: %d reports", name, len(rec.calls))
			continue
		}
		if c := rec.calls[0]; c.Gone > c.Listed || c.Listed != tc.n {
			t.Errorf("%s: inconsistent %+v", name, c)
		}
	}
}

// Report is nil safe: New, NewWith with no Report, and NewWith(Options{}).
func TestReportNilSafe(t *testing.T) {
	for name, opt := range map[string]*Options{"nil opt": nil, "empty": {}, "nil report": {Report: nil}} {
		s := goneSite(t, 12, map[string]srctest.Reply{id(0): notFound(), id(1): notFound()})
		s.opt = opt
		if _, err := s.List(context.Background(), board("t")); err == nil {
			t.Errorf("%s: want the bound error", name)
		}
	}
}

// Search failing (404, 500) never reaches the detail step: Report is not
// called (documented behaviour: no Listed count exists yet).
func TestSearchFailureDoesNotReport(t *testing.T) {
	for _, st := range []int{404, 500} {
		s := goneSite(t, 12, nil)
		s.d = hookDoer{inner: s.d.(*srctest.Doer), pre: func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/search") {
				return &http.Response{StatusCode: st, Body: http.NoBody, Header: http.Header{}}, nil
			}
			return nil, nil
		}}
		rec := &recorder{}
		s.opt = rec.opt()
		if _, err := s.List(context.Background(), board("t")); err == nil {
			t.Fatal("want error")
		}
		if len(rec.calls) != 0 {
			t.Errorf("search %d: reports %+v", st, rec.calls)
		}
	}
}

// What a 404 looks like does not matter, only the status: a 404 carrying a
// valid, complete detail JSON body is still gone; a 200 carrying an HTML
// "not found" page, or an envelope-404 body, is a failure.
func TestDetailBodyShapes(t *testing.T) {
	good := goneSite(t, 12, nil)
	// Take a valid detail body from the fixture handler.
	var valid []byte
	for _, r := range []srctest.Request{{Method: "GET", URL: "https://" + host + "/api/pcsx/position_details?position_id=" + id(0) + "&domain=microsoft.com&hl=en"}} {
		rep, _ := good.d.(*srctest.Doer).Handler(r)
		valid = rep.Body
	}
	if len(valid) == 0 {
		t.Fatal("no valid body")
	}
	// 404 + valid JSON body: gone (one of 12 is within bound).
	ps, err := goneSite(t, 12, map[string]srctest.Reply{id(2): {Status: 404, Body: valid}}).List(context.Background(), board("t"))
	if err != nil || len(ps) != 11 {
		t.Errorf("404 with a valid body: %d postings, %v", len(ps), err)
	}
	// 200 + HTML not-found page: failure, not gone.
	_, err = goneSite(t, 12, map[string]srctest.Reply{id(2): {Status: 200, Body: []byte("<html><title>Page Not Found</title></html>")}}).List(context.Background(), board("t"))
	if err == nil || strings.Contains(err.Error(), "are gone") || errors.Is(err, errDetailGone) {
		t.Errorf("200 html page: %v", err)
	}
}

// A redirect (301/302) is the egress client's business: it follows and
// re-vets; the adapter sees the final status. A doer that surfaces an
// unfollowed 3xx must produce a failure, not a gone posting.
func TestUnfollowedRedirectIsFailure(t *testing.T) {
	for _, code := range []int{301, 302, 307} {
		s := goneSite(t, 12, map[string]srctest.Reply{id(3): {Status: code, Header: http.Header{"Location": {"https://" + host + "/404"}}}})
		_, err := s.List(context.Background(), board("t"))
		if err == nil || strings.Contains(err.Error(), "are gone") {
			t.Errorf("%d: %v", code, err)
		}
	}
}

// Duplicate ids across searches inflate nothing: n is counted after the union,
// so the tolerated gone count follows distinct positions.
func TestBoundCountsDistinctIDs(t *testing.T) {
	replies := map[string]srctest.Reply{id(0): notFound(), id(1): notFound()}
	// 12 distinct ids listed by both terms: 24 raw rows, but n=12, bound 1.
	s := goneSite(t, 12, replies)
	if _, err := s.List(context.Background(), board("t", "u")); err == nil {
		t.Error("two gone of 12 distinct (24 raw) must exceed the bound")
	}
}
