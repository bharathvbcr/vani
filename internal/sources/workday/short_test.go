package workday

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// A query under the window that ends short of its first page's total is kept
// (docs/FOLLOWUPS.md, "Absence ages every open posting"), and the rows it
// counted but did not list are counted, so the run record can show the gap the
// absence pass will age. The board is never failed for it.
func TestShortListingIsCountedNotFailed(t *testing.T) {
	for name, tc := range map[string]struct {
		fb    func() *fakeBoard
		short int
	}{
		"total exact": {fb: func() *fakeBoard { return nvidiaBoard(300) }, short: 0},
		"total lies high": {fb: func() *fakeBoard {
			fb := nvidiaBoard(300)
			fb.total = func(real int, _ map[string][]string) (string, bool) { return fmt.Sprint(real + 50), true }
			return fb
		}, short: 50},
		// A total under the rows of its own page cannot be believed: no count to
		// fall short of, so nothing is added (and never a negative).
		"total lies low": {fb: func() *fakeBoard {
			fb := nvidiaBoard(300)
			fb.total = func(int, map[string][]string) (string, bool) { return "7", true }
			return fb
		}, short: 0},
		// Believed (over the first page's rows) but under what the board lists:
		// the extra rows are read, and a surplus is not a negative shortfall.
		"total understates": {fb: func() *fakeBoard {
			fb := nvidiaBoard(300)
			fb.total = func(int, map[string][]string) (string, bool) { return "250", true }
			return fb
		}, short: 0},
		"total missing": {fb: func() *fakeBoard {
			fb := nvidiaBoard(300)
			fb.total = func(int, map[string][]string) (string, bool) { return "", false }
			return fb
		}, short: 0},
		// A split bucket under the window counts its own shortfall.
		"a bucket's total overstates": {fb: func() *fakeBoard {
			fb := nvidiaBoard(2753)
			fb.total = func(real int, applied map[string][]string) (string, bool) {
				if ids, ok := applied["jobFamilyGroup"]; ok && ids[0] == fid(2, 3) {
					return fmt.Sprint(real + 40), true
				}
				return fmt.Sprint(min(real, window)), true
			}
			return fb
		}, short: 40},
	} {
		t.Run(name, func(t *testing.T) {
			fb := tc.fb()
			rows, w, err := listAll(t, fb, board)
			if err != nil {
				t.Fatalf("a short listing failed the board: %v", err)
			}
			if len(rows) != len(fb.jobs) {
				t.Fatalf("listed %d of %d", len(rows), len(fb.jobs))
			}
			if w.short != tc.short {
				t.Fatalf("short = %d, want %d", w.short, tc.short)
			}
		})
	}
}

// Stats carries the shortfall to the run record, summed over search terms:
// each query falls short on its own, and one query's surplus never cancels
// another's gap.
func TestStatsShortSumsQueries(t *testing.T) {
	var offsets []int
	var st Stats
	// Every query lists 20 rows and claims 100.
	s := &source{d: srctest.NewFunc(pagedHandler(20, &offsets, func(int) int { return 100 }, detailTmpl)),
		now: func() time.Time { return fixedNow }, opt: &Options{
			Stored: func(sources.Board) []posting.Posting { return nil },
			Report: func(_ sources.Board, x Stats) { st = x },
		}}
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 20 {
		t.Fatalf("got %d, %v", len(got), err)
	}
	if st.Short != 80 {
		t.Fatalf("Short = %d, want 80 (100 counted, 20 listed)", st.Short)
	}

	b := board
	b.Search, b.Searches = "engineer", []string{"scientist"}
	st = Stats{}
	if _, err := s.List(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if st.Short != 160 {
		t.Fatalf("two searches: Short = %d, want 160 (80 each)", st.Short)
	}
}
