package eightfold

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// Microsoft's pcsx host answered HTTP 429 (no Retry-After) to a board's second
// search page on 2026-10-22. These tests drive that and the other places a
// board can be throttled. The context carries an empty retry budget, so a 429
// is given up on at once: nothing here waits, and every outcome is what a board
// that stays throttled ends as.

// throttled is a 25-position site whose requests for which limit returns true
// answer 429, and the requests it saw.
func throttled(t *testing.T, limit func(path string, q url.Values) bool) (*source, *[]string) {
	t.Helper()
	f := fixtureSite(t)
	var ids []string
	for i := 0; i < 25; i++ {
		ids = append(ids, fmt.Sprint(5000000000+i))
	}
	f.positions = map[string][]string{"LLM inference": ids}
	s, d := newSource(f)
	inner := d.Handler
	var seen []string
	s.d = srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		u, _ := url.Parse(r.URL)
		seen = append(seen, u.Path+"?"+u.Query().Get("start")+u.Query().Get("position_id"))
		if limit(u.Path, u.Query()) {
			return srctest.Reply{Status: 429, Body: []byte("Too Many Requests")}, true
		}
		return inner(r)
	})
	return s, &seen
}

func noRetry() context.Context { return srcutil.WithRetryBudget(context.Background(), 0) }

func wantRateLimited(t *testing.T, err error) {
	t.Helper()
	var se *egress.StatusError
	if !errors.Is(err, srcutil.ErrRateLimited) || !errors.As(err, &se) || se.Code != 429 ||
		!strings.Contains(err.Error(), "rate limited by "+host+" (HTTP 429), retry next run") {
		t.Fatalf("err = %v, want a rate-limited 429 naming %s", err, host)
	}
	if errors.Is(err, srcutil.ErrBoardNotFound) {
		t.Errorf("a rate limit is not a missing board: %v", err)
	}
}

// The live failure: the second search page is throttled. The board fails as
// rate limited, lists nothing, and asks for no position details.
func TestRateLimitedSecondSearchPage(t *testing.T) {
	s, seen := throttled(t, func(path string, q url.Values) bool {
		return path == "/api/pcsx/search" && q.Get("start") == "10"
	})
	got, err := s.List(noRetry(), board("LLM inference"))
	wantRateLimited(t, err)
	if got != nil {
		t.Errorf("a throttled listing returned %d postings", len(got))
	}
	if !strings.Contains(err.Error(), `search "LLM inference"`) {
		t.Errorf("error does not say what was being read: %v", err)
	}
	if want := []string{"/robots.txt?", "/api/pcsx/search?0", "/api/pcsx/search?10"}; strings.Join(*seen, " ") != strings.Join(want, " ") {
		t.Errorf("requests %v, want %v: nothing more is asked of a throttling host", *seen, want)
	}
}

// A position detail throttled part-way through the fan-out fails the board
// there, rather than listing a partial board that would age the rest dead.
func TestRateLimitedDetailMidFanOut(t *testing.T) {
	s, seen := throttled(t, func(path string, q url.Values) bool {
		return q.Get("position_id") == "5000000012"
	})
	got, err := s.List(noRetry(), board("LLM inference"))
	wantRateLimited(t, err)
	if got != nil || !strings.Contains(err.Error(), "position 5000000012") {
		t.Errorf("got %d postings, err %v", len(got), err)
	}
	details := 0
	for _, r := range *seen {
		if strings.Contains(r, "position_details") {
			details++
		}
	}
	if details != 13 {
		t.Errorf("%d detail requests, want 13: the 12 that answered and the throttled one", details)
	}
}

// A 429 on the very first search is a throttle, not "this site has no pcsx":
// the v2 fallback (a 403 signal only) must not be tried: after the board's one
// robots.txt read there is the one pcsx search and nothing else.
func TestRateLimitedFirstSearchDoesNotTryV2(t *testing.T) {
	s, seen := throttled(t, func(path string, q url.Values) bool { return true })
	_, err := s.List(noRetry(), board("LLM inference"))
	wantRateLimited(t, err)
	if len(*seen) != 2 || !strings.HasPrefix((*seen)[0], "/robots.txt") || !strings.HasPrefix((*seen)[1], "/api/pcsx/search") {
		t.Errorf("requests %v, want the robots.txt read and the one pcsx search", *seen)
	}
}

// A throttled board reports its counts as nothing, and a board context that
// has been cancelled is the cancellation, not a rate limit.
func TestRateLimitedBoardReportsNothingAndCancelWins(t *testing.T) {
	s, _ := throttled(t, func(path string, q url.Values) bool { return path == "/api/pcsx/position_details" })
	reported := false
	s.opt = &Options{Report: func(_ sources.Board, _ Stats) { reported = true }}
	if _, err := s.List(noRetry(), board("LLM inference")); err == nil {
		t.Fatal("throttled details listed a board")
	}
	if !reported {
		t.Error("the listed count of a board that got as far as its details was not reported")
	}
	ctx, cancel := context.WithCancel(noRetry())
	cancel()
	if _, err := s.List(ctx, board("LLM inference")); !errors.Is(err, context.Canceled) || errors.Is(err, srcutil.ErrRateLimited) {
		t.Errorf("cancelled board: %v", err)
	}
}
