package hirehive

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// w4c138: a duplicate key names where the two rows sit in the board's listing
// (page and item), not a running count of accepted postings, which is neither.
func TestW4cDuplicateKeyNamesBothRowsPositions(t *testing.T) {
	item := func(id string) string {
		return `{"id":"` + id + `","title":"Role","hosted_url":"https://amcsgroup.hirehive.com/` + id + `","description":{"html":"<p>Body</p>"}}`
	}
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		u, _ := url.Parse(r.URL)
		if u.Query().Get("page") == "1" {
			return ok(`{"meta":{"total_items":3,"has_next_page":true},"items":[` + item("job_0") + `,` + item("job_1") + `]}`), true
		}
		return ok(`{"meta":{"total_items":3,"has_next_page":false},"items":[` + item("job_1") + `]}`), true
	})
	got, err := New(d).List(context.Background(), board())
	if err == nil || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
	if want := "page 1 item 1 and page 2 item 0 share key"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not say %q", err, want)
	}
}

// A skipped row on the way does not shift the reported position: the first row
// of page 1 is bad, so the accepted count and the item index differ.
func TestW4cDuplicateKeyPositionCountsSkippedRows(t *testing.T) {
	item := func(id string) string {
		return `{"id":"` + id + `","title":"Role","hosted_url":"https://amcsgroup.hirehive.com/` + id + `","description":{"html":"<p>Body</p>"}}`
	}
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		u, _ := url.Parse(r.URL)
		if u.Query().Get("page") == "1" {
			return ok(`{"meta":{"total_items":4,"has_next_page":true},"items":[{"id":"x y","title":"bad"},` + item("job_1") + `,` + item("job_2") + `]}`), true
		}
		return ok(`{"meta":{"total_items":4,"has_next_page":false},"items":[` + item("job_2") + `]}`), true
	})
	ctx := srcutil.WithBadRowSink(context.Background(), func(sources.Board, int, string) {})
	_, err := New(d).List(ctx, board())
	if err == nil {
		t.Fatal("a duplicate key must fail the board")
	}
	if want := "page 1 item 2 and page 2 item 0 share key"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not say %q", err, want)
	}
}
