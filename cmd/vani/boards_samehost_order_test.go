package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// Boards on one host start in watchlist order. launchBoardFetches scans the
// pending list with tryAcquire; a host slot released between the scan's failed
// try for c and its try for d lets d take it, so d starts before c. Here the
// scan is paused (by the "reading x" progress line, which the launcher emits
// from inside the scan) until a has finished and released, so the interleaving
// is forced, not left to the scheduler.
func TestListBoardsSameHostBoardsStartInWatchlistOrder(t *testing.T) {
	finishA := make(chan struct{})
	var cr reconciled
	var so startOrder
	srcs := map[posting.ATS]sources.Source{posting.Eightfold: fakeSource{posting.Eightfold, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
		so.add(b.Token)
		if b.Token == "a" {
			select {
			case <-finishA:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return boardPostings(b, 1), nil
	}}}
	var once sync.Once
	warnf := func(format string, args ...any) {
		if !strings.Contains(fmt.Sprintf(format, args...), "ef/x: reading") {
			return
		}
		once.Do(func() {
			close(finishA)
			// a's result is reconciled only after its host slot was released.
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				cr.mu.Lock()
				done := len(cr.got) > 0
				cr.mu.Unlock()
				if done {
					return
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
	boards := []sources.Board{efBoard("a", "h.test"), efBoard("c", "h.test"), efBoard("x", "x.test"), efBoard("d", "h.test")}
	if _, err := listBoards(t.Context(), boards, srcs, nil, cr.wrap(tempReconciler(t)), warnf, ""); err != nil {
		t.Fatal(err)
	}
	var h []string
	for _, tok := range so.order {
		if tok != "x" {
			h = append(h, tok)
		}
	}
	if strings.Join(h, "") != "acd" {
		t.Fatalf("boards of one host started %v, want watchlist order a c d", h)
	}
}
