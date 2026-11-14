package jibe

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources/rowstest"
)

func TestBadRows(t *testing.T) {
	tmpl := fixtureJobs(t)[1]
	marshal := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	bad := func(k string, v any) string {
		j := synth(tmpl, "B"+rowstest.Index)
		j[k] = v
		return marshal(j)
	}
	rowstest.Check(t, rowstest.Case{
		List: func(ctx context.Context, payload string) ([]posting.Posting, error) {
			var rows []map[string]any
			if err := json.Unmarshal([]byte(payload), &rows); err != nil {
				t.Fatal(err)
			}
			f := &site{t: t, rows: map[query][]map[string]any{{term: "x"}: rows}}
			s, _ := newSource(f)
			return s.List(ctx, board("x"))
		},
		Payload: func(n int, badRows map[int]string) string {
			rows := make([]any, n)
			for i := range rows {
				rows[i] = synth(tmpl, fmt.Sprintf("R%d", i))
				if raw, ok := badRows[i]; ok {
					var j any
					if err := json.Unmarshal([]byte(raw), &j); err != nil {
						t.Fatal(err)
					}
					rows[i] = j
				}
			}
			return marshal(rows)
		},
		BadRows: map[string]string{
			"empty title":       bad("title", " "),
			"bad posted_date":   bad("posted_date", "yesterday"),
			"other client":      bad("client_code", "intel"),
			"offsite canonical": bad("meta_data", map[string]any{"canonical_url": "https://evil.example/jobs/5"}),
		},
	})
}
