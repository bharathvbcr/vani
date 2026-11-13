package recruitee

import (
	"context"
	"strconv"
	"strings"
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
				rows[i] = offerJSON(strconv.Itoa(1000+i), "")
				if raw, ok := bad[i]; ok {
					rows[i] = raw
				}
			}
			return `{"offers":[` + strings.Join(rows, ",") + `]}`
		},
		BadRows: map[string]string{
			"blank title":    `{"id":1,"title":"   ","careers_url":"https://syrve.recruitee.com/o/t"}`,
			"javascript url": `{"id":2,"title":"T","careers_url":"javascript:alert(1)"}`,
			"missing url":    `{"id":3,"title":"T"}`,
		},
	})
}
