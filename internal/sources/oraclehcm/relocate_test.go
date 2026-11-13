package oraclehcm

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// A posting's location is built from its listing row alone, so a stored row
// reused without a detail request hid a place that moved until the row's
// re-read turn. The reused copy takes the row's place now.
func TestReusedRowCarriesTheListedPlace(t *testing.T) {
	const listed = "Plano, TX, United States" // every row of the fake's listing
	_, notDue := dueIDs(ids(400))
	for name, tc := range map[string]struct {
		stored    string
		want      string
		relocated int
	}{
		"moved":            {"London, United Kingdom", listed, 1},
		"unchanged":        {listed, listed, 0},
		"case and spacing": {"  plano,  tx,   UNITED STATES ", "  plano,  tx,   UNITED STATES ", 0},
		"stored had none":  {"", listed, 1},
		"secondary added":  {listed + "; Remote", listed, 1}, // the row no longer lists the second place
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{ids: []string{notDue[0]}}
			stored := storedAll(f.ids)(sources.Board{})
			stored[0].Location = tc.stored
			var st Stats
			got, err := list(t, withStored(t, f.doer(), func(sources.Board) []posting.Posting { return stored }, func(_ sources.Board, s Stats) { st = s }), jpmc())
			if err != nil || len(got) != 1 {
				t.Fatalf("%d postings, err %v", len(got), err)
			}
			if got[0].Title != "stored "+notDue[0] || got[0].Location != tc.want {
				t.Fatalf("posting %q at %q; want the stored text at %q", got[0].Title, got[0].Location, tc.want)
			}
			if f.details != 0 || st.Reused != 1 || st.Relocated != tc.relocated {
				t.Fatalf("%d detail requests, stats %+v; want none, one reused, %d relocated", f.details, st, tc.relocated)
			}
		})
	}
}
