package workday

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// missTimeout is the transport failure of 2026-11-16: a detail page whose
// headers did not arrive within the client's timeout.
var missTimeout = srctest.Reply{Err: fmt.Errorf(`Get "https://%s/x": http2: timeout awaiting response headers: %w`, host, os.ErrDeadlineExceeded)}

// missNoRetry is a context whose retry budget is spent, so a timed-out request
// fails at once instead of waiting a real backoff in the test.
func missNoRetry() context.Context { return srcutil.WithRetryBudget(context.Background(), 0) }

// failDetails serves fb, except that the detail pages of the listed jobs
// answer with reply.
func failDetails(fb *fakeBoard, reply srctest.Reply, jobs ...int) func(srctest.Request) (srctest.Reply, bool) {
	bad := map[string]bool{}
	for _, i := range jobs {
		p, _ := escapePath(fb.jobs[i].path)
		bad[siteURL+p] = true
	}
	return func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method == "GET" && bad[r.URL] {
			return reply, true
		}
		return fb.handle(r)
	}
}

func missSource(h func(srctest.Request) (srctest.Reply, bool), o *Options) *source {
	return &source{d: srctest.NewFunc(h), now: func() time.Time { return fixedNow }, opt: o}
}

// storedJobs are the stored postings of the given jobs, under the URL their
// detail would carry, so each is matched to its listed path.
func storedJobs(fb *fakeBoard, idx ...int) []posting.Posting {
	var out []posting.Posting
	for _, i := range idx {
		j := fb.jobs[i]
		p, _ := escapePath(j.path)
		key, _ := posting.WorkdayKey("nvidia", "NVIDIAExternalCareerSite", fmt.Sprintf("JR%d", j.req))
		out = append(out, posting.Posting{Key: key, ATS: posting.Workday, Company: "NVIDIA", Title: "stored " + j.title, Description: "stored text",
			URL: "https://" + host + "/NVIDIAExternalCareerSite" + p, SeenAt: fixedNow.Add(-48 * time.Hour)})
	}
	return out
}

func rangeIdx(from, to int) []int {
	var out []int
	for i := from; i < to; i++ {
		out = append(out, i)
	}
	return out
}

func missBoard(n int) *fakeBoard {
	fb := nvidiaBoard(n)
	fb.params = nil
	return fb
}

func hasReq(got []posting.Posting, req int) bool {
	for _, p := range got {
		if strings.HasSuffix(string(p.Key), strings.ToLower(fmt.Sprintf("JR%d", req))) {
			return true
		}
	}
	return false
}

func TestATimedOutNewDetailIsLeftForTheNextRun(t *testing.T) {
	fb := missBoard(30)
	var st Stats
	s := missSource(failDetails(fb, missTimeout, 5), &Options{
		Stored: func(sources.Board) []posting.Posting { return storedJobs(fb, rangeIdx(10, 30)...) },
		Report: func(_ sources.Board, x Stats) { st = x },
	})
	got, err := s.List(missNoRetry(), board)
	if err != nil {
		t.Fatalf("one timed-out detail failed the board: %v", err)
	}
	if st.Unanswered != 1 || len(got) != 29 || st.Reused+st.Refreshed+st.Fetched+st.Deferred+st.Prefiltered+st.Gone+st.Unanswered != st.Listed {
		t.Fatalf("stats %+v, %d postings", st, len(got))
	}
	if hasReq(got, fb.jobs[5].req) {
		t.Fatal("the missed posting was returned")
	}
}

func TestATimedOutReReadKeepsTheStoredPosting(t *testing.T) {
	fb := missBoard(90)
	due := -1
	for i, j := range fb.jobs {
		if srcutil.RefreshTurn(j.path, srcutil.Day(fixedNow)) {
			due = i
			break
		}
	}
	if due < 0 {
		t.Fatal("no job is due a re-read")
	}
	var st Stats
	s := missSource(failDetails(fb, missTimeout, due), &Options{
		Stored: func(sources.Board) []posting.Posting { return storedJobs(fb, rangeIdx(0, 90)...) },
		Report: func(_ sources.Board, x Stats) { st = x },
	})
	got, err := s.List(missNoRetry(), board)
	if err != nil {
		t.Fatalf("a timed-out re-read failed the board: %v", err)
	}
	if st.Unanswered != 1 || len(got) != 90 {
		t.Fatalf("stats %+v, %d postings", st, len(got))
	}
	found := false
	for _, p := range got {
		if strings.HasSuffix(string(p.Key), strings.ToLower(fmt.Sprintf("JR%d", fb.jobs[due].req))) {
			found = true
			if p.Title != "stored "+fb.jobs[due].title || !p.SeenAt.Equal(fixedNow) {
				t.Fatalf("kept posting %+v, want the stored one seen now", p)
			}
		}
	}
	if !found {
		t.Fatal("the stored posting was left out")
	}
}

