package personio

import (
	"context"
	"strconv"
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
				rows[i] = positionXML(strconv.Itoa(5000+i), "")
				if raw, ok := bad[i]; ok {
					rows[i] = raw
				}
			}
			return feed(rows...)
		},
		BadRows: map[string]string{
			"empty name":     `<position><id>7</id><office>Berlin</office><name>  </name></position>`,
			"id not numeric": `<position><id>abc</id><office>Berlin</office><name>T</name></position>`,
			"bad date":       `<position><id>8</id><office>Berlin</office><name>T</name><createdAt>soon</createdAt></position>`,
		},
	})
}
