package srcutil

import (
	"context"
	"errors"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources"
)

type w1aSkips struct{ calls, skipped int }

func (s *w1aSkips) sink(_ sources.Board, skipped int, _ string) { s.calls++; s.skipped += skipped }

// w1a: rows an earlier stage skipped count against the bound of the whole
// listing, and Done reports only this stage's own skips.
func TestW1aNewRowsAfterCountsEarlierSkips(t *testing.T) {
	b := sources.Board{Token: "a"}
	var sk w1aSkips
	ctx := WithBadRowSink(context.Background(), sk.sink)
	// 100 rows, bound 10: 6 skipped earlier leave room for 4 more.
	r := NewRowsAfter(ctx, b, 100, 6)
	for i := 0; i < 4; i++ {
		if err := r.Bad(errors.New("bad")); err != nil {
			t.Fatalf("row %d: %v", i+1, err)
		}
	}
	if err := r.Bad(errors.New("bad")); err == nil {
		t.Fatal("the 11th row of the listing was left out without failing the board")
	}

	r = NewRowsAfter(ctx, b, 100, 6)
	for i := 0; i < 4; i++ {
		_ = r.Bad(errors.New("bad"))
	}
	if err := r.Done(); err != nil {
		t.Fatal(err)
	}
	if sk.calls != 1 || sk.skipped != 4 {
		t.Errorf("Done reported %d rows in %d calls, want only its own 4 in 1", sk.skipped, sk.calls)
	}
}

// w1a: with nothing skipped earlier, and with a negative count, the bound is
// the one NewRows gives.
func TestW1aNewRowsAfterZeroAndNegativeMatchNewRows(t *testing.T) {
	b := sources.Board{Token: "a"}
	var sk w1aSkips
	ctx := WithBadRowSink(context.Background(), sk.sink)
	for _, already := range []int{0, -5} {
		r := NewRowsAfter(ctx, b, 100, already)
		for i := 0; i < 10; i++ {
			if err := r.Bad(errors.New("bad")); err != nil {
				t.Fatalf("already %d, row %d: %v", already, i+1, err)
			}
		}
		if err := r.Bad(errors.New("bad")); err == nil {
			t.Errorf("already %d: an 11th bad row of 100 did not fail", already)
		}
	}
}

// w1a: CountBadRows counts what the adapter reports and forwards it unchanged;
// with no sink it counts nothing and leaves the context alone.
func TestW1aCountBadRowsForwardsAndCounts(t *testing.T) {
	b := sources.Board{Token: "a"}
	var sk w1aSkips
	ctx := WithBadRowSink(context.Background(), sk.sink)
	cctx, n := CountBadRows(ctx)
	r := NewRows(cctx, b, 100)
	for i := 0; i < 3; i++ {
		_ = r.Bad(errors.New("bad"))
	}
	if err := r.Done(); err != nil {
		t.Fatal(err)
	}
	if n() != 3 || sk.skipped != 3 || sk.calls != 1 {
		t.Errorf("counted %d, forwarded %d in %d calls, want 3 and 3 in 1", n(), sk.skipped, sk.calls)
	}

	plain := context.Background()
	pctx, pn := CountBadRows(plain)
	if pctx != plain || pn() != 0 {
		t.Error("with no sink the context changed or the count is not zero")
	}
	if err := NewRows(pctx, b, 100).Bad(errors.New("bad")); err == nil {
		t.Error("with no sink a bad row did not fail")
	}
}
