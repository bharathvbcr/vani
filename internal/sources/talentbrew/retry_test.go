package talentbrew

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// A job page that times out once and answers on the retry is a fetched job,
// not an unanswered one.
func TestADetailThatAnswersOnTheRetryIsFetched(t *testing.T) {
	locs := locsN(3)
	var reps []Stats
	s, err := NewWith(siteDoer(locs, nil),
		Options{Stored: storedNone, Wait: noWait, Report: func(_ sources.Board, st Stats) { reps = append(reps, st) }})
	if err != nil {
		t.Fatal(err)
	}
	src := s.(*source)
	flaky := srctest.NewFlakyOnce(src.d, locs[1], fmt.Errorf("Get %q: http2: timeout awaiting response headers: %w", locs[1], os.ErrDeadlineExceeded))
	src.d = flaky
	got, err := s.List(srcutil.WithRetryBudget(context.Background(), time.Minute), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 1 || flaky.Matched() != 2 || reps[0].Unanswered != 0 || reps[0].Fetched != 3 || len(got) != 3 {
		t.Fatalf("asked %d times, reports %+v, %d postings", flaky.Matched(), reps, len(got))
	}
}
