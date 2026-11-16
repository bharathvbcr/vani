package eightfold

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// missTimeout is the transport failure of 2026-11-16: a detail whose headers
// did not arrive within the client's timeout.
var missTimeout = srctest.Reply{Err: fmt.Errorf(`Get "https://%s/api/pcsx/position_details": http2: timeout awaiting response headers: %w`, host, os.ErrDeadlineExceeded)}

// missNoRetry is a context whose retry budget is spent, so a timed-out request
// fails at once.
func missNoRetry() context.Context { return srcutil.WithRetryBudget(context.Background(), 0) }

func missedAccounted(st Stats) bool {
	return st.Reused+st.Fetched+st.Refreshed+st.Prefiltered+st.Deferred+st.Gone+st.Unanswered == st.Listed
}

func TestATimedOutNewDetailIsLeftForTheNextRun(t *testing.T) {
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
	s, _, stats := bounded(t, n, map[string]srctest.Reply{newID: missTimeout}, stored, nil)
	got, err := s.List(missNoRetry(), board("t"))
	if err != nil {
		t.Fatalf("one timed-out detail failed the board: %v", err)
	}
	st := (*stats)[0]
	if st.Unanswered != 1 || len(got) != n-1 || !missedAccounted(st) {
		t.Fatalf("stats %+v, %d postings", st, len(got))
	}
	for _, p := range got {
		if strings.HasSuffix(string(p.Key), newID) {
			t.Fatal("the missed position was returned")
		}
	}
}

func TestATimedOutReReadKeepsTheStoredPosting(t *testing.T) {
	const n = 40
	onTurn, _ := turnSplit(n)
	if len(onTurn) == 0 {
		t.Fatal("fixture: no position is due a re-read")
	}
	var stored []posting.Posting
	for i := range n {
		stored = append(stored, storedPosting(t, "microsoft.com", id(i)))
	}
	due := onTurn[0]
	s, _, stats := bounded(t, n, map[string]srctest.Reply{due: missTimeout}, stored, nil)
	got, err := s.List(missNoRetry(), board("t"))
	if err != nil {
		t.Fatalf("a timed-out re-read failed the board: %v", err)
	}
	st := (*stats)[0]
	if st.Unanswered != 1 || len(got) != n || !missedAccounted(st) {
		t.Fatalf("stats %+v, %d postings", st, len(got))
	}
	for _, p := range got {
		if strings.HasSuffix(string(p.Key), due) {
			if p.Title != "Stored "+due || !p.SeenAt.Equal(fixedNow) {
				t.Fatalf("kept posting %+v, want the stored one seen now", p)
			}
			return
		}
	}
	t.Fatal("the stored position was left out")
}

func TestTimedOutDetailsPastTheBoundFailTheBoard(t *testing.T) {
	const n = 20
	replies := map[string]srctest.Reply{}
	for i := range srcutil.MaxMissed(n, 0) + 1 {
		replies[id(i)] = missTimeout
	}
	s, _, _ := bounded(t, n, replies, nil, nil)
	if got, err := s.List(missNoRetry(), board("t")); err == nil || got != nil {
		t.Fatalf("%d postings, %v; want the board to fail", len(got), err)
	}
}

// Without the stored postings a missed position could not be told from a
// stored one left out, so a timeout fails the board.
func TestATimedOutDetailFailsTheBoardWithoutStoredPostings(t *testing.T) {
	for name, opt := range map[string]*Options{"no options": nil, "options without Stored": {}} {
		s := goneSite(t, 10, map[string]srctest.Reply{id(3): missTimeout})
		s.opt = opt
		if got, err := s.List(missNoRetry(), board("t")); err == nil || got != nil {
			t.Errorf("%s: %d postings, %v; want the board to fail", name, len(got), err)
		}
	}
}

func TestAServerErrorOnADetailStillFailsTheBoardWithStoredPostings(t *testing.T) {
	s, _, _ := bounded(t, 20, map[string]srctest.Reply{id(5): {Status: 500, Body: []byte("{}")}}, nil, nil)
	if got, err := s.List(missNoRetry(), board("t")); err == nil || got != nil {
		t.Fatalf("%d postings, %v; want the board to fail", len(got), err)
	}
}
