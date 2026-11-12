package oraclehcm

import (
	"fmt"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// A search that lists fewer requisitions than it counts, within the allowance
// that keeps it, is kept and its gap counted (Stats.Short). Markers the total
// counts and the pages serve are accounted rows, not a gap.
func TestShortListingIsCounted(t *testing.T) {
	for _, tc := range []struct {
		n, lost, short int
	}{
		{450, 0, 0},
		{450, 4, 4},
		{50, 1, 1},
	} {
		t.Run(fmt.Sprintf("n=%d/lost=%d", tc.n, tc.lost), func(t *testing.T) {
			f := &fake{ids: ids(tc.n)}
			f.page = func(_ string, offset int, rows []string) []string {
				if offset == 0 {
					return rows[:len(rows)-tc.lost]
				}
				return rows
			}
			var st Stats
			if _, err := list(t, withStored(t, f.doer(), storedAll(f.ids), func(_ sources.Board, s Stats) { st = s }), jpmc()); err != nil {
				t.Fatal(err)
			}
			if st.Short != tc.short {
				t.Fatalf("Short = %d, want %d", st.Short, tc.short)
			}
		})
	}

	rows := append(ids(95), tomb1, tomb2)
	f := &fake{ids: rows}
	var st Stats
	if _, err := list(t, withStored(t, f.doer(), storedAll(rows[:95]), func(_ sources.Board, s Stats) { st = s }), jpmc()); err != nil {
		t.Fatal(err)
	}
	if st.Short != 0 || st.Deleted != 2 {
		t.Fatalf("markers read as a gap: %+v", st)
	}
}

// Two search terms each one short: the gaps add, per term.
func TestShortListingSumsTerms(t *testing.T) {
	f := &fake{byTerm: map[string][]string{"engineer": ids(50), "analyst": ids(100)[50:]}}
	f.page = func(_ string, _ int, rows []string) []string { return rows[:len(rows)-1] }
	b := jpmcWith("engineer", "analyst")
	var st Stats
	if _, err := list(t, withStored(t, f.doer(), storedAll(ids(100)), func(_ sources.Board, s Stats) { st = s }), b); err != nil {
		t.Fatal(err)
	}
	if st.Short != 2 {
		t.Fatalf("Short = %d, want 2 (one per term)", st.Short)
	}
}

// A board that fails on a detail after listing short is not reconciled, so
// nothing ages: its report, which Oracle sends for a failed board too, carries
// no shortfall.
func TestShortListingOnAFailedBoardIsNotReported(t *testing.T) {
	f := &fake{ids: ids(50)}
	f.page = func(_ string, _ int, rows []string) []string { return rows[:49] }
	f.detail = func(string) (srctest.Reply, bool) { return srctest.Reply{Status: 500, Body: []byte("{}")}, true }
	var st Stats
	calls := 0
	s := withStored(t, f.doer(), func(sources.Board) []posting.Posting { return nil }, func(_ sources.Board, s Stats) { st = s; calls++ })
	if _, err := list(t, s, jpmc()); err == nil {
		t.Fatal("a failed detail did not fail the board")
	}
	if calls != 1 || st.Short != 0 {
		t.Fatalf("%d reports, %+v", calls, st)
	}
}
