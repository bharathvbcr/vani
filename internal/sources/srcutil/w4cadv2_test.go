package srcutil

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources"
)

// A board whose only fetched row is a stored posting that answers gone: round 2
// says a stored row the listing still names is kept as stored, but the
// "every fetched row was gone and nothing was reused" bound still fails the
// board, so the one-posting board errors on every refresh turn instead of
// keeping the row.
func TestW4cadv2SingleStoredGoneRowIsKeptNotAFailedBoard(t *testing.T) {
	d, _ := stepOf(1, 0, 1, map[int]error{0: errRowGone})
	res, err := d.Run(context.Background())
	if err != nil {
		t.Fatalf("a lone stored row that answered gone failed the board: %v", err)
	}
	if res.Slots[0] == nil {
		t.Fatal("the stored row was not kept")
	}
}

// Stored rows answering gone do not draw on the transient miss bound: three of
// each, on a board that tolerates four gone and three misses, passes.
func TestW4cadv2GoneAndMissedStoredRowsDoNotDoubleCount(t *testing.T) {
	errs := map[int]error{}
	for i := range 3 {
		errs[i] = errRowGone
	}
	for i := 3; i < 6; i++ {
		errs[i] = context.DeadlineExceeded
	}
	d, _ := stepOf(60, 0, 40, errs)
	res, err := d.Run(context.Background())
	if err != nil {
		t.Fatalf("3 gone + 3 transient stored rows failed the board: %v", err)
	}
	if res.Gone != 3 || res.Unanswered != 6 {
		t.Fatalf("counts %+v", res)
	}
}

// An oversize detail on a board with a sink is a held bad row; the report must
// carry it (DetailResult.TooLarge) and the listing must say it was short.
func TestW4cadv2TooLargeRowIsSkippedAndReported(t *testing.T) {
	big := fmt.Errorf("fetch: %w", ErrBodyTooLarge)
	var short int
	var first string
	ctx := WithBadRowSink(context.Background(), func(_ sources.Board, n int, f string) { short, first = n, f })
	d, _ := stepOf(40, 5, 5, map[int]error{2: big, 7: big})
	res, err := d.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.TooLarge != 2 || short != 2 || first == "" {
		t.Fatalf("TooLarge %d, shortfall %d, first %q", res.TooLarge, short, first)
	}
	if res.Slots[2] != nil || res.Slots[7] != nil {
		t.Fatal("an oversize row was listed")
	}
}

// Without a sink an oversize detail still fails the board, as before round 2.
func TestW4cadv2TooLargeWithoutSinkFailsBoard(t *testing.T) {
	d, _ := stepOf(10, 3, 0, map[int]error{1: ErrBodyTooLarge})
	if _, err := d.Run(context.Background()); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("err %v, want ErrBodyTooLarge", err)
	}
}

// Every detail of a small board oversize: the board must not read as a healthy
// empty listing. With the sink it is a short listing of 1 of 1; check that the
// shortfall is reported.
func TestW4cadv2AllOversizeBoardReportsShortfall(t *testing.T) {
	var short int
	ctx := WithBadRowSink(context.Background(), func(_ sources.Board, n int, _ string) { short = n })
	d, _ := stepOf(1, 1, 0, map[int]error{0: ErrBodyTooLarge})
	res, err := d.Run(ctx)
	if err != nil {
		t.Fatalf("err %v", err)
	}
	if short != 1 || len(Postings(res.Slots)) != 0 {
		t.Fatalf("shortfall %d, postings %d", short, len(Postings(res.Slots)))
	}
}
