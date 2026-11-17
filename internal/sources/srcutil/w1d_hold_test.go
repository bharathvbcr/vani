package srcutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

type w1dSink struct {
	skipped []int
	why     []string
}

func (s *w1dSink) ctx() context.Context {
	return WithBadRowSink(context.Background(), func(_ sources.Board, n int, first string) {
		s.skipped = append(s.skipped, n)
		s.why = append(s.why, first)
	})
}

// w1dStep runs a DetailStep over n stored unusable rows with limit, recording
// the requested rows. miss names rows whose request fails transiently.
func w1dStep(ctx context.Context, n, limit int, miss map[int]bool) (DetailResult, []int, error) {
	rows := make([]DetailRow, n)
	for i := range rows {
		rows[i] = DetailRow{ID: fmt.Sprintf("r%d", i), Stored: true, Unusable: true}
	}
	var asked []int
	res, err := DetailStep{
		Board: sources.Board{ATS: posting.Workday, Token: "t"},
		Rows:  n, Plan: PlanDetails(rows, 100, limit),
		Fetch: func(_ context.Context, i int) (*posting.Posting, error) {
			asked = append(asked, i)
			if miss[i] {
				return nil, fmt.Errorf("detail: %w", io.ErrUnexpectedEOF)
			}
			return &posting.Posting{Description: "read"}, nil
		},
		Reuse: func(int) posting.Posting { return posting.Posting{Description: "stored"} },
	}.Run(ctx)
	return res, asked, err
}

// 500 unusable stored rows with a 300 limit issue exactly 300 requests; the
// other 200 are neither listed (with an empty body or otherwise) nor silently
// omitted: they are reported to the sink as a shortfall, so the listing reads as
// short and the postings are held, not aged. The next batch is what the next
// run reads (the first 300 are no longer unusable once read).
func TestW1dStepCapsUnusableRereadsAndHoldsTheRest(t *testing.T) {
	var sk w1dSink
	res, asked, err := w1dStep(sk.ctx(), 500, 300, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 300 || asked[0] != 0 || asked[299] != 299 {
		t.Fatalf("%d requests (%v...), want exactly 300 for rows 0..299", len(asked), asked[:min(3, len(asked))])
	}
	if res.Held != 200 || res.Refreshed != 300 {
		t.Fatalf("result %+v, want 300 refreshed and 200 held", res)
	}
	for i, p := range res.Slots {
		if (i < 300) != (p != nil) {
			t.Fatalf("slot %d listed=%v: held rows must not be listed, read rows must", i, p != nil)
		}
	}
	if !slices.Equal(sk.skipped, []int{200}) {
		t.Fatalf("sink got %v, want one report of 200 held rows", sk.skipped)
	}
	// The next run: the 300 read rows are usable now, the 200 held ones are all
	// that is left, and all of them fit.
	var sk2 w1dSink
	res, asked, err = w1dStep(sk2.ctx(), 200, 300, nil)
	if err != nil || len(asked) != 200 || res.Held != 0 || len(sk2.skipped) != 0 {
		t.Fatalf("next run: asked %d, %+v, sink %v, %v", len(asked), res, sk2.skipped, err)
	}
}

// Exactly at the limit nothing is held and the sink is not called; one over
// holds one.
func TestW1dStepUnusableBoundary(t *testing.T) {
	var sk w1dSink
	if res, asked, err := w1dStep(sk.ctx(), 300, 300, nil); err != nil || len(asked) != 300 || res.Held != 0 || len(sk.skipped) != 0 {
		t.Fatalf("at the limit: asked %d, %+v, sink %v, %v", len(asked), res, sk.skipped, err)
	}
	var sk2 w1dSink
	if res, asked, err := w1dStep(sk2.ctx(), 301, 300, nil); err != nil || len(asked) != 300 || res.Held != 1 || !slices.Equal(sk2.skipped, []int{1}) {
		t.Fatalf("one over: asked %d, %+v, sink %v, %v", len(asked), res, sk2.skipped, err)
	}
}

// A transient miss on an unusable row is held, not a board failure: the row is
// not listed and is reported with the over-limit rows in one shortfall.
func TestW1dStepMissOnUnusableRowIsHeld(t *testing.T) {
	var sk w1dSink
	res, _, err := w1dStep(sk.ctx(), 10, 10, map[int]bool{4: true})
	if err != nil {
		t.Fatalf("a missed unusable row failed the board: %v", err)
	}
	if res.Slots[4] != nil || res.Held != 1 || res.Unanswered != 1 || res.Refreshed != 9 {
		t.Fatalf("result %+v, slot 4 %v", res, res.Slots[4])
	}
	if !slices.Equal(sk.skipped, []int{1}) {
		t.Fatalf("sink got %v, want one held row", sk.skipped)
	}
}

// The miss bound still applies: more transient misses than a board may leave
// for a later run fail it, whether the rows are unusable or not.
func TestW1dStepUnusableMissesAreBounded(t *testing.T) {
	var sk w1dSink
	miss := map[int]bool{}
	for i := range maxMissesPerBoard + 1 {
		miss[i] = true
	}
	_, _, err := w1dStep(sk.ctx(), 40, 40, miss)
	if !errors.Is(err, ErrTooManyMisses) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want too many misses wrapping the miss", err)
	}
	if len(sk.skipped) != 0 {
		t.Fatalf("a failed step reported %v to the sink", sk.skipped)
	}
}

