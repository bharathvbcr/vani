package workday

import (
	"bytes"
	"errors"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// w4cPadded serves fb, except that job idx's detail body is padded with
// trailing spaces to exactly n bytes (valid JSON stays valid).
func w4cPadded(fb *fakeBoard, idx, n int) func(srctest.Request) (srctest.Reply, bool) {
	p, _ := escapePath(fb.jobs[idx].path)
	return func(r srctest.Request) (srctest.Reply, bool) {
		rp, ok := fb.handle(r)
		if r.Method == "GET" && r.URL == siteURL+p && ok && rp.Status == 200 {
			if len(rp.Body) > n {
				panic("detail fixture is larger than the padded size")
			}
			rp.Body = append(bytes.Clone(rp.Body), bytes.Repeat([]byte(" "), n-len(rp.Body))...)
		}
		return rp, ok
	}
}

// The detail read uses the shared 2 MiB detail cap, not srcutil.MaxBody
// (64 MiB): a detail of exactly the cap is read, one byte over fails the board.
func TestW4cWorkdayDetailReadIsCappedAtTheSharedDetailLimit(t *testing.T) {
	if srcutil.MaxDetailBody != 2<<20 {
		t.Fatalf("shared detail cap %d, want 2 MiB", srcutil.MaxDetailBody)
	}
	fb := missBoard(3)
	got, err := missSource(w4cPadded(fb, 0, srcutil.MaxDetailBody), nil).List(missNoRetry(), board)
	if err != nil || len(got) != 3 {
		t.Fatalf("a detail of exactly the cap: %d postings, err %v", len(got), err)
	}
	fb = missBoard(3)
	_, err = missSource(w4cPadded(fb, 0, srcutil.MaxDetailBody+1), nil).List(missNoRetry(), board)
	if !errors.Is(err, srcutil.ErrBodyTooLarge) {
		t.Fatalf("a detail one byte over the cap: err %v, want ErrBodyTooLarge", err)
	}
}
