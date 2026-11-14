package feed

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
			s, _ := newSource(payload, 200)
			return s.List(ctx, board)
		},
		Payload: func(n int, bad map[int]string) string {
			rows := make([]string, n)
			for i := range rows {
				rows[i] = job(fmt.Sprintf("id-%d", i), "")
				if raw, ok := bad[i]; ok {
					rows[i] = raw
				}
			}
			return doc(rows...)
		},
		BadRows: map[string]string{
			"empty title": `<job><partnerJobId>bad</partnerJobId><company>Shopify</company><title>  </title><applyUrl>https://www.shopify.com/careers/bad</applyUrl></job>`,
			"bad url":     `<job><partnerJobId>bad</partnerJobId><company>Shopify</company><title>T</title><applyUrl>javascript:alert(1)</applyUrl></job>`,
		},
	})
}
