package smartrecruiters

import (
	"context"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// A query that lists fewer postings than it counts, within the allowance that
// keeps it, is kept and its gap counted (Stats.Short), per query and never
// below zero, summed over the board's countries.
func TestShortListingIsCounted(t *testing.T) {
	ids := board(250).ids("")
	for name, tc := range map[string]struct {
		serve, short int
	}{
		"all":                {250, 0},
		"one closed":         {249, 1},
		"two closed":         {248, 2},
		"one over the count": {251, 0},
	} {
		a := board(0)
		a.list = func(_ string, offset int) (srctest.Reply, bool) {
			hi := min(offset+100, tc.serve)
			if offset >= hi {
				return okReply(page(250, nil)), true
			}
			return okReply(page(250, idsFrom(ids, tc.serve)[offset:hi])), true
		}
		var st Stats
		if _, err := src(t, a.doer(), &Options{Report: func(_ sources.Board, s Stats) { st = s }}).List(context.Background(), srBoard()); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if st.Short != tc.short {
			t.Errorf("%s: Short = %d, want %d", name, st.Short, tc.short)
		}
	}

	// Two countries, each one short: the gaps add.
	a := board(0)
	a.list = func(cc string, offset int) (srctest.Reply, bool) {
		if offset > 0 {
			return okReply(page(100, nil)), true
		}
		return okReply(page(100, idsFrom(board(200).ids(""), 200)[map[string]int{"us": 0, "ca": 100}[cc]:][:99])), true
	}
	var st Stats
	if _, err := src(t, a.doer(), &Options{Report: func(_ sources.Board, s Stats) { st = s }}).List(context.Background(), countryBoard("us", "ca")); err != nil {
		t.Fatal(err)
	}
	if st.Short != 2 {
		t.Fatalf("two countries one short each: Short = %d, want 2", st.Short)
	}
}

// A board that fails on a detail after listing short is not reconciled, so
// nothing ages: its report carries no shortfall.
func TestShortListingOnAFailedBoardIsNotReported(t *testing.T) {
	ids := board(250).ids("")
	a := board(0)
	a.list = func(_ string, offset int) (srctest.Reply, bool) {
		hi := min(offset+100, 249)
		if offset >= hi {
			return okReply(page(250, nil)), true
		}
		return okReply(page(250, ids[offset:hi])), true
	}
	a.bad = map[string]srctest.Reply{idOf(5): {Status: 500, Body: []byte("{}")}}
	reps := &reports{}
	if _, err := src(t, a.doer(), &Options{Report: reps.add}).List(context.Background(), srBoard()); err == nil {
		t.Fatal("a failed detail did not fail the board")
	}
	if len(reps.calls) != 1 || reps.calls[0].Short != 0 {
		t.Fatalf("reports %+v", reps.calls)
	}
}
