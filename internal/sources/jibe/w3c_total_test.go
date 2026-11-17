package jibe

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func w3cRows(t *testing.T, n int) []map[string]any {
	t.Helper()
	tmpl := fixtureJobs(t)[1]
	var rows []map[string]any
	for i := 0; i < n; i++ {
		rows = append(rows, synth(tmpl, fmt.Sprint(20000+i)))
	}
	return rows
}

// A reported total below the rows already served is not trusted to end the
// walk: before the fix the walk stopped after page 1 (100 of 250 listed) and
// every stored posting on pages 2-3 aged as absent.
func TestW3cTotalBelowRowsFailsTheBoard(t *testing.T) {
	for _, total := range []int{50, 0, 99} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			f := &site{t: t, rows: map[query][]map[string]any{{}: w3cRows(t, 250)}, total: map[query]int{{}: total}}
			s, _ := newSource(f)
			got, err := s.List(context.Background(), board())
			if err == nil {
				t.Fatalf("listed %d postings of a board that counts %d and serves 250 rows", len(got), total)
			}
			if !errors.Is(err, srcutil.ErrTotalBelowRows) || !errors.Is(err, ErrShape) {
				t.Errorf("err %v, want ErrShape wrapping ErrTotalBelowRows", err)
			}
			if got != nil {
				t.Errorf("a failed board returned %d postings", len(got))
			}
		})
	}
}

// A total equal to the rows served, and one above them reached by paging,
// still list everything.
func TestW3cTotalAtOrAboveRows(t *testing.T) {
	for _, n := range []int{100, 101, 250} {
		f := &site{t: t, rows: map[query][]map[string]any{{}: w3cRows(t, n)}}
		s, _ := newSource(f)
		got, err := s.List(context.Background(), board())
		if err != nil || len(got) != n {
			t.Errorf("%d rows: got %d, err %v", n, len(got), err)
		}
	}
}

// A total larger than the site has fails as before (a page that adds
// nothing), and a page that repeats page 1 ends at that, not at the cap.
func TestW3cTotalAboveRealityAndRepeatedPage(t *testing.T) {
	f := &site{t: t, rows: map[query][]map[string]any{{}: w3cRows(t, 150)}, total: map[query]int{{}: 400}}
	s, _ := newSource(f)
	if got, err := s.List(context.Background(), board()); err == nil || !errors.Is(err, ErrShape) {
		t.Fatalf("overstated total: got %d, err %v", len(got), err)
	}
	if len(f.reqs) > 4 {
		t.Errorf("overstated total took %d requests", len(f.reqs))
	}
}
