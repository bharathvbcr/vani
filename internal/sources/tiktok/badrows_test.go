package tiktok

import (
	"context"
	"fmt"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources/rowstest"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

func TestBadRows(t *testing.T) {
	rowstest.Check(t, rowstest.Case{
		List: func(ctx context.Context, payload string) ([]posting.Posting, error) {
			s, _, _ := newSource(t, func(searchBody) srctest.Reply { return ok(payload) })
			return s.List(ctx, board)
		},
		Payload: func(n int, bad map[int]string) string {
			rows := make([]string, n)
			for i := range rows {
				rows[i] = rowJSON(fmt.Sprint(1000+i), "Role", "CT_223")
				if raw, ok := bad[i]; ok {
					rows[i] = raw
				}
			}
			return answerJSON(n, rows...)
		},
		// Ids are numbers the site uses twice nowhere: each bad row needs its own,
		// because a repeated id is the paging looping.
		BadRows: map[string]string{
			"empty title": rowJSON("900"+rowstest.Index, "  ", "CT_223"),
		},
	})
}
