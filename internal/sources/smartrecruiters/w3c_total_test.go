package smartrecruiters

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// A board that counts fewer postings than its first page serves fails with a
// named reason; before the fix the walk ended after page 1 with 100 of 250
// listed, no error and no shortfall.
func TestW3cTotalBelowRowsFailsTheBoard(t *testing.T) {
	for _, total := range []int{50, 0, 99} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			ids := board(250).ids("")
			a := board(0)
			a.list = func(_ string, offset int) (srctest.Reply, bool) {
				hi := min(offset+100, len(ids))
				return okReply(page(total, ids[min(offset, hi):hi])), true
			}
			got, err := src(t, a.doer(), nil).List(context.Background(), srBoard())
			if err == nil {
				t.Fatalf("listed %d postings of a board that counts %d and serves 250 rows", len(got), total)
			}
			if !errors.Is(err, srcutil.ErrTotalBelowRows) || !errors.Is(err, ErrShape) {
				t.Errorf("err %v, want ErrShape wrapping ErrTotalBelowRows", err)
			}
			if len(a.queries) != 1 {
				t.Errorf("%d listing requests, want the first page only", len(a.queries))
			}
		})
	}
}

// A total equal to the rows served is complete in one request; a total above
// them is reached by paging.
func TestW3cTotalAtOrAboveRows(t *testing.T) {
	for _, n := range []int{100, 101, 250} {
		a := board(n)
		got, err := src(t, a.doer(), nil).List(context.Background(), srBoard())
		if err != nil || len(got) != n {
			t.Errorf("%d rows: got %d, err %v", n, len(got), err)
		}
	}
}

// A page that repeats page 1 under a larger total is a stuck offset: it ends
// there, at the second request, not at the page cap.
func TestW3cRepeatedPageUnderLargerTotalTerminates(t *testing.T) {
	ids := board(100).ids("")
	a := board(0)
	a.list = func(string, int) (srctest.Reply, bool) { return okReply(page(300, ids)), true }
	_, err := src(t, a.doer(), nil).List(context.Background(), srBoard())
	if err == nil || !errors.Is(err, errIncomplete) {
		t.Fatalf("err %v, want errIncomplete", err)
	}
	if len(a.queries) != 2 {
		t.Errorf("%d listing requests, want 2", len(a.queries))
	}
}
