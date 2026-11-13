package breezy

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
			return New(reply(payload)).List(ctx, board())
		},
		Payload: func(n int, bad map[int]string) string {
			rows := make([]string, n)
			for i := range rows {
				rows[i] = fmt.Sprintf(`{"id":"id%d","name":"Role","url":"https://thirdchannel-inc.breezy.hr/p/id%d"}`, i, i)
				if raw, ok := bad[i]; ok {
					rows[i] = raw
				}
			}
			return "[" + strings.Join(rows, ",") + "]"
		},
		BadRows: map[string]string{
			"empty title":   `{"id":"bad","name":"  ","url":"https://thirdchannel-inc.breezy.hr/p/bad"}`,
			"off host":      `{"id":"bad","name":"Role","url":"https://evil.test/p/bad"}`,
			"wrong company": `{"id":"bad","name":"Role","url":"https://thirdchannel-inc.breezy.hr/p/bad","company":{"friendly_id":"other-co"}}`,
		},
	})
}
