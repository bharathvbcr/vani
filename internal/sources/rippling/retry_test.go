package rippling

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// A detail that times out once and answers on the retry is a fetched job, not
// an unanswered one. This package has no TestMain without waits, so the
// retry's backoff is answered at once here.
func TestADetailThatAnswersOnTheRetryIsFetched(t *testing.T) {
	t.Cleanup(srcutil.SetWaitForTest(func(ctx context.Context, _ time.Duration) error { return ctx.Err() }))
	var ids []string
	var stored []posting.Posting
	for _, id := range idsDue(t, 10, false) {
		ids = append(ids, id)
		stored = append(stored, storedPosting(t, id))
	}
	for i := range 10 {
		ids = append(ids, fmt.Sprintf("n%d", i))
	}
	s, reps := bounded(t, boardDoer(ids, nil), Options{Stored: func(sources.Board) []posting.Posting { return stored }})
	flaky := srctest.NewFlakyOnce(s.d, detailURL("n5"), missTimeout.Err)
	s.d = flaky
	got, err := s.List(srcutil.WithRetryBudget(context.Background(), time.Minute), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(*reps) != 1 {
		t.Fatalf("reports %+v", *reps)
	}
	st := (*reps)[0]
	if _, ok := hasJob(got, "n5"); !ok || flaky.Matched() != 2 || st.Unanswered != 0 || st.Fetched != 10 || len(got) != 20 {
		t.Fatalf("asked %d times, stats %+v, %d postings", flaky.Matched(), st, len(got))
	}
}
