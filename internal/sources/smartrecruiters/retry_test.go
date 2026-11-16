package smartrecruiters

import (
	"context"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// A detail that times out once and answers on the retry is a fetched posting,
// not an unanswered one.
func TestADetailThatAnswersOnTheRetryIsFetched(t *testing.T) {
	a := board(20)
	var st Stats
	var stored []posting.Posting
	for i := 10; i < 20; i++ {
		stored = append(stored, storedPosting(idOf(i), stamp))
	}
	s := src(t, a.doer(), &Options{
		Stored: func(sources.Board) []posting.Posting { return stored },
		Report: func(_ sources.Board, s Stats) { st = s },
	})
	flaky := srctest.NewFlakyOnce(s.d, "/postings/"+idOf(5), missTimeout.Err)
	s.d = flaky
	got, err := s.List(srcutil.WithRetryBudget(context.Background(), time.Minute), srBoard())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hasID(got, idOf(5)); !ok || flaky.Matched() != 2 || st.Unanswered != 0 || st.Fetched == 0 || len(got) != 20 {
		t.Fatalf("asked %d times, stats %+v, %d postings", flaky.Matched(), st, len(got))
	}
}
