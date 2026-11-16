package srcutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

var errRowGone = errors.New("row gone")

// stepTimeout is a transient request failure.
func stepTimeout() error {
	return fmt.Errorf("Get x: http2: timeout awaiting response headers: %w", os.ErrDeadlineExceeded)
}

// stepOf is a DetailStep over rows 0..rows-1 whose plan holds the first newRows
// as new rows, the next refresh as re-reads and the rest as reused. Fetch
// answers a posting keyed by the row, unless errs holds an error for it.
func stepOf(rows, newRows, refresh int, errs map[int]error) (DetailStep, *[]int) {
	pl := DetailPlan{New: newRows}
	for i := range rows {
		if i < newRows+refresh {
			pl.Fetch = append(pl.Fetch, i)
		} else {
			pl.Reuse = append(pl.Reuse, i)
		}
	}
	var asked []int
	return DetailStep{
		Rows: rows, Plan: pl, CanDefer: true,
		Fetch: func(_ context.Context, i int) (*posting.Posting, error) {
			asked = append(asked, i)
			if err := errs[i]; err != nil {
				return nil, err
			}
			return &posting.Posting{Key: posting.Key(fmt.Sprint("fetched-", i))}, nil
		},
		Reuse: func(i int) posting.Posting { return posting.Posting{Key: posting.Key(fmt.Sprint("stored-", i))} },
		Gone:  errRowGone,
		GoneErr: func(gone, fetch int) error {
			return fmt.Errorf("%d of %d gone", gone, fetch)
		},
		FetchErr: func(i int, err error) error { return fmt.Errorf("row %d: %w", i, err) },
	}, &asked
}

func keysOf(slots []*posting.Posting) string {
	var out []string
	for _, p := range slots {
		if p == nil {
			out = append(out, "-")
		} else {
			out = append(out, string(p.Key))
		}
	}
	return strings.Join(out, " ")
}

func TestDetailStepReusesFetchesAndAccounts(t *testing.T) {
	d, asked := stepOf(10, 2, 3, nil)
	res, err := d.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Reused != 5 || res.Fetched != 2 || res.Refreshed != 3 || res.Unanswered != 0 || len(*asked) != 5 {
		t.Fatalf("result %+v, asked %v", res, *asked)
	}
	if got := keysOf(res.Slots); got != "fetched-0 fetched-1 fetched-2 fetched-3 fetched-4 stored-5 stored-6 stored-7 stored-8 stored-9" {
		t.Fatalf("slots %s", got)
	}
	if n := len(Postings(res.Slots)); n != 10 {
		t.Fatalf("Postings = %d", n)
	}
}

func TestDetailStepAbsorbsAMissedNewRowAndKeepsAMissedStoredRow(t *testing.T) {
	// 30 requests (MaxMissed 3): 25 new rows, then 5 re-reads.
	d, _ := stepOf(40, 25, 5, map[int]error{1: stepTimeout(), 27: stepTimeout()})
	left := 0
	d.LeftOut = func(int, error) error { left++; return nil }
	res, err := d.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Unanswered != 2 || res.LeftOut != 1 || left != 1 || res.Fetched != 24 || res.Refreshed != 4 {
		t.Fatalf("result %+v, LeftOut hook ran %d times", res, left)
	}
	// Row 1 is new and missed: left out. Row 27 is a re-read that missed: kept
	// as stored.
	if res.Slots[1] != nil || res.Slots[27] == nil || res.Slots[27].Key != "stored-27" {
		t.Fatalf("slots %s", keysOf(res.Slots))
	}
}

func TestDetailStepLeftOutHookCanRefuseAMissedNewRow(t *testing.T) {
	d, _ := stepOf(10, 3, 0, map[int]error{1: stepTimeout()})
	refusal := errors.New("may be stored")
	d.LeftOut = func(i int, err error) error { return fmt.Errorf("row %d: %w: %w", i, refusal, err) }
	_, err := d.Run(context.Background())
	if !errors.Is(err, refusal) || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("err = %v, want the refusal wrapping the timeout", err)
	}
}

func TestDetailStepGoneBound(t *testing.T) {
	// 30 fetched: up to MaxGone(30) = 3 gone are left out; the 4th fails.
	gone := map[int]error{0: errRowGone, 1: errRowGone, 2: errRowGone}
	d, _ := stepOf(40, 30, 0, gone)
	res, err := d.Run(context.Background())
	if err != nil || res.Gone != 3 || res.Fetched != 27 {
		t.Fatalf("3 gone: %+v, %v", res, err)
	}
	gone[3] = errRowGone
	d, _ = stepOf(40, 30, 0, gone)
	res, err = d.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "4 of 30 gone") || res.Gone != 4 {
		t.Fatalf("4 gone: %+v, %v", res, err)
	}
	// A single fetched row gone with nothing reused is an endpoint failing.
	d, _ = stepOf(1, 1, 0, map[int]error{0: errRowGone})
	if _, err = d.Run(context.Background()); err == nil {
		t.Fatal("the only row gone with nothing reused must fail the board")
	}
	d, _ = stepOf(2, 1, 0, map[int]error{0: errRowGone})
	if _, err = d.Run(context.Background()); err != nil {
		t.Fatalf("one new row gone beside a reused one: %v", err)
	}
}

