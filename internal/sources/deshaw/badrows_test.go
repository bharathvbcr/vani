package deshaw

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources/rowstest"
)

func TestBadRows(t *testing.T) {
	fx := fixture(t)
	base := entries(fx, "regularJobs")[0]
	asJSON := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	bad := func(edit func(d map[string]any)) string {
		e := cloneEntry(t, base, 9001)
		edit(e["data"].(map[string]any))
		return asJSON(e)
	}
	rowstest.Check(t, rowstest.Case{
		List: func(ctx context.Context, payload string) ([]posting.Posting, error) {
			var m map[string]any
			if err := json.Unmarshal([]byte(payload), &m); err != nil {
				t.Fatal(err)
			}
			return (&source{d: serve(wrap(t, m), 200), now: func() time.Time { return fixedNow }}).List(ctx, board())
		},
		Payload: func(n int, badRows map[int]string) string {
			rows := make([]any, n)
			for i := range rows {
				rows[i] = cloneEntry(t, base, 100+i)
				if raw, ok := badRows[i]; ok {
					var e any
					if err := json.Unmarshal([]byte(raw), &e); err != nil {
						t.Fatal(err)
					}
					rows[i] = e
				}
			}
			m := fixture(t)
			pageProps(m)["regularJobs"] = rows
			pageProps(m)["internships"] = []any{}
			return asJSON(m)
		},
		BadRows: map[string]string{
			"empty title":     bad(func(d map[string]any) { d["displayName"] = "   " }),
			"jobUrl climbs":   bad(func(d map[string]any) { d["jobUrl"] = "../admin-9001" }),
			"jobUrl wrong id": bad(func(d map[string]any) { d["jobUrl"] = "Role-5" }),
		},
	})
}
