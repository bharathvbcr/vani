package main

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// mixedWatchlist is hosts hosts with per boards each, interleaved so that a
// host's boards are not adjacent in the watchlist.
func mixedWatchlist(hosts, per int) []sources.Board {
	var out []sources.Board
	for j := 0; j < per; j++ {
		for h := 0; h < hosts; h++ {
			out = append(out, efBoard(fmt.Sprintf("h%d-%d", h, j), fmt.Sprintf("h%d.test", h)))
		}
	}
	return out
}

func hostOfToken(tok string) string {
	for i := range tok {
		if tok[i] == '-' {
			return tok[:i]
		}
	}
	return tok
}

// Start order per host is watchlist order for every pool size, with fetches
// that finish at uneven speeds, and no host ever has two fetches in flight.
func TestListBoardsAttackPerHostStartOrderAcrossParallelism(t *testing.T) {
	for _, par := range []int{1, 2, 3, 16} {
		t.Run(fmt.Sprint("parallel", par), func(t *testing.T) {
			withBoardListParallel(t, par)
			boards := mixedWatchlist(5, 4)
			var so startOrder
			var mu sync.Mutex
			inflight := map[string]int{}
			var viol atomic.Int32
			srcs := map[posting.ATS]sources.Source{posting.Eightfold: fakeSource{posting.Eightfold, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
				h := hostOfToken(b.Token)
				mu.Lock()
				inflight[h]++
				if inflight[h] > 1 {
					viol.Add(1)
				}
				mu.Unlock()
				so.add(b.Token)
				time.Sleep(time.Duration(len(b.Token)*int(b.Token[len(b.Token)-1]%3)) * time.Millisecond)
				mu.Lock()
				inflight[h]--
				mu.Unlock()
				return boardPostings(b, 1), nil
			}}}
			var cr reconciled
			if _, err := listBoards(t.Context(), boards, srcs, nil, cr.wrap(tempReconciler(t)), func(string, ...any) {}, ""); err != nil {
				t.Fatal(err)
			}
			if viol.Load() != 0 {
				t.Fatalf("a host had two fetches in flight %d times", viol.Load())
			}
			byHost := map[string][]string{}
			for _, tok := range so.order {
				byHost[hostOfToken(tok)] = append(byHost[hostOfToken(tok)], tok)
			}
			for h, got := range byHost {
				want := []string{h + "-0", h + "-1", h + "-2", h + "-3"}
				if !slices.Equal(got, want) {
					t.Fatalf("host %s started %v, want %v", h, got, want)
				}
			}
			want := make([]string, len(boards))
			for i, b := range boards {
				want[i] = b.Token
			}
			if !slices.Equal(cr.got, want) {
				t.Fatalf("reconciled %v, want watchlist order %v", cr.got, want)
			}
		})
	}
}

// Throughput: with more hosts than pool slots, the pool is filled (max
// concurrent fetches reaches the pool size) even though every host has parked
// boards behind its first.
func TestListBoardsAttackPoolStaysFullAcrossHosts(t *testing.T) {
	const par = 4
	withBoardListParallel(t, par)
	boards := mixedWatchlist(8, 3)
	var cur, peak atomic.Int32
	reached := make(chan struct{})
	var once sync.Once
	srcs := map[posting.ATS]sources.Source{posting.Eightfold: fakeSource{posting.Eightfold, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
		n := cur.Add(1)
		defer cur.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		if n >= par {
			once.Do(func() { close(reached) })
		}
		select {
		case <-reached:
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
		}
		return boardPostings(b, 1), nil
	}}}
	if _, err := listBoards(t.Context(), boards, srcs, nil, tempReconciler(t).Reconcile, func(string, ...any) {}, ""); err != nil {
		t.Fatal(err)
	}
	if peak.Load() != par {
		t.Fatalf("peak concurrent fetches %d, want %d (pool underused)", peak.Load(), par)
	}
}

// Cancelled mid-pass: the run returns, every fetch goroutine ends, and none
// are left behind.
func TestListBoardsAttackCancelMidPassLeaksNothing(t *testing.T) {
	withBoardListParallel(t, 3)
	before := runtime.NumGoroutine()
	for round := 0; round < 5; round++ {
		ctx, cancel := context.WithCancel(t.Context())
		boards := mixedWatchlist(4, 6)
		var started atomic.Int32
		srcs := map[posting.ATS]sources.Source{posting.Eightfold: fakeSource{posting.Eightfold, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
			if started.Add(1) == 3 {
				cancel()
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}}}
		done := make(chan error, 1)
		go func() {
			_, err := listBoards(ctx, boards, srcs, nil, tempReconciler(t).Reconcile, func(string, ...any) {}, "")
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("cancelled run returned nil error")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("listBoards did not return after cancel")
		}
		cancel()
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before+2 {
		t.Fatalf("goroutines %d, was %d before: leak", n, before)
	}
}

// A host that appears once, and boards all on one host, both finish.
func TestListBoardsAttackSingletonAndSingleHostWatchlists(t *testing.T) {
	for _, par := range []int{1, 16} {
		withBoardListParallel(t, par)
		for _, boards := range [][]sources.Board{
			{efBoard("only-0", "only.test")},
			{efBoard("s-0", "s.test"), efBoard("s-1", "s.test"), efBoard("s-2", "s.test"), efBoard("s-3", "s.test")},
		} {
			var so startOrder
			srcs := map[posting.ATS]sources.Source{posting.Eightfold: fakeSource{posting.Eightfold, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
				so.add(b.Token)
				return boardPostings(b, 1), nil
			}}}
			if _, err := listBoards(t.Context(), boards, srcs, nil, tempReconciler(t).Reconcile, func(string, ...any) {}, ""); err != nil {
				t.Fatal(err)
			}
			if len(so.order) != len(boards) {
				t.Fatalf("parallel %d: started %v of %d", par, so.order, len(boards))
			}
			for i, b := range boards {
				if so.order[i] != b.Token {
					t.Fatalf("parallel %d: order %v", par, so.order)
				}
			}
		}
	}
}
