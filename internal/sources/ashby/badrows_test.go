package ashby

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources/rowstest"
)

func TestBadRows(t *testing.T) {
	rowstest.Check(t, rowstest.Case{
		List: func(ctx context.Context, payload string) ([]posting.Posting, error) {
			s, _ := newSource(one(200, payload))
			return s.List(ctx, board)
		},
		Payload: func(n int, bad map[int]string) string {
			rows := make([]string, n)
			for i := range rows {
				rows[i] = fmt.Sprintf(`{"id":"j%d","title":"T","jobUrl":"https://x.test/%d"}`, i, i)
				if raw, ok := bad[i]; ok {
					rows[i] = raw
				}
			}
			return `{"jobs":[` + strings.Join(rows, ",") + `]}`
		},
		BadRows: map[string]string{
			"empty title":    `{"id":"bad","title":"  ","jobUrl":"https://x.test/bad"}`,
			"javascript url": `{"id":"bad","title":"T","jobUrl":"javascript:alert(1)"}`,
			"id separator":   `{"id":"a:b","title":"T","jobUrl":"https://x.test/bad"}`,
		},
	})
}
