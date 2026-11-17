package main

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// With one board listing at a time the caller, not the fetching goroutine,
// returns the slot, after it has read that board's result. The launcher must
// therefore never start a later board while an earlier one is parked: the later
// board would hold the only slot while the caller waits for the earlier board's
// result, which cannot start until the slot is returned.
func TestW1adiagSerialLaunchKeepsWatchlistOrder(t *testing.T) {
	withBoardListParallel(t, 1)
	const n = 300
	var watchlist []sources.Board
	for i := 0; i < n; i++ {
		watchlist = append(watchlist, sources.Board{ATS: posting.Greenhouse, Token: fmt.Sprintf("co%d", i), Company: "C", Host: fmt.Sprintf("h%d.example", i)})
	}
	srcs := map[posting.ATS]sources.Source{
		posting.Greenhouse: fakeSource{posting.Greenhouse, func(context.Context, sources.Board) ([]posting.Posting, error) {
			return nil, nil
		}},
	}
	reconcile := func(context.Context, sources.Board, []posting.Posting, time.Time) (pipeline.Summary, error) {
		return pipeline.Summary{}, nil
	}
	prev := runtime.GOMAXPROCS(8)
	defer runtime.GOMAXPROCS(prev)
	for round := 0; round < 20; round++ {
		done := make(chan error, 1)
		go func() {
			rep, err := listBoards(context.Background(), watchlist, srcs, nil, reconcile, func(string, ...any) {}, "")
			if err == nil && rep.Read != n {
				err = fmt.Errorf("read %d of %d", rep.Read, n)
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("round %d: listBoards hangs: a later board holds the only slot while the caller waits for an earlier one", round)
		}
	}
}
