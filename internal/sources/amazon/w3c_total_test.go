package amazon

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func w3cJobs(t *testing.T, n int) []map[string]any {
	base := fixtureJobs(t)[0]
	var out []map[string]any
	for i := 0; i < n; i++ {
		j := map[string]any{}
		for k, v := range base {
			j[k] = v
		}
		id := fmt.Sprint(30000000 + i)
		j["id_icims"], j["job_path"] = id, "/en/jobs/"+id+"/role"
		out = append(out, j)
	}
	return out
}

// hits below the jobs the first page served ended the walk there before the
// fix (100 of 250 listed, no error).
func TestW3cHitsBelowRowsFailsTheBoard(t *testing.T) {
	a := w3cJobs(t, 250)
	for _, hits := range []int{50, 0, 99} {
		t.Run(fmt.Sprint(hits), func(t *testing.T) {
			s, _ := newSource(handler(t, map[string]int{"a": hits}, map[string][][]map[string]any{"a": {a[:100], a[100:200], a[200:]}}))
			got, err := s.List(context.Background(), board("a"))
			if err == nil {
				t.Fatalf("listed %d postings of a search that matches %d", len(got), hits)
			}
			if !errors.Is(err, srcutil.ErrTotalBelowRows) || !errors.Is(err, ErrShape) {
				t.Errorf("err %v, want ErrShape wrapping ErrTotalBelowRows", err)
			}
		})
	}
}

// hits equal to the rows served, and one above them, list everything.
func TestW3cHitsAtOrAboveRows(t *testing.T) {
	a := w3cJobs(t, 250)
	for _, tc := range []struct{ hits, rows int }{{100, 100}, {101, 101}, {250, 250}} {
		pages := [][]map[string]any{a[:min(100, tc.rows)]}
		if tc.rows > 100 {
			pages = append(pages, a[100:tc.rows])
		}
		s, _ := newSource(handler(t, map[string]int{"a": tc.hits}, map[string][][]map[string]any{"a": pages}))
		got, err := s.List(context.Background(), board("a"))
		if err != nil || len(got) != tc.rows {
			t.Errorf("%d rows: got %d, err %v", tc.rows, len(got), err)
		}
	}
}

// A hit count larger than the site holds fails as before.
func TestW3cHitsAboveRealityFails(t *testing.T) {
	a := w3cJobs(t, 120)
	s, _ := newSource(handler(t, map[string]int{"a": 400}, map[string][][]map[string]any{"a": {a[:100], a[100:]}}))
	if got, err := s.List(context.Background(), board("a")); err == nil || !errors.Is(err, ErrShape) {
		t.Fatalf("got %d, err %v", len(got), err)
	}
}
