package eightfold

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// A detail that times out once and answers on the retry is a fetched position,
// not an unanswered one.
func TestADetailThatAnswersOnTheRetryIsFetched(t *testing.T) {
	const n = 40
	_, offTurn := turnSplit(n)
	var stored []posting.Posting
	for _, pid := range offTurn[4:] {
		stored = append(stored, storedPosting(t, "microsoft.com", pid))
	}
	var newID string
	for i := range n {
		if pid := id(i); !slices.Contains(offTurn[4:], pid) {
			newID = pid
			break
		}
	}
	s, _, stats := bounded(t, n, nil, stored, nil)
	flaky := srctest.NewFlakyOnce(s.d, "position_id="+newID, missTimeout.Err)
	s.d = flaky
	got, err := s.List(srcutil.WithRetryBudget(context.Background(), time.Minute), board("t"))
	if err != nil {
		t.Fatal(err)
	}
	st := (*stats)[0]
	if flaky.Matched() != 2 || st.Unanswered != 0 || st.Fetched == 0 || len(got) != n || !missedAccounted(st) {
		t.Fatalf("asked %d times, stats %+v, %d postings", flaky.Matched(), st, len(got))
	}
}
