package manatal

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
				rows[i] = fmt.Sprintf(`{"hash":"H%04d","position_name":"Role","description":"<p>Body</p>"}`, i)
				if raw, ok := bad[i]; ok {
					rows[i] = raw
				}
			}
			return fmt.Sprintf(`{"count":%d,"next":null,"results":[%s]}`, n, strings.Join(rows, ","))
		},
		BadRows: map[string]string{
			"empty title": `{"hash":"BAD1","position_name":" <b></b> ","description":"Body"}`,
			"colon hash":  `{"hash":"A:B","position_name":"Role","description":"Body"}`,
			"bad salary":  `{"hash":"BAD2","position_name":"Role","is_salary_visible":true,"salary_min":"not-a-number","currency_code":"USD","description":"Body"}`,
		},
	})
}
