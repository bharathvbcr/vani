package workday

import (
	"context"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// w1dholdCtx is a retry-free context whose listing reports its shortfall to got.
func w1dholdCtx(got *[]int) context.Context {
	return srcutil.WithBadRowSink(missNoRetry(), func(_ sources.Board, n int, _ string) { *got = append(*got, n) })
}

// A board whose every stored row is unusable (n of them, past MaxDetails) spends
// exactly MaxDetails requests in a run. The rest are not listed (no empty body,
// no silent omission): the listing reports them as a shortfall so the ledger holds
// their postings, and the board does not fail.
func TestW1dholdManyUnusableRowsAreCappedAndHeld(t *testing.T) {
	n := MaxDetails + 200
	fb := missBoard(n)
	h, counts := countDetails(fb.handle)
	keys := map[posting.Key]bool{}
	var stored []posting.Posting
	for i := range n {
		sp := storedJobs(fb, i)
		sp[0].Description = ""
		keys[sp[0].Key] = true
		stored = append(stored, sp...)
	}
	var st Stats
	s := missSource(h, &Options{
		Stored:   func(sources.Board) []posting.Posting { return stored },
		Unusable: func(k posting.Key) bool { return keys[k] },
		Report:   func(_ sources.Board, x Stats) { st = x },
	})
	var short []int
	got, err := s.List(w1dholdCtx(&short), board)
	if err != nil {
		t.Fatalf("the board failed: %v", err)
	}
	total := 0
	for _, c := range counts() {
		total += c
	}
	if total != MaxDetails {
		t.Fatalf("%d detail requests, want exactly MaxDetails=%d", total, MaxDetails)
	}
	if len(short) != 1 || short[0] != n-MaxDetails {
		t.Fatalf("shortfall reports %v, want one report of %d held rows", short, n-MaxDetails)
	}
	if len(got) != MaxDetails {
		t.Fatalf("%d postings listed, want the %d that were read", len(got), MaxDetails)
	}
	for _, p := range got {
		if p.Description == "" {
			t.Fatalf("posting %s listed with an empty body", p.Key)
		}
	}
	if st.Refreshed != MaxDetails {
		t.Fatalf("stats %+v", st)
	}
}

// A timed-out detail on an unusable row is held, not a board failure: the row is
// not listed and the shortfall is reported, so its stored posting is kept.
func TestW1dholdMissOnUnusableRowHoldsIt(t *testing.T) {
	fb := missBoard(30)
	const unreadable = 5
	s := missSource(failDetails(fb, missTimeout, unreadable), &Options{
		Stored:   func(sources.Board) []posting.Posting { return w1dadvUnreadableStored(fb, unreadable) },
		Unusable: w1dadvMarked(fb, unreadable),
	})
	var short []int
	got, err := s.List(w1dholdCtx(&short), board)
	if err != nil {
		t.Fatalf("a missed unusable row failed the board: %v", err)
	}
	if hasReq(got, fb.jobs[unreadable].req) {
		t.Fatal("the held row was listed")
	}
	if len(short) != 1 || short[0] != 1 {
		t.Fatalf("shortfall reports %v, want one held row", short)
	}
	for _, p := range got {
		if p.Description == "" {
			t.Fatalf("posting %s listed with an empty body", p.Key)
		}
	}
}
