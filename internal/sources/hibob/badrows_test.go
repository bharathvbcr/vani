package hibob

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
			return New(one(payload)).List(ctx, board())
		},
		Payload: func(n int, bad map[int]string) string {
			rows := make([]string, n)
			for i := range rows {
				rows[i] = fmt.Sprintf(`{"id":"id%d","title":"Role","description":"Body"}`, i)
				if raw, ok := bad[i]; ok {
					rows[i] = raw
				}
			}
			return wrap(strings.Join(rows, ","))
		},
		BadRows: map[string]string{
			"empty title": `{"id":"bad","title":" <b></b> ","description":"Body"}`,
			"bad date":    `{"id":"bad","title":"Role","publishedAt":"Tuesday","description":"Body"}`,
			"colon id":    `{"id":"a:b","title":"Role","description":"Body"}`,
		},
	})
}
