package main

import (
	"context"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// w1aGraceAfterCancel bounds how long a watchdog waits for listBoards to return
// once it has cancelled the run's context.
const w1aGraceAfterCancel = 10 * time.Second

// w1aListBoardsWatched runs listBoards in a goroutine and fails the test when
// it has not returned within limit. It never fails while the goroutine is still
// running: on expiry it cancels the run's context and waits, bounded, for
// listBoards to return, so the test's cleanups (which reset package globals
// that listBoards reads) run only after the last reader has finished. A
// listBoards that ignores cancellation for w1aGraceAfterCancel is a leaked
// reader of those globals; the helper panics then, because returning would let
// a cleanup race with it and report a hang as a data race.
//
// hangMsg is the failure text for a run that had to be cancelled. The caller
// builds everything that needs the testing.T (the reconciler, the ledger) on
// the test goroutine and passes it in; the goroutine here touches no testing.T
// method.
func w1aListBoardsWatched(ctx context.Context, t *testing.T, limit time.Duration, hangMsg string, watchlist []sources.Board, srcs map[posting.ATS]sources.Source,
	reconcile reconcileFunc, warnf func(string, ...any)) (boardsReport, error) {
	t.Helper()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		rep boardsReport
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, e := listBoards(runCtx, watchlist, srcs, nil, reconcile, warnf, "")
		done <- result{r, e}
	}()
	select {
	case r := <-done:
		return r.rep, r.err
	case <-time.After(limit):
		cancel()
		select {
		case <-done:
			t.Fatal(hangMsg)
			return boardsReport{}, nil
		case <-time.After(w1aGraceAfterCancel):
			panic("w1a watchdog: listBoards did not return " + w1aGraceAfterCancel.String() + " after its context was cancelled; the goroutine still reads package globals, so the test cannot fail cleanly")
		}
	}
}
