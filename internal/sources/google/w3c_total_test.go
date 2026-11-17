package google

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// A total below the rows page 1 served cannot vouch that page 1 holds every
// match: before the fix the rows were returned as the whole term.
func TestW3cTotalBelowRowsFailsTheBoard(t *testing.T) {
	for _, total := range []int{3, 0} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			s, _ := newSource(map[string]string{searchURL("x", nil): page(total, span(0, 5)...)})
			got, err := s.List(context.Background(), board("x"))
			if err == nil {
				t.Fatalf("listed %d postings of a term that counts %d and served 5 rows", len(got), total)
			}
			if !errors.Is(err, srcutil.ErrTotalBelowRows) || !errors.Is(err, ErrShape) {
				t.Errorf("err %v, want ErrShape wrapping ErrTotalBelowRows", err)
			}
		})
	}
}

// A total equal to the rows served is read once, with no date-sorted request.
func TestW3cTotalEqualRowsIsReadOnce(t *testing.T) {
	s, d := newSource(map[string]string{searchURL("x", nil): page(5, span(0, 5)...)})
	got, err := s.List(context.Background(), board("x"))
	if err != nil || len(got) != 5 || len(d.URLs()) != 1 {
		t.Fatalf("got %d, err %v, %d requests", len(got), err, len(d.URLs()))
	}
}
