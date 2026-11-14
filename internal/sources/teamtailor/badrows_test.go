package teamtailor

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
			return New(one(payload)).List(ctx, board())
		},
		Payload: func(n int, bad map[int]string) string {
			items := make([]string, n)
			for i := range items {
				items[i] = fmt.Sprintf(`{"id":"id%d","title":"Role","url":"https://acme.teamtailor.com/jobs/%d-role","date_published":"2026-11-09T13:43:21+01:00"}`, i, i)
				if raw, ok := bad[i]; ok {
					items[i] = raw
				}
			}
			return `{"version":"https://jsonfeed.org/version/1.1","title":"Acme","items":[` + strings.Join(items, ",") + `]}`
		},
		BadRows: map[string]string{
			"empty title":   `{"id":"bad","title":"  ","url":"https://acme.teamtailor.com/jobs/1-x","date_published":"2026-11-09T13:43:21+01:00"}`,
			"off host":      `{"id":"bad","title":"Role","url":"https://evil.test/jobs/1-x","date_published":"2026-11-09T13:43:21+01:00"}`,
			"bad date":      `{"id":"bad","title":"Role","url":"https://acme.teamtailor.com/jobs/1-x","date_published":"Tuesday"}`,
			"other company": `{"id":"bad","title":"Role","url":"https://acme.teamtailor.com/jobs/1-x","date_published":"2026-11-09T13:43:21+01:00","_jobposting":{"hiringOrganization":{"sameAs":"https://other.teamtailor.com"}}}`,
		},
	})
}
