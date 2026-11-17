package main

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// w1aadv: bound accounting. An adapter that skips its own bad rows counts them
// against the total it saw (100 rows, MaxGone 10) and passes. The seam then
// counts the unstorable rows among the rows left (90, MaxGone 9) and passes
// again. 10 + 9 = 19 of 100 rows are gone with no board failure, where the
// documented bound is a tenth of the rows the board sent.
func TestW1aadvBoundAcrossAdapterAndSeam(t *testing.T) {
	withBoardListParallel(t, 1)
	l, reconcile := w1aLedger(t)
	var sk w1aSink
	b := sources.Board{ATS: posting.Lever, Token: "aco", Company: "A"}
	srcs := map[posting.ATS]sources.Source{
		posting.Lever: fakeSource{posting.Lever, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
			all := boardPostings(b, 100)
			rows := srcutil.NewRows(ctx, b, len(all))
			var kept []posting.Posting
			for i, p := range all {
				if i < 10 {
					if err := rows.Bad(errors.New("adapter: row cannot be read")); err != nil {
						return nil, err
					}
					continue
				}
				if i >= 10 && i < 19 {
					p.Location = strings.Repeat("x", srcutil.MaxLocation+1) // the adapter does not see this
				}
				kept = append(kept, p)
			}
			return kept, rows.Done()
		}},
	}
	var warn warnLog
	ctx := srcutil.WithBadRowSink(context.Background(), sk.sink)
	rep, err := listBoards(ctx, []sources.Board{b}, srcs, nil, reconcile, warn.warnf, "")
	if err != nil {
		t.Fatal(err)
	}
	gone := sk.skipped["aco"]
	stored := len(w1aStoredKeys(l, "lv:aco:"))
	if rep.Items[0].Outcome == boardRead && gone > srcutil.MaxGone(100) {
		t.Errorf("board read with %d of 100 rows gone (adapter 10 + seam 9, stored %d), over the %d a board may leave out; sink calls %d",
			gone, stored, srcutil.MaxGone(100), sk.calls["aco"])
	}
}

// w1aadv: runtime.Goexit inside List (a test's t.FailNow, or an adapter that
// ends its goroutine) runs no recover and no send: the goroutine ends without
// its one listFetch, so the consumer waits for it forever.
func TestW1aadvGoexitInListDoesNotHangTheRun(t *testing.T) {
	withBoardListParallel(t, 1)
	b := sources.Board{ATS: posting.Lever, Token: "exit", Company: "E"}
	srcs := map[posting.ATS]sources.Source{
		posting.Lever: fakeSource{posting.Lever, func(context.Context, sources.Board) ([]posting.Posting, error) {
			runtime.Goexit()
			return nil, nil
		}},
	}
	var warn warnLog
	_, _ = w1aListBoardsWatched(context.Background(), t, 3*time.Second,
		"listBoards hangs: the board whose List called runtime.Goexit never sends its listFetch",
		[]sources.Board{b}, srcs, tempReconciler(t).Reconcile, warn.warnf)
}
