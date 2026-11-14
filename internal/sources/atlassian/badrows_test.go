package atlassian

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources/rowstest"
)

func TestBadRows(t *testing.T) {
	base := fixture(t)[0]
	raw := func(row map[string]any) string {
		out, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	bad := func(mut func(row map[string]any)) string {
		row := withID(t, base, 9999999)
		mut(row)
		return raw(row)
	}
	rowstest.Check(t, rowstest.Case{
		List: func(ctx context.Context, payload string) ([]posting.Posting, error) {
			s := &source{d: serve([]byte(payload), 200), now: func() time.Time { return fixedNow }}
			return s.List(ctx, board())
		},
		Payload: func(n int, badRows map[int]string) string {
			rows := make([]string, n)
			for i := range rows {
				rows[i] = raw(withID(t, base, 100000+i))
				if r, ok := badRows[i]; ok {
					rows[i] = r
				}
			}
			return "[" + strings.Join(rows, ",") + "]"
		},
		BadRows: map[string]string{
			"empty title":       bad(func(r map[string]any) { r["title"] = "  " }),
			"missing id":        bad(func(r map[string]any) { delete(r, "id") }),
			"portal id differs": bad(func(r map[string]any) { r["portalJobPost"].(map[string]any)["id"] = 1 }),
		},
	})
}
