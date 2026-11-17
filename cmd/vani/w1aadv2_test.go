package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// w1aadv2: an adapter that pages its listing and starts a Rows per page keeps
// each page inside that page's own bound (a tenth of the page, at least one)
// while the listing as a whole leaves out far more than a tenth. The seam
// counts what the adapter reported (CountBadRows) but only compares it with the
// bound when it skips a row itself (Rows.Done returns early when its own
// skipped is zero), so the board is read with a third of its rows gone.
func TestW1aadv2PagedAdapterSkipsBeyondTheListingBound(t *testing.T) {
	withBoardListParallel(t, 1)
	_, reconcile := w1aLedger(t)
	var sk w1aSink
	b := sources.Board{ATS: posting.Lever, Token: "paged", Company: "P"}
	srcs := map[posting.ATS]sources.Source{
		posting.Lever: fakeSource{posting.Lever, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
			var kept []posting.Posting
			for page := 0; page < 10; page++ {
				rows := srcutil.NewRows(ctx, b, 3) // one page of 3 rows, bound max(1, 3/10) = 1
				if err := rows.Bad(errors.New("adapter: row cannot be read")); err != nil {
					return nil, err
				}
				for i := 0; i < 2; i++ {
					p := boardPostings(b, 1)[0]
					p.Key = posting.Key(fmt.Sprintf("%s%d-%d", b.KeyPrefix(), page, i))
					kept = append(kept, p)
				}
				if err := rows.Done(); err != nil {
					return nil, err
				}
			}
			return kept, nil // 20 kept, 10 gone: a third of 30
		}},
	}
	var warn warnLog
	ctx := srcutil.WithBadRowSink(context.Background(), sk.sink)
	rep, err := listBoards(ctx, []sources.Board{b}, srcs, nil, reconcile, warn.warnf, "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Items[0].Outcome == boardRead {
		t.Errorf("board read with %d of 30 rows gone (bound %d)", sk.skipped["paged"], srcutil.MaxGone(30))
	}
}

// w1aadv2: the seam's total is len(listed)+adapterSkipped. An adapter that
// drops rows silently (a filter) after counting its own skips against the rows
// it read leaves the seam a smaller total than the adapter used, so one
// unstorable row makes 6 bad rows of 100 read look like 6 of 45, over the bound
// of 4, and fails a board that is inside the documented tenth.
func TestW1aadv2SeamTotalSmallerThanAdapterTotalFailsAHealthyBoard(t *testing.T) {
	withBoardListParallel(t, 1)
	_, reconcile := w1aLedger(t)
	var sk w1aSink
	b := sources.Board{ATS: posting.Lever, Token: "filt", Company: "F"}
	srcs := map[posting.ATS]sources.Source{
		posting.Lever: fakeSource{posting.Lever, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
			rows := srcutil.NewRows(ctx, b, 100) // 100 rows read
			for i := 0; i < 5; i++ {
				if err := rows.Bad(errors.New("adapter: row cannot be read")); err != nil {
					return nil, err
				}
			}
			kept := boardPostings(b, 40) // 55 more rows were filtered out silently
			kept[3].Location = strings.Repeat("x", srcutil.MaxLocation+1)
			return kept, rows.Done()
		}},
	}
	var warn warnLog
	ctx := srcutil.WithBadRowSink(context.Background(), sk.sink)
	rep, err := listBoards(ctx, []sources.Board{b}, srcs, nil, reconcile, warn.warnf, "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Items[0].Outcome != boardRead {
		t.Errorf("6 bad rows of 100 read failed the board: %+v", rep.Items[0])
	}
}
