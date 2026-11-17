package main

import (
	"context"
	"fmt"
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
		t.Run(fmt.Sprintf("parallel=%d", parallel), func(t *testing.T) {
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
			rep, err := w1aListBoardsWatched(context.Background(), t, 20*time.Second,
				"listBoards hangs after a board's List called runtime.Goexit",
				[]sources.Board{exit, sameHost, other}, srcs, tempReconciler(t).Reconcile, warn.warnf)
			if err != nil {
				t.Fatalf("the run ended on one board's Goexit: %v", err)
			}
			if len(rep.Items) != 3 {
				t.Fatalf("%d boards reported, want 3", len(rep.Items))
			}
			if got := rep.Items[0]; got.Outcome != boardFailed || !strings.Contains(got.Error, "exited without returning") || !strings.Contains(got.Error, "exitco") {
				t.Errorf("exiting board %+v, want failed and named", got)
			}
			for _, e := range rep.Items[1:] {
				if e.Outcome != boardRead {
					t.Errorf("board %s outcome %q, want read", e.Token, e.Outcome)
				}
			}
		})
	}
}
