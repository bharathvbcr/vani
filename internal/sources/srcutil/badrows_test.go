package srcutil

import (
	"context"
	"errors"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources"
)

type sunk struct {
	calls, skipped int
	first          string
}

func sinkCtx(s *sunk) context.Context {
	return WithBadRowSink(context.Background(), func(_ sources.Board, n int, first string) {
		s.calls++
		s.skipped += n
		s.first = first
	})
}

func TestRowsWithoutASinkReturnTheErrorItself(t *testing.T) {
	want := errors.New("empty title")
	r := NewRows(context.Background(), sources.Board{}, 100)
	if got := r.Bad(want); got != want {
		t.Fatalf("got %v, want the row's own error", got)
	}
	if err := r.Done(); err != nil {
		t.Fatal(err)
	}
}

func TestRowsBoundIsATenthAtLeastOne(t *testing.T) {
	for _, c := range []struct{ total, bad, wantFail int }{
		{1, 1, 0}, {5, 1, 0}, {5, 2, 2}, {10, 1, 0}, {10, 2, 2}, {19, 1, 0}, {20, 2, 0}, {20, 3, 3}, {0, 2, 2},
	} {
		var s sunk
		r := NewRows(sinkCtx(&s), sources.Board{}, c.total)
		failedAt := 0
		for i := 1; i <= c.bad; i++ {
			if r.Bad(errors.New("bad")) != nil && failedAt == 0 {
				failedAt = i
			}
		}
		if failedAt != c.wantFail {
			t.Errorf("total %d, %d bad rows: failed at row %d, want %d", c.total, c.bad, failedAt, c.wantFail)
		}
		if err := r.Done(); (err != nil) != (c.wantFail != 0) {
			t.Errorf("total %d, %d bad rows: Done %v", c.total, c.bad, err)
		}
		if c.wantFail != 0 && s.calls != 0 {
			t.Errorf("total %d, %d bad rows: a failed board reported a shortfall", c.total, c.bad)
		}
	}
}

func TestRowsDoneReportsOnceWithTheFirstError(t *testing.T) {
	var s sunk
	r := NewRows(sinkCtx(&s), sources.Board{}, 50)
	r.Bad(errors.New("first"))
	r.Bad(errors.New("second"))
	if s.calls != 0 {
		t.Fatal("reported before Done")
	}
	if err := r.Done(); err != nil {
		t.Fatal(err)
	}
	if s.calls != 1 || s.skipped != 2 || s.first != "first" {
		t.Fatalf("sink saw %+v", s)
	}
}

func TestRowsCleanListingReportsNothing(t *testing.T) {
	var s sunk
	if err := NewRows(sinkCtx(&s), sources.Board{}, 5).Done(); err != nil || s.calls != 0 {
		t.Fatalf("%v, %+v", err, s)
	}
}

// A paged listing learns its total page by page; the bound is judged against
// the total at the end.
func TestRowsPagedTotalIsJudgedAtDone(t *testing.T) {
	var s sunk
	r := NewRows(sinkCtx(&s), sources.Board{}, 0)
	r.Add(10)
	if r.Bad(errors.New("bad")) != nil {
		t.Fatal("one bad row of ten is within the bound")
	}
	r.Add(10)
	if err := r.Done(); err != nil || s.skipped != 1 {
		t.Fatalf("%v, %+v", err, s)
	}
	r = NewRows(sinkCtx(&s), sources.Board{}, 0)
	r.Add(10)
	r.Bad(errors.New("bad"))
	r.Add(0)
	if r.Bad(errors.New("bad")) == nil {
		t.Fatal("two bad rows of ten are over the bound")
	}
}

func TestNilSinkIsNoSink(t *testing.T) {
	ctx := WithBadRowSink(context.Background(), nil)
	if err := NewRows(ctx, sources.Board{}, 1).Bad(errors.New("x")); err == nil {
		t.Fatal("a nil sink must not skip rows")
	}
}
