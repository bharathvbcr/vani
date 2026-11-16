package rippling

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

// missTimeout is the transport failure of 2026-11-16: a detail whose headers
// did not arrive within the client's timeout.
var missTimeout = srctest.Reply{Err: fmt.Errorf(`Get "https://api.rippling.com/x": http2: timeout awaiting response headers: %w`, os.ErrDeadlineExceeded)}

// missNoRetry is a context whose retry budget is spent, so a timed-out request
// fails at once.
func missNoRetry() context.Context { return srcutil.WithRetryBudget(context.Background(), 0) }

func hasJob(got []posting.Posting, id string) (posting.Posting, bool) {
	key, _ := posting.RipplingKey(board.Token, id)
	for _, p := range got {
		if p.Key == key {
			return p, true
		}
	}
	return posting.Posting{}, false
}

func TestATimedOutNewDetailIsLeftForTheNextRun(t *testing.T) {
	old := idsDue(t, 10, false)
	var ids []string
	var stored []posting.Posting
	for _, id := range old {
		ids = append(ids, id)
		stored = append(stored, storedPosting(t, id))
	}
	for i := range 10 {
		ids = append(ids, fmt.Sprintf("n%d", i))
	}
	d := boardDoer(ids, map[string]srctest.Reply{"n5": missTimeout})
	s, reps := bounded(t, d, Options{Stored: func(sources.Board) []posting.Posting { return stored }})
	got, err := s.List(missNoRetry(), board)
	if err != nil {
		t.Fatalf("one timed-out detail failed the board: %v", err)
	}
	if len(*reps) != 1 {
		t.Fatalf("reports %+v", *reps)
	}
	st := (*reps)[0]
	if st.Unanswered != 1 || len(got) != 19 || st.Reused+st.Fetched+st.Refreshed+st.Deferred+st.Prefiltered+st.Unlisted+st.Unanswered != st.Listed {
		t.Fatalf("stats %+v, %d postings", st, len(got))
	}
	if _, ok := hasJob(got, "n5"); ok {
		t.Fatal("the missed job was returned")
	}
}

func TestATimedOutReReadKeepsTheStoredJob(t *testing.T) {
	due := idsDue(t, 1, true)[0]
	ids := append([]string{due}, idsDue(t, 9, false)...)
	var stored []posting.Posting
	for _, id := range ids {
		stored = append(stored, storedPosting(t, id))
	}
	d := boardDoer(ids, map[string]srctest.Reply{due: missTimeout})
	s, reps := bounded(t, d, Options{Stored: func(sources.Board) []posting.Posting { return stored }})
	got, err := s.List(missNoRetry(), board)
	if err != nil {
		t.Fatalf("a timed-out re-read failed the board: %v", err)
	}
	p, ok := hasJob(got, due)
	if !ok || len(got) != 10 || (*reps)[0].Unanswered != 1 || p.Title != "Stored "+due || !p.SeenAt.Equal(fixedNow) {
		t.Fatalf("%d postings, stats %+v, kept %+v (%v)", len(got), *reps, p, ok)
	}
}

func TestTimedOutDetailsPastTheBoundFailTheBoard(t *testing.T) {
	ids := make([]string, 20)
	bad := map[string]srctest.Reply{}
	for i := range ids {
		ids[i] = fmt.Sprintf("n%d", i)
		if i <= srcutil.MaxMissed(20, 0) {
			bad[ids[i]] = missTimeout
		}
	}
	s, _ := bounded(t, boardDoer(ids, bad), Options{Stored: func(sources.Board) []posting.Posting { return nil }})
	if got, err := s.List(missNoRetry(), board); err == nil || got != nil {
		t.Fatalf("%d postings, %v; want the board to fail", len(got), err)
	}
}

func TestATimedOutDetailFailsTheBoardWithoutStoredPostings(t *testing.T) {
	ids := []string{"n0", "n1", "n2", "n3", "n4", "n5"}
	d := boardDoer(ids, map[string]srctest.Reply{"n2": missTimeout})
	s := &source{d: d, now: func() time.Time { return fixedNow }}
	if got, err := s.List(missNoRetry(), board); err == nil || got != nil {
		t.Fatalf("%d postings, %v; want the board to fail", len(got), err)
	}
}

// A 500 is an answer, and a 404 is "not on the board": neither is a miss, and
// the 404 keeps its rewrite.
func TestOtherDetailFailuresStillFailTheBoardWithStoredPostings(t *testing.T) {
	for name, tc := range map[string]struct {
		rep  srctest.Reply
		want string
	}{
		"500": {srctest.Reply{Status: 500, Body: []byte("{}")}, "500"},
		"404": {srctest.Reply{Status: 404, Body: []byte("{}")}, "is not on the board"},
	} {
		ids := []string{"n0", "n1", "n2", "n3", "n4", "n5"}
		s, reps := bounded(t, boardDoer(ids, map[string]srctest.Reply{"n2": tc.rep}), Options{Stored: func(sources.Board) []posting.Posting { return nil }})
		got, err := s.List(missNoRetry(), board)
		if err == nil || got != nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %d postings, %v; want a failure naming %q", name, len(got), err, tc.want)
		}
		if len(*reps) != 1 || (*reps)[0].Unanswered != 0 {
			t.Errorf("%s: reports %+v, want none missed", name, *reps)
		}
	}
}
