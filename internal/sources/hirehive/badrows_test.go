package hirehive

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/rowstest"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func goodItem(i int) string {
	return fmt.Sprintf(`{"id":"job_%d","title":"Role","hosted_url":"https://amcsgroup.hirehive.com/%d"}`, i, i)
}

func TestBadRows(t *testing.T) {
	rowstest.Check(t, rowstest.Case{
		List: func(ctx context.Context, payload string) ([]posting.Posting, error) {
			return New(one(payload)).List(ctx, board())
		},
		Payload: func(n int, bad map[int]string) string {
			items := make([]string, n)
			for i := range items {
				items[i] = goodItem(i)
				if raw, ok := bad[i]; ok {
					items[i] = raw
				}
			}
			return wrap(n, strings.Join(items, ","))
		},
		BadRows: map[string]string{
			"empty title": `{"id":"bad","title":"  ","hosted_url":"https://amcsgroup.hirehive.com/b"}`,
			"bad date":    `{"id":"bad","title":"Role","hosted_url":"https://amcsgroup.hirehive.com/b","published_date":"Tuesday"}`,
			"off host":    `{"id":"bad","title":"Role","hosted_url":"https://other.hirehive.com/b"}`,
		},
	})
}

// The board's own count still cross-checks the listing: a skipped row counts
// toward it, a row the board never sent does not, so a short page fails.
func TestSkippedRowsCountTowardTheBoardsTotal(t *testing.T) {
	ctx := skipCtx(nil)
	body := wrap(5, goodItem(1)+","+goodItem(2)+","+goodItem(3)+`,{"id":"bad","title":" ","hosted_url":"https://amcsgroup.hirehive.com/b"}`)
	// 4 rows against a total of 5: a row is missing, not merely unreadable.
	if got, err := New(srctest.New(map[string]srctest.Reply{firstURL: {Status: 200, Body: []byte(body)}})).List(ctx, board()); err == nil {
		t.Fatalf("a listing one row short of its total was accepted: %d postings", len(got))
	}
	body = wrap(4, goodItem(1)+","+goodItem(2)+","+goodItem(3)+`,{"id":"bad","title":" ","hosted_url":"https://amcsgroup.hirehive.com/b"}`)
	var n int
	ctx = skipCtx(&n)
	got, err := New(srctest.New(map[string]srctest.Reply{firstURL: {Status: 200, Body: []byte(body)}})).List(ctx, board())
	if err != nil || len(got) != 3 || n != 1 {
		t.Fatalf("got %d postings, %v, %d skipped; want 3 and 1", len(got), err, n)
	}
}

func skipCtx(n *int) context.Context {
	return srcutil.WithBadRowSink(context.Background(), func(_ sources.Board, k int, _ string) {
		if n != nil {
			*n += k
		}
	})
}
