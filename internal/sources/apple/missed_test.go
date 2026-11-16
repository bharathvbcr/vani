package apple

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// timeout is the transport failure of 2026-11-16: a details page whose
// headers did not arrive within the client's timeout.
var timeout = fmt.Errorf(`Get "https://jobs.apple.com/en-us/details/x": http2: timeout awaiting response headers: %w`, os.ErrDeadlineExceeded)

// noRetry is a context whose retry budget is spent, so a timed-out request
// fails at once instead of waiting a real backoff in the test.
func noRetry() context.Context { return srcutil.WithRetryBudget(context.Background(), 0) }

// failing replaces the replies for the details pages of the given ids with err.
func failing(d *srctest.Doer, err error, ids ...string) {
	for _, id := range ids {
		d.Replies[detailURL(id, "0001", "role-"+id)] = srctest.Reply{Err: err}
	}
}

func storedApple(t *testing.T, ids ...string) []posting.Posting {
	t.Helper()
	var out []posting.Posting
	for _, id := range ids {
		key, err := posting.AppleKey("apple", id)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, posting.Posting{Key: key, ATS: posting.Apple, Company: "Apple", Title: "Stored " + id, Description: "stored text", SeenAt: fixedNow.Add(-48 * time.Hour)})
	}
	return out
}

// dueID is a position id among the first n whose re-read is due on fixedNow.
func dueID(t *testing.T, n int) string {
	t.Helper()
	for i := range n {
		if srcutil.RefreshTurn(appleID(i), srcutil.Day(fixedNow)) {
			return appleID(i)
		}
	}
	t.Fatal("no position is due a re-read")
	return ""
}

// One timed-out details page among 30 no longer fails the board: the new
// position is left for the next run, unreturned and unstored, as a deferred
// one is, and the stats say so.
func TestATimedOutNewPositionIsLeftForTheNextRun(t *testing.T) {
	const n = 30
	s, d, stats := withStored(appleSite(t, n, fmt.Sprint(n)), storedApple(t, appleID(0), appleID(1)))
	failing(d, timeout, appleID(7))
	got, err := s.List(noRetry(), board("a"))
	if err != nil {
		t.Fatalf("one timed-out position failed the board: %v", err)
	}
	st := (*stats)[0]
	if st.Unanswered != 1 || len(got) != n-1 || !accounted(st) {
		t.Fatalf("stats %+v, %d postings", st, len(got))
	}
	for _, p := range got {
		if strings.HasSuffix(string(p.Key), appleID(7)) {
			t.Fatal("the missed position was returned")
		}
	}
}

// A stored position whose re-read times out is returned as stored, so it is
// still listed and nothing ages.
func TestATimedOutReReadKeepsTheStoredPosting(t *testing.T) {
	const n = 30
	due := dueID(t, n)
	s, d, stats := withStored(appleSite(t, n, fmt.Sprint(n)), storedApple(t, due))
	failing(d, timeout, due)
	got, err := s.List(noRetry(), board("a"))
	if err != nil {
		t.Fatalf("a timed-out re-read failed the board: %v", err)
	}
	st := (*stats)[0]
	if st.Unanswered != 1 || len(got) != n || !accounted(st) {
		t.Fatalf("stats %+v, %d postings", st, len(got))
	}
	for _, p := range got {
		if strings.HasSuffix(string(p.Key), due) {
			if p.Title != "Stored "+due || !p.SeenAt.Equal(fixedNow) {
				t.Fatalf("kept posting %+v, want the stored one seen now", p)
			}
			return
		}
	}
	t.Fatal("the stored position was left out")
}

// More misses than MaxMissed is the details endpoint failing: the board fails
// as before rather than answering with a fraction of its positions.
func TestManyTimedOutPositionsFailTheBoard(t *testing.T) {
	const n = 30
	s, d, _ := withStored(appleSite(t, n, fmt.Sprint(n)), nil)
	var ids []string
	for i := range srcutil.MaxMissed(n, 0) + 1 {
		ids = append(ids, appleID(i))
	}
	failing(d, timeout, ids...)
	if got, err := s.List(noRetry(), board("a")); err == nil || got != nil {
		t.Fatalf("got %d postings, %v; want the board to fail", len(got), err)
	}
}

// Without the stored postings a missed position could not be told from a
// stored one left out, so a timeout still fails the board.
func TestATimedOutPositionFailsTheBoardWithoutStoredPostings(t *testing.T) {
	s, d := newSource(appleSite(t, 30, "30"))
	failing(d, timeout, appleID(3))
	if got, err := s.List(noRetry(), board("a")); err == nil || got != nil {
		t.Fatalf("got %d postings, %v; want the board to fail", len(got), err)
	}
}

// A server error is an answer, not a miss: it fails the board as before.
func TestAServerErrorStillFailsTheBoard(t *testing.T) {
	s, d, _ := withStored(appleSite(t, 30, "30"), nil)
	id := appleID(4)
	d.Replies[detailURL(id, "0001", "role-"+id)] = srctest.Reply{Status: 500}
	if got, err := s.List(noRetry(), board("a")); err == nil || got != nil {
		t.Fatalf("got %d postings, %v; want the board to fail", len(got), err)
	}
}
