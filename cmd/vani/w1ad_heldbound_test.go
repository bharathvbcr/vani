package main

import (
	"context"
	"errors"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// w1ad: an adapter listing 500 rows of which `held` are stored postings whose
// re-read it did not make (Rows.Hold), `adapterBad` it skipped (Rows.Bad) and
// `seamBad` it hands on unstorable, so the seam skips them. The rest are kept.
func w1adList(held, adapterBad, seamBad int) func(context.Context, sources.Board) ([]posting.Posting, error) {
	return func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
		const total = 500
		rows := srcutil.NewRows(ctx, b, total)
		for i := 0; i < adapterBad; i++ {
			if err := rows.Bad(errors.New("adapter: row cannot be read")); err != nil {
				return nil, err
			}
		}
		rows.Hold(held, "stored text unusable, re-read not made")
		kept := boardPostings(b, total-held-adapterBad)
		for i := 0; i < seamBad; i++ {
			w1aOversize(&kept[i])
		}
		return kept, rows.Done()
	}
}

// w1ad: 500 unusable stored rows (300 re-read, 200 held) and no bad row: the
// board is read and reads as short by the 200 held. Held rows are not bad rows,
// so the 10% bound (50) is not touched.
func TestW1adHeldRowsDoNotCountTowardTheBadRowBound(t *testing.T) {
	withBoardListParallel(t, 1)
	b := sources.Board{ATS: posting.Lever, Token: "held", Company: "H"}
	items, sk := w1aRunBoards(t, []sources.Board{b}, w1adList(200, 0, 0))
	if items[0].Outcome != boardRead {
		t.Fatalf("200 held of 500 failed the board: %+v", items[0])
	}
	if sk.skipped["held"] != 200 {
		t.Errorf("sink saw %d, want the 200 held as the listing's shortfall", sk.skipped["held"])
	}
}

// w1ad: 200 held plus bad rows under the bound is read; the bound is on the bad
// rows alone, adapter and seam together (50 of 500), however many are held.
func TestW1adBadRowsUnderTheBoundWithHeldAreRead(t *testing.T) {
	withBoardListParallel(t, 1)
	for _, tc := range []struct {
		name            string
		adapter, seam   int
		wantRead        bool
		wantSinkSkipped int
	}{
		{"adapter 49", 49, 0, true, 249},
		{"adapter 50 (at the bound)", 50, 0, true, 250},
		{"adapter 51 (one over)", 51, 0, false, 0},
		{"seam 50 (at the bound)", 0, 50, true, 250},
		{"seam 51 (one over)", 0, 51, false, 0},
		{"adapter 30 + seam 20 (at the bound)", 30, 20, true, 250},
		{"adapter 30 + seam 21 (one over)", 30, 21, false, 0},
	} {
		b := sources.Board{ATS: posting.Lever, Token: "mix", Company: "M"}
		items, sk := w1aRunBoards(t, []sources.Board{b}, w1adList(200, tc.adapter, tc.seam))
		if got := items[0].Outcome == boardRead; got != tc.wantRead {
			t.Errorf("%s: read=%v, want %v (%+v)", tc.name, got, tc.wantRead, items[0])
			continue
		}
		if tc.wantRead && sk.skipped["mix"] != tc.wantSinkSkipped {
			t.Errorf("%s: sink saw %d, want %d (held + adapter bad + seam bad)", tc.name, sk.skipped["mix"], tc.wantSinkSkipped)
		}
	}
}

// w1ad: the per-listing counter keeps held apart from skipped, and the seam's
// bound reads only the skipped.
func TestW1adListingCountKeepsHeldApartFromSkipped(t *testing.T) {
	b := sources.Board{ATS: posting.Lever, Token: "cnt", Company: "C"}
	sunk := 0
	ctx := srcutil.WithBadRowSink(context.Background(), func(_ sources.Board, n int, _ string) { sunk += n })
	lctx, counted := srcutil.CountBadRows(ctx)
	listed, err := w1adList(200, 5, 0)(lctx, b)
	if err != nil {
		t.Fatal(err)
	}
	c := counted()
	if c.Held != 200 || c.Skipped != 5 || c.Total != 500 || !c.Used {
		t.Fatalf("ListingCount = %+v, want Held 200 Skipped 5 Total 500", c)
	}
	kept, err := storableListing(ctx, b, listed, c)
	if err != nil || len(kept) != len(listed) {
		t.Fatalf("storableListing: kept %d of %d, err %v", len(kept), len(listed), err)
	}
	if sunk != 205 {
		t.Errorf("sink saw %d, want 205 (the adapter's 5 bad + 200 held; the seam skipped none)", sunk)
	}
}