// A non-transient failure on an unusable row fails the board, sink or not.
func TestW1dStepNonTransientFailureOnUnusableRowFails(t *testing.T) {
	var sk w1dSink
	rows := []DetailRow{{ID: "a", Stored: true, Unusable: true}}
	boom := errors.New("HTTP 500")
	_, err := DetailStep{
		Board: sources.Board{ATS: posting.Workday, Token: "t"}, Rows: 1, Plan: PlanDetails(rows, 100, 5),
		Fetch: func(context.Context, int) (*posting.Posting, error) { return nil, boom },
		Reuse: func(int) posting.Posting { return posting.Posting{} },
	}.Run(sk.ctx())
	if !errors.Is(err, boom) || len(sk.skipped) != 0 {
		t.Fatalf("err %v, sink %v", err, sk.skipped)
	}
}

// Without a sink nothing can be held: rows past the limit fail the board
// (ErrCannotHold) before any request, and a miss fails it as it always did.
func TestW1dStepWithoutASinkCannotHold(t *testing.T) {
	res, asked, err := w1dStep(context.Background(), 5, 3, nil)
	if !errors.Is(err, ErrCannotHold) || len(asked) != 0 {
		t.Fatalf("over the limit with no sink: err %v, %d requests, %+v", err, len(asked), res)
	}
	_, _, err = w1dStep(context.Background(), 5, 5, map[int]bool{2: true})
	if !errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, ErrTooManyMisses) {
		t.Fatalf("a miss with no sink: err %v, want the miss itself", err)
	}
}

// A cancelled context stops the step and reports nothing.
func TestW1dStepCancelledReportsNothing(t *testing.T) {
	var sk w1dSink
	ctx, cancel := context.WithCancel(sk.ctx())
	cancel()
	if _, _, err := w1dStep(ctx, 10, 3, nil); err == nil || len(sk.skipped) != 0 {
		t.Fatalf("err %v, sink %v", err, sk.skipped)
	}
}

// PlanDetails with no unusable rows is unchanged: Held is empty.
func TestW1dPlanDetailsHeldEmptyWithoutUnusable(t *testing.T) {
	pl := PlanDetails([]DetailRow{{ID: "n"}, {ID: "s", Stored: true}}, 100, 1)
	if len(pl.Held) != 0 {
		t.Fatalf("plan %+v", pl)
	}
}
