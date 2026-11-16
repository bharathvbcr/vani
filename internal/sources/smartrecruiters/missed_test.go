package smartrecruiters

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// missTimeout is the transport failure of 2026-11-16: a detail whose headers
// did not arrive within the client's timeout.
var missTimeout = srctest.Reply{Err: fmt.Errorf(`Get "https://api.smartrecruiters.com/x": http2: timeout awaiting response headers: %w`, os.ErrDeadlineExceeded)}

// missNoRetry is a context whose retry budget is spent, so a timed-out request
// fails at once.
func missNoRetry() context.Context { return srcutil.WithRetryBudget(context.Background(), 0) }

func failing(a *api, rep srctest.Reply, ids ...string) {
	a.bad = map[string]srctest.Reply{}
	for _, id := range ids {
		a.bad[id] = rep
	}
}

func hasID(got []posting.Posting, id string) (posting.Posting, bool) {
	key, _ := posting.SmartRecruitersKey(company, id)
	for _, p := range got {
		if p.Key == key {
			return p, true
		}
	}
	return posting.Posting{}, false
}

func TestATimedOutNewDetailIsLeftForTheNextRun(t *testing.T) {
	a := board(20)
	failing(a, missTimeout, idOf(5))
	var st Stats
	var stored []posting.Posting
	for i := 10; i < 20; i++ {
		stored = append(stored, storedPosting(idOf(i), stamp))
	}
	s := src(t, a.doer(), &Options{
		Stored: func(sources.Board) []posting.Posting { return stored },
		Report: func(_ sources.Board, s Stats) { st = s },
	})
	got, err := s.List(missNoRetry(), srBoard())
	if err != nil {
		t.Fatalf("one timed-out detail failed the board: %v", err)
	}
	if st.Unanswered != 1 || len(got) != 19 || st.Reused+st.Refreshed+st.Fetched+st.Deferred+st.Prefiltered+st.Gone+st.Unanswered != st.Listed {
		t.Fatalf("stats %+v, %d postings", st, len(got))
	}
	if _, ok := hasID(got, idOf(5)); ok {
		t.Fatal("the missed posting was returned")
	}
}

// A stored row re-read because its releasedDate moved, and one re-read on its
// turn, are each returned as stored when the re-read times out.
func TestATimedOutReReadKeepsTheStoredPosting(t *testing.T) {
	t.Run("changed", func(t *testing.T) {
		a := board(20)
		failing(a, missTimeout, idOf(3))
		st := storedPosting(idOf(3), stamp)
		st.PostedAt = st.PostedAt.AddDate(0, 0, -1)
		var stats Stats
		s := src(t, a.doer(), &Options{
			Stored: func(sources.Board) []posting.Posting { return []posting.Posting{st} },
			Report: func(_ sources.Board, s Stats) { stats = s },
		})
		got, err := s.List(missNoRetry(), srBoard())
		if err != nil {
			t.Fatalf("a timed-out re-read failed the board: %v", err)
		}
		p, ok := hasID(got, idOf(3))
		if !ok || len(got) != 20 || stats.Unanswered != 1 || p.Title != "Stored "+idOf(3) || !p.SeenAt.Equal(fixedNow) {
			t.Fatalf("%d postings, stats %+v, kept %+v (%v)", len(got), stats, p, ok)
		}
	})
	t.Run("scheduled", func(t *testing.T) {
		due := idOnTurn(true)
		n := 0
		for i := 0; idOf(i) != due; i++ {
			n = i + 1
		}
		n++ // the board holds ids 0..n-1, due among them
		a := board(n)
		failing(a, missTimeout, due)
		var stored []posting.Posting
		for i := range n {
			stored = append(stored, storedPosting(idOf(i), stamp))
		}
		var stats Stats
		s := src(t, a.doer(), &Options{
			Stored: func(sources.Board) []posting.Posting { return stored },
			Report: func(_ sources.Board, s Stats) { stats = s },
		})
		got, err := s.List(missNoRetry(), srBoard())
		if err != nil {
			t.Fatalf("a timed-out scheduled re-read failed the board: %v", err)
		}
		p, ok := hasID(got, due)
		if !ok || len(got) != n || stats.Unanswered != 1 || p.Title != "Stored "+due || !p.SeenAt.Equal(fixedNow) {
			t.Fatalf("%d postings, stats %+v, kept %+v (%v)", len(got), stats, p, ok)
		}
	})
}

func TestTimedOutDetailsPastTheBoundFailTheBoard(t *testing.T) {
	a := board(20)
	var ids []string
	for i := range srcutil.MaxMissed(20, 0) + 1 {
		ids = append(ids, idOf(i))
	}
	failing(a, missTimeout, ids...)
	s := src(t, a.doer(), &Options{})
	if got, err := s.List(missNoRetry(), srBoard()); err == nil || got != nil {
		t.Fatalf("%d postings, %v; want the board to fail", len(got), err)
	}
}

func TestATimedOutDetailFailsTheBoardWithoutStoredPostings(t *testing.T) {
	a := board(20)
	failing(a, missTimeout, idOf(5))
	if got, err := src(t, a.doer(), nil).List(missNoRetry(), srBoard()); err == nil || got != nil {
		t.Fatalf("%d postings, %v; want the board to fail", len(got), err)
	}
}

func TestAServerErrorOnADetailStillFailsTheBoardWithStoredPostings(t *testing.T) {
	a := board(20)
	failing(a, srctest.Reply{Status: 500, Body: []byte("{}")}, idOf(5))
	if got, err := src(t, a.doer(), &Options{}).List(missNoRetry(), srBoard()); err == nil || got != nil {
		t.Fatalf("%d postings, %v; want the board to fail", len(got), err)
	}
}
