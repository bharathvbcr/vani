package gem

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
			s, _ := newSource(reply(200, payload))
			return s.List(ctx, board)
		},
		Payload: func(n int, bad map[int]string) string {
			rows := make([]string, n)
			for i := range rows {
				rows[i] = fmt.Sprintf(`{"id":"p%d","title":"T","absolute_url":"https://jobs.gem.com/groq/p%d"}`, i, i)
				if raw, ok := bad[i]; ok {
					rows[i] = raw
				}
			}
			return "[" + strings.Join(rows, ",") + "]"
		},
		BadRows: map[string]string{
			"empty title":    `{"id":"bad","title":" ","absolute_url":"https://jobs.gem.com/groq/bad"}`,
			"javascript url": `{"id":"bad","title":"T","absolute_url":"javascript:alert(1)"}`,
		},
	})
}
