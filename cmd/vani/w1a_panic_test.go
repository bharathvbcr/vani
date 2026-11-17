package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// w1aStringer panics when printed; fmt catches that, and the test pins that the
// board's error still comes out.
type w1aStringer struct{}

func (w1aStringer) String() string { panic("String panicked") }

// A fake adapter that panics in List fails only its own board. The error names
// the board and carries the panic value, bounded and on one line; the other
// board is read and reconciled, and the run goes on. The listing must still send
// its one result and release its slot, or the later board would never run, so
// the test would hang rather than fail (the harness timeout is the backstop).
func TestW1aPanickingAdapterFailsOnlyItsOwnBoard(t *testing.T) {
	huge := strings.Repeat("secret-row-content ", 5000)
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"string", "boom", "boom"},
		{"error", errors.New("an error value"), "an error value"},
		{"nil", nil, "nil"},
		{"huge string", huge, "secret-row-content"},
		{"multi-line", "line one\nline two", "line one line two"},
		{"printing panics", w1aStringer{}, "String panicked"},
	}
	for _, parallel := range []int{1, 4} {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				withBoardListParallel(t, parallel)
				bad := sources.Board{ATS: posting.Lever, Token: "badco", Company: "Bad"}
				good := sources.Board{ATS: posting.Greenhouse, Token: "goodco", Company: "Good"}
				after := sources.Board{ATS: posting.Ashby, Token: "afterco", Company: "After"}
				srcs := map[posting.ATS]sources.Source{
					posting.Lever: fakeSource{posting.Lever, func(context.Context, sources.Board) ([]posting.Posting, error) {
						panic(tc.value)
					}},
					posting.Greenhouse: fakeSource{posting.Greenhouse, func(_ context.Context, b sources.Board) ([]posting.Posting, error) {
						return boardPostings(b, 2), nil
					}},
					posting.Ashby: fakeSource{posting.Ashby, func(_ context.Context, b sources.Board) ([]posting.Posting, error) {
						return boardPostings(b, 1), nil
					}},
				}
				var warn warnLog
				rep, err := listBoards(context.Background(), []sources.Board{good, bad, after}, srcs, nil, tempReconciler(t).Reconcile, warn.warnf, "")
				if err != nil {
					t.Fatalf("the run ended on one board's panic: %v", err)
				}
				if rep.Read != 2 || rep.Failed != 1 || len(rep.Items) != 3 {
					t.Fatalf("totals: %+v", rep)
				}
				if rep.Items[0].Outcome != boardRead || rep.Items[2].Outcome != boardRead {
					t.Errorf("the boards around the panic were not read: %+v", rep.Items)
				}
				f := rep.Items[1]
				if f.Outcome != boardFailed || f.endedRun {
					t.Fatalf("panicking board: %+v", f)
				}
				if !strings.Contains(f.Error, "lv/badco") || !strings.Contains(f.Error, tc.want) {
					t.Errorf("error %q lacks the board or the panic value %q", f.Error, tc.want)
				}
				if strings.ContainsAny(f.Error, "\n\r") {
					t.Errorf("error spans lines: %q", f.Error)
				}
			})
		}
	}
}

// The panic value in the error is bounded, whatever the adapter put in it.
func TestW1aPanicValueIsBounded(t *testing.T) {
	_, err := listOneBoard(context.Background(), fakeSource{posting.Lever, func(context.Context, sources.Board) ([]posting.Posting, error) {
		panic(strings.Repeat("é", 100000))
	}}, sources.Board{ATS: posting.Lever, Token: "x"})
	if err == nil {
		t.Fatal("no error from a panicking listing")
	}
	if n := len(err.Error()); n > 700 {
		t.Errorf("error is %d bytes, want it bounded near %d", n, maxPanicRunes*4)
	}
}

// A listing that panics after building part of its rows returns none of them.
func TestW1aPanicAfterPartialListingKeepsNoRows(t *testing.T) {
	b := sources.Board{ATS: posting.Lever, Token: "half"}
	listed, err := listOneBoard(context.Background(), fakeSource{posting.Lever, func(_ context.Context, b sources.Board) ([]posting.Posting, error) {
		rows := boardPostings(b, 3)
		_ = rows
		panic("after three rows")
	}}, b)
	if err == nil || listed != nil {
		t.Fatalf("listed %v, err %v", listed, err)
	}
}

// A panic while the run's context is already cancelled is still that board's
// error, not a cancellation: the run's own cancel is what ends the run.
func TestW1aPanicWhileCancelledIsTheBoardsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := sources.Board{ATS: posting.Lever, Token: "gone"}
	srcs := map[posting.ATS]sources.Source{
		posting.Lever: fakeSource{posting.Lever, func(context.Context, sources.Board) ([]posting.Posting, error) {
			cancel()
			panic("while cancelled")
		}},
	}
	var warn warnLog
	rep, _ := listBoards(ctx, []sources.Board{b}, srcs, nil, tempReconciler(t).Reconcile, warn.warnf, "")
	if len(rep.Items) != 1 || rep.Items[0].Outcome != boardFailed || !strings.Contains(rep.Items[0].Error, "while cancelled") {
		t.Fatalf("report: %+v", rep)
	}
}
