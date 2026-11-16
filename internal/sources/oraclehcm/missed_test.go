package oraclehcm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// headerTimeout is jpmc/CX_1001's failure of 2026-11-16: one requisition's
// detail timed out awaiting headers beside 1,967 listed rows.
var headerTimeout = srctest.Reply{Err: fmt.Errorf(`Get "https://jpmc.fa.oraclecloud.com/x": net/http: timeout awaiting response headers: %w`, os.ErrDeadlineExceeded)}

// listNoRetry lists with a spent retry budget, so a timed-out request fails at
// once instead of waiting a real backoff in the test.
func listNoRetry(s sources.Source) ([]posting.Posting, error) {
	return s.List(srcutil.WithRetryBudget(context.Background(), 0), jpmc())
}

func TestATimedOutNewDetailIsLeftForTheNextRun(t *testing.T) {
	f := &fake{ids: ids(20)}
	f.detail = func(id string) (srctest.Reply, bool) { return headerTimeout, id == f.ids[5] }
	var st Stats
	s := withStored(t, f.doer(), storedAll(f.ids[10:]), func(_ sources.Board, s Stats) { st = s })
	got, err := listNoRetry(s)
	if err != nil {
		t.Fatalf("one timed-out detail failed the board: %v", err)
	}
	if len(got) != 19 || st.Unanswered != 1 || st.Reused+st.Fetched+st.Refreshed+st.Deferred+st.Gone+st.Unanswered != st.Listed {
		t.Fatalf("%d postings, stats %+v", len(got), st)
	}
	for _, p := range got {
		if strings.HasSuffix(string(p.Key), f.ids[5]) {
			t.Fatal("the missed requisition was returned")
		}
	}
}

func TestATimedOutReReadKeepsTheStoredRequisition(t *testing.T) {
	all := ids(40)
	due, _ := dueIDs(all)
	if len(due) == 0 {
		t.Fatal("no requisition is due a re-read")
	}
	f := &fake{ids: all}
	f.detail = func(id string) (srctest.Reply, bool) { return headerTimeout, id == due[0] }
	var st Stats
	s := withStored(t, f.doer(), storedAll(all), func(_ sources.Board, s Stats) { st = s })
	got, err := listNoRetry(s)
	if err != nil {
		t.Fatalf("a timed-out re-read failed the board: %v", err)
	}
	if len(got) != 40 || st.Unanswered != 1 {
		t.Fatalf("%d postings, stats %+v", len(got), st)
	}
	for _, p := range got {
		if strings.HasSuffix(string(p.Key), due[0]) && p.Title != "stored "+due[0] {
			t.Fatalf("kept %q, want the stored posting", p.Title)
		}
	}
}

func TestTimedOutDetailsPastTheBoundFailTheBoard(t *testing.T) {
	f := &fake{ids: ids(40)}
	bad := map[string]bool{}
	for _, id := range f.ids[:srcutil.MaxMissed(40, 0)+1] {
		bad[id] = true
	}
	f.detail = func(id string) (srctest.Reply, bool) { return headerTimeout, bad[id] }
	s := withStored(t, f.doer(), func(sources.Board) []posting.Posting { return nil }, nil)
	if got, err := listNoRetry(s); err == nil || got != nil {
		t.Fatalf("%d postings, %v; want the board to fail", len(got), err)
	}
}

func TestATimedOutDetailFailsTheBoardWithoutStoredPostings(t *testing.T) {
	f := &fake{ids: ids(20)}
	f.detail = func(id string) (srctest.Reply, bool) { return headerTimeout, id == f.ids[2] }
	if got, err := listNoRetry(New(f.doer())); err == nil || got != nil {
		t.Fatalf("%d postings, %v; want the board to fail", len(got), err)
	}
}

func TestMissedAndGoneAloneDoNotAnswerABoard(t *testing.T) {
	f := &fake{ids: ids(2)}
	f.detail = func(id string) (srctest.Reply, bool) {
		if id == f.ids[0] {
			return ok(goneBody()), true
		}
		return headerTimeout, true
	}
	s := withStored(t, f.doer(), func(sources.Board) []posting.Posting { return nil }, nil)
	if _, err := listNoRetry(s); !errors.Is(err, srcutil.ErrNothingAnswered) {
		t.Fatalf("err = %v, want ErrNothingAnswered", err)
	}
}
