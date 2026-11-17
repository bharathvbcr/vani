package eightfold

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func w3cSite(t *testing.T, n, count int) *site {
	f := fixtureSite(t)
	var ids []string
	for i := 0; i < n; i++ {
		ids = append(ids, fmt.Sprint(1000+i))
	}
	f.positions = map[string][]string{"x": ids}
	if count >= 0 {
		f.count = map[string]int{"x": count}
	}
	return f
}

// A count below the positions its first page served ended the walk there
// before the fix (10 of 25 listed, no error).
func TestW3cCountBelowRowsFailsTheBoard(t *testing.T) {
	for _, count := range []int{5, 0, 9} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, d := newSource(w3cSite(t, 25, count))
			got, err := s.List(context.Background(), board("x"))
			if err == nil {
				t.Fatalf("listed %d postings of a search that counts %d and serves 25", len(got), count)
			}
			if !errors.Is(err, srcutil.ErrTotalBelowRows) || !errors.Is(err, ErrShape) {
				t.Errorf("err %v, want ErrShape wrapping ErrTotalBelowRows", err)
			}
			for _, u := range d.URLs() {
				if strings.Contains(u, "start=10") || strings.Contains(u, "start=20") {
					t.Errorf("walked past page 1: %s", u)
				}
			}
		})
	}
}

// A count equal to the rows served, and one above them, list everything.
func TestW3cCountAtOrAboveRows(t *testing.T) {
	for _, n := range []int{10, 11, 25} {
		s, _ := newSource(w3cSite(t, n, -1))
		got, err := s.List(context.Background(), board("x"))
		if err != nil || len(got) != n {
			t.Errorf("%d rows: got %d, err %v", n, len(got), err)
		}
	}
}

// A count larger than the site holds fails as before, at the first page that
// adds nothing.
func TestW3cCountAboveRealityFails(t *testing.T) {
	s, _ := newSource(w3cSite(t, 15, 90))
	if got, err := s.List(context.Background(), board("x")); err == nil || !errors.Is(err, ErrShape) {
		t.Fatalf("got %d, err %v", len(got), err)
	}
}