func TestDetailStepFailsOnANonTransientError(t *testing.T) {
	boom := errors.New("HTTP 500")
	d, _ := stepOf(10, 3, 0, map[int]error{1: boom})
	res, err := d.Run(context.Background())
	if !errors.Is(err, boom) || errors.Is(err, ErrTooManyMisses) || !strings.HasPrefix(err.Error(), "row 1: ") {
		t.Fatalf("err = %v", err)
	}
	if res.Fetched != 1 {
		t.Fatalf("the report must count what ran before the failure: %+v", res)
	}
}

// The board's error names the cap and the misses already absorbed, and still
// wraps the timeout.
func TestDetailStepCapHitNamesTheCountAndKeepsTheCause(t *testing.T) {
	errs := map[int]error{}
	for _, i := range []int{0, 1, 2, 3} {
		errs[i] = stepTimeout()
	}
	d, _ := stepOf(40, 30, 0, errs)
	res, err := d.Run(context.Background())
	if err == nil {
		t.Fatal("four misses must fail the board")
	}
	if !errors.Is(err, ErrTooManyMisses) || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("err = %v, want ErrTooManyMisses wrapping the timeout", err)
	}
	if !strings.Contains(err.Error(), "3 already left") || !strings.HasPrefix(err.Error(), "row 3: ") || res.Unanswered != 3 {
		t.Fatalf("err = %v, result %+v", err, res)
	}
}

func TestDetailStepSkippedRowsAreAnswered(t *testing.T) {
	d, _ := stepOf(2, 2, 0, nil)
	d.Fetch = func(_ context.Context, i int) (*posting.Posting, error) {
		if i == 0 {
			return nil, nil
		}
		return &posting.Posting{Key: "k"}, nil
	}
	res, err := d.Run(context.Background())
	if err != nil || res.Skipped != 1 || res.Fetched != 1 || len(Postings(res.Slots)) != 1 {
		t.Fatalf("%+v, %v", res, err)
	}
	// Everything skipped is still an answer: the endpoint responded.
	d.Fetch = func(context.Context, int) (*posting.Posting, error) { return nil, nil }
	if _, err = d.Run(context.Background()); err != nil {
		t.Fatalf("all skipped: %v", err)
	}
}

func TestDetailStepNothingAnsweredIsDoneErr(t *testing.T) {
	// One fetched row with nothing reused: MaxMissed is 0, so the miss refuses.
	d, _ := stepOf(1, 1, 0, map[int]error{0: stepTimeout()})
	if _, err := d.Run(context.Background()); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a lone miss with nothing reused must fail with its cause: %v", err)
	}
	// Two new rows, nothing reused: one miss is absorbed and the other is gone,
	// so nothing was answered.
	d, _ = stepOf(2, 2, 0, map[int]error{0: stepTimeout(), 1: errRowGone})
	d.DoneErr = func(err error) error { return fmt.Errorf("board: %w", err) }
	_, err := d.Run(context.Background())
	if !errors.Is(err, ErrNothingAnswered) || !strings.HasPrefix(err.Error(), "board: ") {
		t.Fatalf("err = %v", err)
	}
}

func TestDetailStepStopsOnACancelledContext(t *testing.T) {
	d, asked := stepOf(5, 5, 0, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Run(ctx); !errors.Is(err, context.Canceled) || len(*asked) != 0 {
		t.Fatalf("err = %v, asked %v", err, *asked)
	}
}

func TestDetailStepAcceptedHookFailsTheBoard(t *testing.T) {
	d, _ := stepOf(4, 4, 0, nil)
	stop := errors.New("stop")
	d.Accepted = func(n, _ int, _ posting.Posting) error {
		if n == 2 {
			return stop
		}
		return nil
	}
	res, err := d.Run(context.Background())
	if !errors.Is(err, stop) || res.Fetched != 3 {
		t.Fatalf("%+v, %v", res, err)
	}
}

func TestUniquePostingsKeepsTheFirstOfEachKey(t *testing.T) {
	a, b := posting.Posting{Key: "a", Title: "first"}, posting.Posting{Key: "a", Title: "second"}
	got := UniquePostings([]*posting.Posting{&a, nil, &b})
	if len(got) != 1 || got[0].Title != "first" {
		t.Fatalf("%+v", got)
	}
}
