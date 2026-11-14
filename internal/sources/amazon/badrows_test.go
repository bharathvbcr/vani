package amazon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources/rowstest"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

func TestBadRows(t *testing.T) {
	base := fixtureJobs(t)[0]
	row := func(mut func(j map[string]any)) string {
		j := map[string]any{}
		for k, v := range base {
			j[k] = v
		}
		mut(j)
		raw, err := json.Marshal(j)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	good := func(i int) string {
		return row(func(j map[string]any) {
			id := fmt.Sprint(30000000 + i)
			j["id_icims"], j["job_path"] = id, "/en/jobs/"+id+"/role"
		})
	}
	rowstest.Check(t, rowstest.Case{
		List: func(ctx context.Context, payload string) ([]posting.Posting, error) {
			s, _ := newSource(func(srctest.Request) (srctest.Reply, bool) {
				return srctest.Reply{Status: 200, Body: []byte(payload)}, true
			})
			return s.List(ctx, board("a"))
		},
		Payload: func(n int, bad map[int]string) string {
			rows := make([]string, n)
			for i := range rows {
				rows[i] = good(i)
				if raw, ok := bad[i]; ok {
					rows[i] = raw
				}
			}
			return fmt.Sprintf(`{"error":null,"hits":%d,"facets":{},"jobs":[%s]}`, n, strings.Join(rows, ","))
		},
		BadRows: map[string]string{
			"id not numeric": row(func(j map[string]any) { j["id_icims"] = "bad" + rowstest.Index }),
			"empty title": row(func(j map[string]any) {
				j["id_icims"], j["job_path"], j["title"] = "4000"+rowstest.Index, "/en/jobs/4000"+rowstest.Index+"/role", "  "
			}),
			"bad date": row(func(j map[string]any) {
				j["id_icims"], j["job_path"], j["posted_date"] = "5000"+rowstest.Index, "/en/jobs/5000"+rowstest.Index+"/role", "soon"
			}),
		},
	})
}
