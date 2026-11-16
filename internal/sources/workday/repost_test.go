package workday

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// A reposted posting is listed under its path plus "-1", so it matches no
// stored path and is planned as a new row. When its detail times out, leaving
// it out would age the stored posting of the same requisition as absent, so the
// board fails (it returns no partial answer, as before detail misses were
// absorbed) and the error still carries the timeout.
func TestATimedOutRepostFailsTheBoardInsteadOfAgingTheStoredPosting(t *testing.T) {
	fb := missBoard(30)
	stored := storedJobs(fb, append([]int{0}, rangeIdx(10, 30)...)...) // under job 0's original path
	fb.jobs[0].path += "-1"
	opts := func() *Options { return &Options{Stored: func(sources.Board) []posting.Posting { return stored }} }

	// Control: the repost's detail answers, so the board does.
	if got, err := missSource(fb.handle, opts()).List(missNoRetry(), board); err != nil || !hasReq(got, fb.jobs[0].req) {
		t.Fatalf("control: %d postings, %v", len(got), err)
	}

	got, err := missSource(failDetails(fb, missTimeout, 0), opts()).List(missNoRetry(), board)
	if err == nil || got != nil {
		t.Fatalf("%d postings, %v; want the board to fail", len(got), err)
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) || !strings.Contains(err.Error(), "repost") {
		t.Fatalf("err = %v, want the repost refusal wrapping the timeout", err)
	}
}

// A repost the plan defers past the request cap is the same hole: it was left
// out with no request at all.
func TestADeferredRepostFailsTheBoardInsteadOfAgingTheStoredPosting(t *testing.T) {
	n := MaxDetails + 5
	fb := missBoard(n)
	last := n - 1 // listed last, so among the deferred new rows
	stored := storedJobs(fb, last)
	opts := func() *Options { return &Options{Stored: func(sources.Board) []posting.Posting { return stored }} }

	// Control: listed under its stored path, the same board defers its new rows
	// and answers.
	var st Stats
	o := opts()
	o.Report = func(_ sources.Board, x Stats) { st = x }
	if got, err := missSource(fb.handle, o).List(missNoRetry(), board); err != nil || st.Deferred == 0 || !hasReq(got, fb.jobs[last].req) {
		t.Fatalf("control: %d postings, stats %+v, %v", len(got), st, err)
	}

	fb.jobs[last].path += "-1"
	got, err := missSource(fb.handle, opts()).List(missNoRetry(), board)
	if err == nil || got != nil {
		t.Fatalf("%d postings, %v; want the board to fail", len(got), err)
	}
	if !strings.Contains(err.Error(), "deferred") || !strings.Contains(err.Error(), "repost") {
		t.Fatalf("err = %v, want the deferred-repost refusal", err)
	}
}

// failOnce serves h, except that the first request for url times out.
func failOnce(h func(srctest.Request) (srctest.Reply, bool), url string) (func(srctest.Request) (srctest.Reply, bool), *int) {
	asked := 0
	return func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method == "GET" && r.URL == url {
			if asked++; asked == 1 {
				return missTimeout, true
			}
		}
		return h(r)
	}, &asked
}

// A detail that times out once and answers on the retry is a Fetched row, not
// an Unanswered one.
func TestADetailThatAnswersOnTheRetryIsFetched(t *testing.T) {
	t.Cleanup(srcutil.SetWaitForTest(func(ctx context.Context, _ time.Duration) error { return ctx.Err() }))
	fb := missBoard(30)
	p, _ := escapePath(fb.jobs[5].path)
	h, asked := failOnce(fb.handle, siteURL+p)
	var st Stats
	s := missSource(h, &Options{
		Stored: func(sources.Board) []posting.Posting { return storedJobs(fb, rangeIdx(10, 30)...) },
		Report: func(_ sources.Board, x Stats) { st = x },
	})
	got, err := s.List(srcutil.WithRetryBudget(context.Background(), time.Minute), board)
	if err != nil {
		t.Fatal(err)
	}
	if *asked != 2 || st.Unanswered != 0 || st.Fetched != 10 || len(got) != 30 || !hasReq(got, fb.jobs[5].req) {
		t.Fatalf("asked %d times, stats %+v, %d postings", *asked, st, len(got))
	}
}
