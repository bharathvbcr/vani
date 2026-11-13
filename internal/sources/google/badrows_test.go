package google

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/rowstest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func TestBadRows(t *testing.T) {
	rowstest.Check(t, rowstest.Case{
		List: func(ctx context.Context, payload string) ([]posting.Posting, error) {
			s, _ := newSource(map[string]string{searchURL("x", nil): wrap(payload)})
			return s.List(ctx, board("x"))
		},
		Payload: func(n int, bad map[int]string) string {
			rows := make([]string, n)
			for i := range rows {
				rows[i] = one(fmt.Sprint(1000+i), "Role")
				if raw, ok := bad[i]; ok {
					rows[i] = raw
				}
			}
			return fmt.Sprintf(`[[%s], null, %d, 20]`, strings.Join(rows, ","), n)
		},
		BadRows: map[string]string{
			"id not numeric":       one("abc", "A"),
			"empty title":          one("7", " "),
			"too few fields":       `["8","A"]`,
			"locations wrong type": strings.Replace(one("9", "A"), `[["Mountain View, CA, USA"]]`, `"MV"`, 1),
		},
	})
}

func skipCtx() context.Context {
	return srcutil.WithBadRowSink(context.Background(), func(sources.Board, int, string) {})
}

// The page's own count still cross-checks the listing: a skipped row is one of
// the rows the page holds, so a page that holds fewer rows than it counts is
// still truncated, and a count over the page still fails.
func TestSkippedRowsDoNotHideATruncatedPage(t *testing.T) {
	u := searchURL("x", nil)
	page := wrap(fmt.Sprintf(`[[%s,%s,%s], null, 5, 20]`, one("1", "A"), one("2", "B"), one("x", "C")))
	s, _ := newSource(map[string]string{u: page})
	if got, err := s.List(skipCtx(), board("x")); err == nil {
		t.Fatalf("5 counted, 3 held: accepted %d postings", len(got))
	}
}
