package oraclehcm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// A detail that times out once and answers on the retry is a fetched
// requisition, not an unanswered one.
func TestADetailThatAnswersOnTheRetryIsFetched(t *testing.T) {
	f := &fake{ids: ids(20)}
	var st Stats
	s := withStored(t, f.doer(), storedAll(f.ids[10:]), func(_ sources.Board, s Stats) { st = s })
	src := s.(*source)
	flaky := srctest.NewFlakyOnce(src.d, f.ids[5], headerTimeout.Err)
	src.d = flaky
	got, err := s.List(srcutil.WithRetryBudget(context.Background(), time.Minute), jpmc())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range got {
		found = found || strings.HasSuffix(string(p.Key), f.ids[5])
	}
	if !found || flaky.Matched() != 2 || st.Unanswered != 0 || st.Fetched == 0 || len(got) != 20 {
		t.Fatalf("asked %d times, stats %+v, %d postings", flaky.Matched(), st, len(got))
	}
}
