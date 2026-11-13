package pinpoint

import (
	"context"
	"fmt"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources/rowstest"
)

func TestBadRows(t *testing.T) {
	rowstest.Check(t, rowstest.Case{
		List: func(ctx context.Context, payload string) ([]posting.Posting, error) {
			s, _ := newSource(payload)
			return s.List(ctx, board)
		},
		Payload: func(n int, bad map[int]string) string {
			rows := make([]string, n)
			for i := range rows {
				rows[i] = itemJSON(fmt.Sprintf("g%d", i), "")
				if raw, ok := bad[i]; ok {
					rows[i] = raw
				}
			}
			return data(rows...)
		},
		BadRows: map[string]string{
			"blank title":    `{"id":"bad","title":"  ","url":"https://workwithus.pinpointhq.com/p"}`,
			"javascript url": `{"id":"bad","title":"T","url":"javascript:alert(1)"}`,
			"id separator":   `{"id":"a:b","title":"T","url":"https://workwithus.pinpointhq.com/p"}`,
		},
	})
}
