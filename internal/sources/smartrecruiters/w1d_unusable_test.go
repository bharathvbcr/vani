package smartrecruiters

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// w1dStoredWithOneUnreadable stores all n postings of board(n), each unchanged
// and none on its refresh turn except where the turn falls by chance; the one
// returned index has an empty Description (its body could not be read back) and
// is not on its turn, so only its missing text can make the adapter read it.
func w1dStoredWithOneUnreadable(t *testing.T, n int) ([]posting.Posting, int) {
	t.Helper()
	bad := -1
	var stored []posting.Posting
	for i := range n {
		p := storedPosting(idOf(i), stamp)
		if bad < 0 && !srcutil.RefreshTurn(idOf(i), srcutil.Day(fixedNow)) {
			bad = i
			p.Description = ""
		}
		stored = append(stored, p)
	}
	if bad < 0 {
		t.Fatal("every id is on its turn")
	}
	return stored, bad
}

// w1dMark is the Unusable mark of the one key.
func w1dMark(k posting.Key) func(posting.Key) bool {
	return func(x posting.Key) bool { return x == k }
}

// A stored posting whose text is genuinely empty, with no Unusable mark, is
// reused as before: no detail request, no failure on a miss.
func TestW1dEmptyTextWithoutAMarkIsReused(t *testing.T) {
	a := board(20)
	stored, bad := w1dStoredWithOneUnreadable(t, 20)
	failing(a, missTimeout, idOf(bad)) // a request for it would miss
	var st Stats
	s := src(t, a.doer(), &Options{
		Stored: func(sources.Board) []posting.Posting { return stored },
		Report: func(_ sources.Board, x Stats) { st = x },
	})
	got, err := s.List(missNoRetry(), srBoard())
	if err != nil {
		t.Fatal(err)
	}
	p, ok := hasID(got, idOf(bad))
	if !ok || len(got) != 20 || p.Description != "" || st.Reused != 20 || st.Refreshed != 0 || st.Unanswered != 0 {
		t.Fatalf("%d postings, listed=%v text %q, stats %+v: want all 20 reused", len(got), ok, p.Description, st)
	}
}

// A stored posting the ledger could not read back (marked) is read again, never
// handed back with an empty description.
func TestW1dUnusableStoredPostingIsReadAgain(t *testing.T) {
	a := board(20)
	stored, bad := w1dStoredWithOneUnreadable(t, 20)
	var st Stats
	s := src(t, a.doer(), &Options{
		Stored: func(sources.Board) []posting.Posting { return stored }, Unusable: w1dMark(stored[bad].Key),
		Report: func(_ sources.Board, x Stats) { st = x },
	})
	got, err := s.List(missNoRetry(), srBoard())
	if err != nil {
		t.Fatal(err)
	}
	p, ok := hasID(got, idOf(bad))
	if !ok || len(got) != 20 || strings.TrimSpace(p.Description) == "" || p.Description == "stored text" {
		t.Fatalf("%d postings, posting %d listed=%v with text %q: want it listed with the text its detail read", len(got), bad, ok, p.Description)
	}
	if st.Refreshed != 1 || st.Reused != 19 {
		t.Fatalf("stats %+v: want the unreadable posting re-read once and the rest reused", st)
	}
}

// When that re-read misses, the posting is neither listed with no text nor left
// out of a listing the ledger ages it against: the board fails.
func TestW1dUnusableStoredPostingMissIsNotSilentlyOmitted(t *testing.T) {
	a := board(20)
	stored, bad := w1dStoredWithOneUnreadable(t, 20)
	failing(a, missTimeout, idOf(bad))
	s := src(t, a.doer(), &Options{Stored: func(sources.Board) []posting.Posting { return stored }, Unusable: w1dMark(stored[bad].Key)})
	got, err := s.List(missNoRetry(), srBoard())
	if err != nil {
		return // failing the board is the safe answer
	}
	p, ok := hasID(got, idOf(bad))
	if !ok || strings.TrimSpace(p.Description) == "" {
		t.Fatalf("a missed unreadable posting was dropped or listed with no text: listed=%v text %q, %d postings, no error", ok, p.Description, len(got))
	}
}

// The unreadable row is never dropped by the title rule that drops new rows, and
// its read is not deferred past the cap: a board of new rows over MaxDetails
// still reads it.
func TestW1dUnusableStoredPostingIsNotDeferredByNewRows(t *testing.T) {
	n := MaxDetails + 10
	a := board(n)
	stored, bad := w1dStoredWithOneUnreadable(t, 5)
	s := src(t, a.doer(), &Options{
		Stored: func(sources.Board) []posting.Posting { return stored }, Unusable: w1dMark(stored[bad].Key),
		DropListed: func(posting.Posting) bool { return true },
	})
	got, err := s.List(missNoRetry(), srBoard())
	if err != nil {
		t.Fatal(err)
	}
	p, ok := hasID(got, idOf(bad))
	if !ok || strings.TrimSpace(p.Description) == "" {
		t.Fatalf("unreadable posting listed=%v with text %q", ok, p.Description)
	}
}
