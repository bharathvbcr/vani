package avature

import (
	"context"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// A detail page that times out once and answers on the retry is a fetched
// posting, not an unanswered one. This package has no TestMain without waits, so
// the retry's backoff is answered at once here.
func TestADetailThatAnswersOnTheRetryIsFetched(t *testing.T) {
	t.Cleanup(srcutil.SetWaitForTest(func(ctx context.Context, _ time.Duration) error { return ctx.Err() }))
	f := newFake(t, 40)
	_, notDue := idsByTurn(f)
	if len(notDue) < 6 {
		t.Fatalf("fixture: %d ids off their turn", len(notDue))
	}
	var stored []posting.Posting
	for _, id := range notDue[4:] {
		stored = append(stored, storedAs(t, id))
	}
	newID := notDue[0]
	s, st := f.bounded(stored, nil)
	// Only the new posting's page: its id ends the detail URL.
	flaky := srctest.NewFlakyOnce(s.d, "/"+newID, missTimeout.Err)
	s.d = flaky
	got, err := s.List(srcutil.WithRetryBudget(context.Background(), time.Minute), board())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hasKey(got, newID); !ok || flaky.Matched() != 2 || st.Unanswered != 0 || st.Fetched == 0 || len(got) != 40 {
		t.Fatalf("asked %d times, stats %+v, %d postings", flaky.Matched(), *st, len(got))
	}
}
