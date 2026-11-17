package rippling

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// w1dStored is ten stored postings none on their refresh turn, the first with an
// empty Description (its body could not be read back).
func w1dStored(t *testing.T) ([]string, []posting.Posting) {
	t.Helper()
	ids := idsDue(t, 10, false)
	var stored []posting.Posting
	for i, id := range ids {
		p := storedPosting(t, id)
		if i == 0 {
			p.Description = ""
		}
		stored = append(stored, p)
	}
	return ids, stored
}

// A stored posting with genuinely empty text and no Unusable mark is reused as
// before: no detail request for it, and a miss on it would not matter.
func TestW1dEmptyTextWithoutAMarkIsReused(t *testing.T) {
	ids, stored := w1dStored(t)
	d := boardDoer(ids, map[string]srctest.Reply{ids[0]: missTimeout})
	s, reps := bounded(t, d, Options{Stored: func(sources.Board) []posting.Posting { return stored }})
	got, err := s.List(missNoRetry(), board)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := hasJob(got, ids[0])
	if !ok || len(got) != 10 || p.Description != "" || (*reps)[0].Reused != 10 || (*reps)[0].Unanswered != 0 {
		t.Fatalf("%d postings, listed=%v text %q, stats %+v: want all 10 reused", len(got), ok, p.Description, *reps)
	}
}

// A stored posting the ledger could not read back (marked) is read again, never
// handed back with an empty description.
func TestW1dUnusableStoredJobIsReadAgain(t *testing.T) {
	ids, stored := w1dStored(t)
	s, reps := bounded(t, boardDoer(ids, nil), Options{Stored: func(sources.Board) []posting.Posting { return stored }, Unusable: func(k posting.Key) bool { return k == stored[0].Key }})
	got, err := s.List(missNoRetry(), board)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := hasJob(got, ids[0])
	if !ok || len(got) != 10 || strings.TrimSpace(p.Description) == "" || p.Description == "stored text" {
		t.Fatalf("%d postings, job listed=%v with text %q: want the text its detail read", len(got), ok, p.Description)
	}
	if st := (*reps)[0]; st.Refreshed != 1 || st.Reused != 9 {
		t.Fatalf("stats %+v: want one re-read, nine reused", st)
	}
}

// A miss on that re-read is not left out silently and not listed with no text.
func TestW1dUnusableStoredJobMissIsNotSilentlyOmitted(t *testing.T) {
	ids, stored := w1dStored(t)
	d := boardDoer(ids, map[string]srctest.Reply{ids[0]: missTimeout})
	s, _ := bounded(t, d, Options{Stored: func(sources.Board) []posting.Posting { return stored }, Unusable: func(k posting.Key) bool { return k == stored[0].Key }})
	got, err := s.List(missNoRetry(), board)
	if err != nil {
		return // failing the board is the safe answer
	}
	p, ok := hasJob(got, ids[0])
	if !ok || strings.TrimSpace(p.Description) == "" {
		t.Fatalf("a missed unreadable job was dropped or listed with no text: listed=%v text %q, %d postings, no error", ok, p.Description, len(got))
	}
}
