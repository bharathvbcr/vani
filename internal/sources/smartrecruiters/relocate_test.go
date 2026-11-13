package smartrecruiters

import (
	"context"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// A stored row is reused without a detail request, so a place that moved on the
// board stayed hidden until the row's re-read turn. The row carries the same
// structured place as the detail, so the reused copy takes it.
func TestReusedRowCarriesTheListedPlace(t *testing.T) {
	const listed = "Austin, Texas, United States" // what board() lists every row with
	for name, tc := range map[string]struct {
		stored    string
		want      string
		relocated int
	}{
		"moved":               {"Paris, France", listed, 1},
		"unchanged":           {listed, listed, 0},
		"case and spacing":    {"  austin,  texas, UNITED STATES", "  austin,  texas, UNITED STATES", 0},
		"stored had no place": {"", listed, 1},
	} {
		t.Run(name, func(t *testing.T) {
			id := idOnTurn(false)
			a := board(0)
			a.ids = func(string) []string { return []string{id} }
			st := storedPosting(id, stamp)
			st.Location = tc.stored
			var stats Stats
			s := src(t, a.doer(), &Options{
				Stored: func(sources.Board) []posting.Posting { return []posting.Posting{st} },
				Report: func(_ sources.Board, s Stats) { stats = s },
			})
			got, err := s.List(context.Background(), srBoard())
			if err != nil || len(got) != 1 {
				t.Fatalf("%v, %v", got, err)
			}
			if got[0].Location != tc.want || got[0].Description != "stored text" {
				t.Fatalf("location %q, description %q; want %q and the stored text", got[0].Location, got[0].Description, tc.want)
			}
			if len(a.fetchedIDs()) != 0 || stats.Reused != 1 || stats.Relocated != tc.relocated {
				t.Fatalf("fetched %v, stats %+v; want no request, one reused, %d relocated", a.fetchedIDs(), stats, tc.relocated)
			}
		})
	}
}
