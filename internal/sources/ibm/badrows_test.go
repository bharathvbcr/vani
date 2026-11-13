package ibm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources/rowstest"
)

func TestBadRows(t *testing.T) {
	asJSON := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	base := fixtureHits(t)[2]
	bad := func(set map[string]any) string {
		set["field_text_01"] = "700" + rowstest.Index // a numeric string: json.Number reads it
		set["url"] = "https://careers.ibm.com/careers/JobDetail?jobId=700" + rowstest.Index
		return asJSON(clone(base, set))
	}
	rowstest.Check(t, rowstest.Case{
		List: func(ctx context.Context, payload string) ([]posting.Posting, error) {
			s, _ := newSource(constant(200, payload))
			return s.List(ctx, ibmBoard("x"))
		},
		Payload: func(n int, badRows map[int]string) string {
			hits := make([]string, n)
			for i := range hits {
				hits[i] = asJSON(numbered(t, 100+i, "Role"))
				if raw, ok := badRows[i]; ok {
					hits[i] = raw
				}
			}
			return fmt.Sprintf(`{"took":3,"timed_out":false,"hits":{"total":{"value":%d,"relation":"eq"},"hits":[%s]}}`, n, strings.Join(hits, ","))
		},
		BadRows: map[string]string{
			"bad effective date": bad(map[string]any{"title": "Role", "effectivedate": "soon"}),
			"empty title":        bad(map[string]any{"title": "  "}),
		},
	})
}