func TestTimedOutDetailsPastTheBoundFailTheBoard(t *testing.T) {
	fb := missBoard(30)
	s := missSource(failDetails(fb, missTimeout, 0, 1, 2, 3), &Options{
		Stored: func(sources.Board) []posting.Posting { return storedJobs(fb, rangeIdx(10, 30)...) },
	})
	if got, err := s.List(missNoRetry(), board); err == nil || got != nil {
		t.Fatalf("%d postings, %v; want the board to fail", len(got), err)
	}
}

func TestATimedOutDetailFailsTheBoardWithoutStoredPostings(t *testing.T) {
	fb := missBoard(30)
	if got, err := missSource(failDetails(fb, missTimeout, 5), nil).List(missNoRetry(), board); err == nil || got != nil {
		t.Fatalf("no Options: %d postings, %v; want the board to fail", len(got), err)
	}
}

func TestAServerErrorOnADetailStillFailsTheBoard(t *testing.T) {
	fb := missBoard(30)
	s := missSource(failDetails(fb, srctest.Reply{Status: 500}, 5), &Options{
		Stored: func(sources.Board) []posting.Posting { return storedJobs(fb, rangeIdx(10, 30)...) },
	})
	if got, err := s.List(missNoRetry(), board); err == nil || got != nil {
		t.Fatalf("%d postings, %v; want the board to fail", len(got), err)
	}
}

// A stored posting whose URL names no path on the site may be the row that
// just missed, and leaving it out would age it: the board fails.
func TestATimedOutNewDetailFailsTheBoardWhenAStoredURLMatchesNoPath(t *testing.T) {
	fb := missBoard(30)
	stored := storedJobs(fb, rangeIdx(10, 30)...)
	stray := stored[0]
	stray.Key, _ = posting.WorkdayKey("nvidia", "NVIDIAExternalCareerSite", "JR999999")
	stray.URL = "https://" + host + "/External/job/Elsewhere_JR999999"
	stored = append(stored, stray)
	s := missSource(failDetails(fb, missTimeout, 5), &Options{Stored: func(sources.Board) []posting.Posting { return stored }})
	got, err := s.List(missNoRetry(), board)
	if err == nil || got != nil {
		t.Fatalf("%d postings, %v; want the board to fail", len(got), err)
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("err = %v, want the timeout", err)
	}
}

// A "new" row whose fetched posting is already stored means the stored URL and
// the listed path disagree, so a missed new row may be a stored posting.
func TestAMissedNewRowFailsTheBoardWhenAnotherNewRowTurnsOutStored(t *testing.T) {
	fb := missBoard(12)
	// The stored job is one not due a re-read on the fixed day (never job 1,
	// which is listed under another path but is the same requisition, or job 5,
	// whose detail times out).
	kept := -1
	for i, j := range fb.jobs {
		if i != 1 && i != 5 && !srcutil.RefreshTurn(j.path, srcutil.Day(fixedNow)) {
			kept = i
			break
		}
	}
	if kept < 0 {
		t.Fatal("every job is due a re-read on the fixed day")
	}
	fb.jobs[1].req = fb.jobs[kept].req
	stored := storedJobs(fb, kept)
	opts := func() *Options {
		return &Options{Stored: func(sources.Board) []posting.Posting { return stored }}
	}

	// Control: no miss, so the duplicate is one posting and the board answers.
	got, err := missSource(fb.handle, opts()).List(missNoRetry(), board)
	if err != nil || len(got) != 11 {
		t.Fatalf("control: %d postings, %v", len(got), err)
	}

	got, err = missSource(failDetails(fb, missTimeout, 5), opts()).List(missNoRetry(), board)
	if err == nil || got != nil {
		t.Fatalf("%d postings, %v; want the board to fail", len(got), err)
	}
	if !strings.Contains(err.Error(), "missed new rows") {
		t.Fatalf("err = %v, want the stored-under-another-URL refusal", err)
	}
}
