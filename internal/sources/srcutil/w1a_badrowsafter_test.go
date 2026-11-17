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
	if got := n(); got != (ListingCount{Total: 100, Skipped: 3, Used: true}) || sk.skipped != 3 || sk.calls != 1 {
		t.Errorf("counted %+v, forwarded %d in %d calls, want 3 of 100 and 3 in 1", got, sk.skipped, sk.calls)
	}

	pctx, pn := CountBadRows(context.Background())
	if got := pn(); got != (ListingCount{}) {
		t.Errorf("before any Rows the count is %+v, want zero", got)
	}
	if err := NewRows(pctx, b, 100).Bad(errors.New("bad")); err == nil {
		t.Error("with no sink a bad row did not fail")
	}
	if got := pn(); got.Total != 100 || got.Skipped != 0 || !got.Used {
		t.Errorf("with no sink the count is %+v, want 100 rows seen, none skipped", got)
	}
}

// w1a: a paged adapter's Rows (one per page, with Add) all add to the listing's
// counter, each reports only its own skips, and a second CountBadRows context
// does not see the first's rows.
func TestW1aCountBadRowsSumsRowsAndIsScoped(t *testing.T) {
	b := sources.Board{Token: "a"}
	var sk w1aSkips
	ctx := WithBadRowSink(context.Background(), sk.sink)
	cctx, n := CountBadRows(ctx)
	other, on := CountBadRows(ctx)
	for page := 0; page < 4; page++ {
		r := NewRows(cctx, b, 0)
		r.Add(20)
		_ = r.Bad(errors.New("bad"))
		if err := r.Done(); err != nil {
			t.Fatal(err)
		}
	}
	if got := n(); got != (ListingCount{Total: 80, Skipped: 4, Used: true}) {
		t.Errorf("counted %+v, want 80 rows and 4 skipped", got)
	}
	if sk.calls != 4 || sk.skipped != 4 {
		t.Errorf("sink saw %d rows in %d calls, want 4 in 4", sk.skipped, sk.calls)
	}
	if got := on(); got != (ListingCount{}) {
		t.Errorf("another listing's counter leaked: %+v", got)
	}
	_ = other
}

// w1a: Done fails a stage that skipped nothing itself when the earlier stages'
// skips are already over the bound.
func TestW1aDoneChecksPriorSkipsWithNoneOfItsOwn(t *testing.T) {
	b := sources.Board{Token: "a"}
	var sk w1aSkips
	ctx := WithBadRowSink(context.Background(), sk.sink)
	if err := NewRowsAfter(ctx, b, 100, 11).Done(); err == nil {
		t.Error("11 prior skips of 100 passed Done")
	}
	if err := NewRowsAfter(ctx, b, 100, 10).Done(); err != nil {
		t.Errorf("10 prior skips of 100 failed Done: %v", err)
	}
	if sk.calls != 0 {
		t.Errorf("Done reported %d times for a stage that skipped nothing", sk.calls)
	}
}
