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

// w1a: run one Lever board per source function and return each board's outcome.
func w1aRunBoards(t *testing.T, boards []sources.Board, list func(context.Context, sources.Board) ([]posting.Posting, error)) ([]boardReport, *w1aSink) {
	t.Helper()
	_, reconcile := w1aLedger(t)
	var sk w1aSink
	srcs := map[posting.ATS]sources.Source{posting.Lever: fakeSource{posting.Lever, list}}
	var warn warnLog
	ctx := srcutil.WithBadRowSink(context.Background(), sk.sink)
	rep, err := listBoards(ctx, boards, srcs, nil, reconcile, warn.warnf, "")
	if err != nil {
		t.Fatal(err)
	}
	return rep.Items, &sk
}

func w1aOversize(p *posting.Posting) { p.Location = strings.Repeat("x", srcutil.MaxLocation+1) }

// w1a: a paged adapter with a Rows per page whose skips together stay inside a
// tenth of all its pages' rows is read, and the sink gets each page's own skips.
func TestW1aPagedAdapterWithinTheListingBoundIsRead(t *testing.T) {
	withBoardListParallel(t, 1)
	b := sources.Board{ATS: posting.Lever, Token: "paged", Company: "P"}
	items, sk := w1aRunBoards(t, []sources.Board{b}, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
		var kept []posting.Posting
		for page := 0; page < 10; page++ {
			rows := srcutil.NewRows(ctx, b, 20) // 200 rows, bound 20
			if err := rows.Bad(errors.New("adapter: row cannot be read")); err != nil {
				return nil, err
			}
			for i := 0; i < 19; i++ {
				p := boardPostings(b, 1)[0]
				p.Key = posting.Key(fmt.Sprintf("%s%d-%d", b.KeyPrefix(), page, i))
				kept = append(kept, p)
			}
			if err := rows.Done(); err != nil {
				return nil, err
			}
		}
		return kept, nil
	})
	if items[0].Outcome != boardRead {
		t.Fatalf("10 skipped of 200 failed the board: %+v", items[0])
	}
	if sk.skipped["paged"] != 10 {
		t.Errorf("sink saw %d skipped rows, want the adapter's 10", sk.skipped["paged"])
	}
}

// w1a: an adapter that read 100 rows and dropped some silently (a prefilter)
// has its bound on the 100, not on the rows it handed over: one seam bad row
// among 40 listed is inside a tenth of 100, and 10 adapter skips plus one are not.
func TestW1aPrefilteredRowsCountInTheTotal(t *testing.T) {
	withBoardListParallel(t, 1)
	for _, tc := range []struct {
		adapterSkips int
		wantRead     bool
	}{{0, true}, {9, true}, {10, false}} {
		b := sources.Board{ATS: posting.Lever, Token: "pre", Company: "P"}
		items, _ := w1aRunBoards(t, []sources.Board{b}, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
			rows := srcutil.NewRows(ctx, b, 100)
			for i := 0; i < tc.adapterSkips; i++ {
				if err := rows.Bad(errors.New("adapter: row cannot be read")); err != nil {
					return nil, err
				}
			}
			kept := boardPostings(b, 40) // the rest of the 100 were filtered out silently
			w1aOversize(&kept[3])
			return kept, rows.Done()
		})
		if got := items[0].Outcome == boardRead; got != tc.wantRead {
			t.Errorf("adapter skips %d + 1 seam of 100: read=%v, want %v (%+v)", tc.adapterSkips, got, tc.wantRead, items[0])
		}
	}
}

// w1a: an adapter that uses no Rows has counted nothing, so the listing itself
// is the total: 2 unstorable of 20 pass, 3 fail.
func TestW1aAdapterWithNoRowsFallsBackToTheListing(t *testing.T) {
	withBoardListParallel(t, 1)
	for _, tc := range []struct {
		bad      int
		wantRead bool
	}{{0, true}, {srcutil.MaxGone(20), true}, {srcutil.MaxGone(20) + 1, false}} {
		b := sources.Board{ATS: posting.Lever, Token: "norows", Company: "N"}
		items, _ := w1aRunBoards(t, []sources.Board{b}, func(_ context.Context, b sources.Board) ([]posting.Posting, error) {
			ps := boardPostings(b, 20)
			for i := 0; i < tc.bad; i++ {
				w1aOversize(&ps[i])
			}
			return ps, nil
		})
		if got := items[0].Outcome == boardRead; got != tc.wantRead {
			t.Errorf("%d unstorable of 20 with no Rows: read=%v, want %v", tc.bad, got, tc.wantRead)
		}
	}
}

// w1a: the listing counter belongs to one List call. Board "heavy" skips 9 of
// 100 through its Rows; board "light" lists 10 rows with one unstorable (bound
// 1) and no Rows. If heavy's skips leaked into light's counter, light would
// fail, with boards listed one at a time and four at a time.
func TestW1aListingCounterIsScopedPerBoard(t *testing.T) {
	for _, par := range []int{1, 4} {
		t.Run(fmt.Sprintf("parallel%d", par), func(t *testing.T) {
			withBoardListParallel(t, par)
			var boards []sources.Board
			for i := 0; i < 8; i++ {
				boards = append(boards,
					sources.Board{ATS: posting.Lever, Token: fmt.Sprintf("heavy%d", i), Company: "H"},
					sources.Board{ATS: posting.Lever, Token: fmt.Sprintf("light%d", i), Company: "L"})
			}
			items, _ := w1aRunBoards(t, boards, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
				if strings.HasPrefix(b.Token, "heavy") {
					rows := srcutil.NewRows(ctx, b, 100)
					for i := 0; i < 9; i++ {
						if err := rows.Bad(errors.New("adapter: row cannot be read")); err != nil {
							return nil, err
						}
					}
					return boardPostings(b, 91), rows.Done()
				}
				ps := boardPostings(b, 10)
				w1aOversize(&ps[0])
				return ps, nil
			})
			for i, it := range items {
				if it.Outcome != boardRead {
					t.Errorf("board %d (%s) not read: %+v", i, boards[i].Token, it)
				}
			}
		})
	}
}
