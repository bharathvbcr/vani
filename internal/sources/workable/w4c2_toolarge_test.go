package workable

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

var w4c2NoStored = Options{Stored: func(sources.Board) []posting.Posting { return nil }}

// w4c2Oversize makes the detail of id larger than the cap the other details fit
// in, and returns the replies.
func w4c2Oversize(t *testing.T, ids []string, id string) map[string]srctest.Reply {
	t.Helper()
	r := replies(ids...)
	old := maxDetailBody
	maxDetailBody = int64(len(r[detailURL(ids[0])].Body)) + 64
	t.Cleanup(func() { maxDetailBody = old })
	r[detailURL(id)] = srctest.Reply{Status: 200, Body: []byte(detailJSON(id, `,"x":"`+strings.Repeat("a", int(maxDetailBody))+`"`))}
	return r
}

// One job whose detail is over the body cap must not fail its board on every
// run: it is reported as a short listing, and the rest are listed.
func TestW4c2OneOversizeDetailIsSkippedNotFatal(t *testing.T) {
	ids := w4cIDs(30)
	_, d := newSource(w4c2Oversize(t, ids, ids[7]))
	var shortfall int
	var first string
	ctx := srcutil.WithBadRowSink(context.Background(), func(_ sources.Board, n int, why string) { shortfall, first = n, why })
	got, err := w4cBounded(d, w4c2NoStored).List(ctx, board)
	if err != nil {
		t.Fatalf("one oversize detail failed the board: %v", err)
	}
	if len(got) != 29 || shortfall != 1 || !strings.Contains(first, "too large") {
		t.Fatalf("%d postings, shortfall %d (%q); want 29, 1, a reason naming the size", len(got), shortfall, first)
	}
}

// A stored posting whose detail is oversize is not aged as absent: the listing
// reads as short, so reconcile holds it.
func TestW4c2OversizeStoredRowIsHeldByTheShortListing(t *testing.T) {
	ids := w4cIDs(30)
	on, _ := w4cTurns(ids)
	sp := w4cStore(t, on[0])
	_, d := newSource(w4c2Oversize(t, ids, on[0]))
	var shortfall int
	ctx := srcutil.WithBadRowSink(context.Background(), func(_ sources.Board, n int, _ string) { shortfall = n })
	got, err := w4cBounded(d, Options{Stored: func(sources.Board) []posting.Posting { return []posting.Posting{sp} }}).List(ctx, board)
	if err != nil || shortfall != 1 || len(got) != 29 {
		t.Fatalf("%d postings, shortfall %d, err %v; want 29, 1, nil", len(got), shortfall, err)
	}
}

// Without a sink the caller cannot hold a short listing, so the board fails
// as it always did; and past the bad-row bound it fails too.
func TestW4c2OversizeFailsWithoutASinkOrPastTheBound(t *testing.T) {
	ids := w4cIDs(30)
	_, d := newSource(w4c2Oversize(t, ids, ids[7]))
	if _, err := w4cBounded(d, w4c2NoStored).List(context.Background(), board); !errors.Is(err, srcutil.ErrBodyTooLarge) {
		t.Fatalf("no sink: %v, want ErrBodyTooLarge", err)
	}
	r := w4c2Oversize(t, ids, ids[0])
	for _, id := range ids[1:5] { // MaxGone(30) = 3, so 4 oversize is over
		r[detailURL(id)] = r[detailURL(ids[0])]
	}
	_, d = newSource(r)
	ctx := srcutil.WithBadRowSink(context.Background(), func(sources.Board, int, string) {})
	if _, err := w4cBounded(d, w4c2NoStored).List(ctx, board); !errors.Is(err, srcutil.ErrBodyTooLarge) {
		t.Fatalf("past the bound: %v, want ErrBodyTooLarge", err)
	}
}
