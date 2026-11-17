package srcutil

import (
	"context"
	"errors"
	"testing"
)

// Gone stored rows are left out (they age after MissesToDead) and draw only on
// the gone bound; transient misses draw only on the miss bound. Three of each,
// on a board that tolerates four gone and three misses, passes without one
// kind spending the other's allowance.
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
	if res.Gone != 3 || res.Unanswered != 3 {
		t.Fatalf("counts %+v", res)
	}
}

// An oversize detail fails the board (ErrBodyTooLarge): it is not a transient
// miss the plan can absorb.
func TestW4cadv2TooLargeDetailFailsBoard(t *testing.T) {
	d, _ := stepOf(10, 3, 0, map[int]error{1: ErrBodyTooLarge})
	if _, err := d.Run(context.Background()); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("err %v, want ErrBodyTooLarge", err)
	}
}
