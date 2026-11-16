package apple

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// A details page that times out once and answers on the retry is a fetched
// position, not an unanswered one.
func TestADetailThatAnswersOnTheRetryIsFetched(t *testing.T) {
	const n = 30
	s, _, stats := withStored(appleSite(t, n, fmt.Sprint(n)), storedApple(t, appleID(0), appleID(1)))
	id := appleID(7)
	flaky := srctest.NewFlakyOnce(s.d, detailURL(id, "0001", "role-"+id), timeout)
	s.d = flaky
	got, err := s.List(srcutil.WithRetryBudget(context.Background(), time.Minute), board("a"))
	if err != nil {
		t.Fatal(err)
	}
	st := (*stats)[0]
	if flaky.Matched() != 2 || st.Unanswered != 0 || st.Fetched == 0 || len(got) != n || !accounted(st) {
		t.Fatalf("asked %d times, stats %+v, %d postings", flaky.Matched(), st, len(got))
	}
}
