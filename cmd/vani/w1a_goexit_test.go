package main

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// An adapter whose List ends its goroutine with runtime.Goexit fails only its
// own board: the board is reported failed with "exited without returning", it
// releases its host slot (a second board on the same host still runs after it),
// and the run goes on to read the boards after it.
func TestW1aGoexitInListFailsOnlyItsOwnBoard(t *testing.T) {
	for _, parallel := range []int{1, 4} {
		withBoardListParallel(t, parallel)
		exit := sources.Board{ATS: posting.Lever, Token: "exitco", Company: "Exit"}
		sameHost := sources.Board{ATS: posting.Lever, Token: "sameco", Company: "Same"}
		other := sources.Board{ATS: posting.Greenhouse, Token: "otherco", Company: "Other"}
		srcs := map[posting.ATS]sources.Source{
			posting.Lever: fakeSource{posting.Lever, func(_ context.Context, b sources.Board) ([]posting.Posting, error) {
				if b.Token == "exitco" {
					runtime.Goexit()
				}
				return boardPostings(b, 2), nil
			}},
			posting.Greenhouse: fakeSource{posting.Greenhouse, func(_ context.Context, b sources.Board) ([]posting.Posting, error) {
				return boardPostings(b, 1), nil
			}},
		}
		var warn warnLog
		type result struct {
			rep boardsReport
			err error
		}
		done := make(chan result, 1)
		go func() {
			rep, err := listBoards(context.Background(), []sources.Board{exit, sameHost, other}, srcs, nil, tempReconciler(t).Reconcile, warn.warnf, "")
			done <- result{rep, err}
		}()
		select {
		case r := <-done:
			if r.err != nil {
				t.Fatalf("parallel %d: the run ended on one board's Goexit: %v", parallel, r.err)
			}
			if len(r.rep.Items) != 3 {
				t.Fatalf("parallel %d: %d boards reported, want 3", parallel, len(r.rep.Items))
			}
			if got := r.rep.Items[0]; got.Outcome != boardFailed || !strings.Contains(got.Error, "exited without returning") || !strings.Contains(got.Error, "exitco") {
				t.Errorf("parallel %d: exiting board %+v, want failed and named", parallel, got)
			}
			for _, e := range r.rep.Items[1:] {
				if e.Outcome != boardRead {
					t.Errorf("parallel %d: board %s outcome %q, want read", parallel, e.Token, e.Outcome)
				}
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("parallel %d: listBoards hangs after a board's List called runtime.Goexit", parallel)
		}
	}
}
