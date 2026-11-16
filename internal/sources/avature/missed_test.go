package avature

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// missTimeout is the transport failure of 2026-11-16: a detail page whose
// headers did not arrive within the client's timeout.
var missTimeout = srctest.Reply{Err: fmt.Errorf(`Get "https://%s/careers/JobDetail/x": http2: timeout awaiting response headers: %w`, host, os.ErrDeadlineExceeded)}

// missNoRetry is a context whose retry budget is spent, so a timed-out request
// fails at once.
func missNoRetry() context.Context { return srcutil.WithRetryBudget(context.Background(), 0) }

// timingOut makes the detail pages of the given ids time out.
func (f *fake) timingOut(ids ...string) {
	f.detailFor = func(id string) (srctest.Reply, bool) { return missTimeout, slices.Contains(ids, id) }
}

func hasKey(got []posting.Posting, id string) (posting.Posting, bool) {
	key, _ := posting.AvatureKey("twosigma", id)
	for _, p := range got {
		if p.Key == key {
			return p, true
		}
	}
	return posting.Posting{}, false
}

func TestATimedOutNewDetailIsLeftForTheNextRun(t *testing.T) {
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
	f.timingOut(newID)
	s, st := f.bounded(stored, nil)
	got, err := s.List(missNoRetry(), board())
	if err != nil {
		t.Fatalf("one timed-out detail failed the board: %v", err)
	}
	if st.Unanswered != 1 || len(got) != 39 || st.Reused+st.Fetched+st.Refreshed+st.Deferred+st.Prefiltered+st.Gone+st.Unanswered != st.Listed {
		t.Fatalf("stats %+v, %d postings", *st, len(got))
	}
	if _, ok := hasKey(got, newID); ok {
		t.Fatal("the missed posting was returned")
	}
}

func TestATimedOutReReadKeepsTheStoredPosting(t *testing.T) {
	f := newFake(t, 40)
	due, _ := idsByTurn(f)
	if len(due) == 0 {
		t.Fatal("fixture: no id is due a re-read")
	}
	var stored []posting.Posting
	for _, id := range f.ids {
		stored = append(stored, storedAs(t, id))
	}
	f.timingOut(due[0])
	s, st := f.bounded(stored, nil)
	got, err := s.List(missNoRetry(), board())
	if err != nil {
		t.Fatalf("a timed-out re-read failed the board: %v", err)
	}
	p, ok := hasKey(got, due[0])
	if !ok || len(got) != 40 || st.Unanswered != 1 || p.Title != "stored "+due[0] || !p.SeenAt.Equal(fixedNow) {
		t.Fatalf("%d postings, stats %+v, kept %+v (%v)", len(got), *st, p, ok)
	}
}

func TestTimedOutDetailsPastTheBoundFailTheBoard(t *testing.T) {
	const n = 20
	f := newFake(t, n)
	f.timingOut(f.ids[:srcutil.MaxMissed(n, 0)+1]...)
	s, _ := f.bounded(nil, nil)
	if got, err := s.List(missNoRetry(), board()); err == nil || got != nil {
		t.Fatalf("%d postings, %v; want the board to fail", len(got), err)
	}
}

// Without the stored postings (limit is not MaxDetails) a missed posting could
// not be told from a stored one left out, so a timeout fails the board.
func TestATimedOutDetailFailsTheBoardWithoutStoredPostings(t *testing.T) {
	f := newFake(t, 10)
	f.timingOut(f.ids[3])
	s, _ := f.source()
	if got, err := s.List(missNoRetry(), board()); err == nil || got != nil {
		t.Fatalf("no Options: %d postings, %v; want the board to fail", len(got), err)
	}
	s, _ = f.source()
	s.opt = &Options{}
	if got, err := s.List(missNoRetry(), board()); err == nil || got != nil {
		t.Fatalf("Options without Stored: %d postings, %v; want the board to fail", len(got), err)
	}
}

func TestAServerErrorOnADetailStillFailsTheBoardWithStoredPostings(t *testing.T) {
	f := newFake(t, 20)
	f.detailFor = func(id string) (srctest.Reply, bool) {
		return srctest.Reply{Status: 500, Body: []byte("{}")}, id == f.ids[5]
	}
	s, _ := f.bounded(nil, nil)
	if got, err := s.List(missNoRetry(), board()); err == nil || got != nil {
		t.Fatalf("%d postings, %v; want the board to fail", len(got), err)
	}
}
