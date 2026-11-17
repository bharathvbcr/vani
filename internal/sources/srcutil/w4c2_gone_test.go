package srcutil

import (
	"context"
	"errors"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources"
)

// A stored row the listing still names that answers gone is returned as stored
// and counted unanswered, not left out (which would age it as absent); a new
// row that answers gone is still left out.
func TestW4c2GoneStoredRowIsKeptNewRowIsLeftOut(t *testing.T) {
	// rows 0..9 new, 10..19 stored and re-read, 20..39 reused.
	d, _ := stepOf(40, 10, 10, map[int]error{1: errRowGone, 13: errRowGone})
	res, err := d.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Slots[1] != nil {
		t.Fatalf("a new row that is gone was listed: %v", keysOf(res.Slots))
	}
	if res.Slots[13] == nil || res.Slots[13].Key != "stored-13" {
		t.Fatalf("a stored row that answered gone was left out: %v", keysOf(res.Slots))
	}
	if res.Gone != 2 || res.Unanswered != 1 || res.LeftOut != 0 || res.Refreshed != 9 || res.Fetched != 9 {
		t.Fatalf("counts %+v", res)
	}
}

// A stored row with unreadable text cannot be returned as stored; its gone
// answer is held through the sink (a short listing), and without a sink it
// fails the board rather than ageing the row.
func TestW4c2GoneUnusableStoredRowIsHeld(t *testing.T) {
	mk := func() DetailStep {
		d, _ := stepOf(10, 0, 1, map[int]error{0: errRowGone})
		d.Plan.Unusable = 1
		return d
	}
	var shortfall int
	ctx := WithBadRowSink(context.Background(), func(_ sources.Board, n int, _ string) { shortfall = n })
	res, err := mk().Run(ctx)
	if err != nil || res.Held != 1 || res.Unanswered != 1 || res.Slots[0] != nil || shortfall != 1 {
		t.Fatalf("with a sink: %+v, shortfall %d, %v", res, shortfall, err)
	}
	if _, err := mk().Run(context.Background()); !errors.Is(err, ErrCannotHold) {
		t.Fatalf("without a sink: %v, want ErrCannotHold", err)
	}
}

// Stored rows that answer gone count toward the MaxGone bound: a detail
// endpoint answering 404 for everything still fails the board.
func TestW4c2GoneStoredRowsCountTowardTheBound(t *testing.T) {
	gone := map[int]error{}
	for i := range 4 {
		gone[i] = errRowGone
	}
	d, _ := stepOf(40, 0, 30, gone)
	if _, err := d.Run(context.Background()); err == nil {
		t.Fatal("4 of 30 stored rows gone did not fail the board")
	}
}
