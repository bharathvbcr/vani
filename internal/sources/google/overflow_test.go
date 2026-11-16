package google

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// page is a ds:1 data value holding the rows for ids, counting total matches.
func page(total int, ids ...int) string {
	var rows []string
	for _, id := range ids {
		rows = append(rows, one(fmt.Sprint(1000+id), fmt.Sprint("Role ", id)))
	}
	return wrap(fmt.Sprintf(`[[%s], null, %d, 20]`, strings.Join(rows, ","), total))
}

func span(lo, hi int) []int {
	var out []int
	for i := lo; i < hi; i++ {
		out = append(out, i)
	}
	return out
}

// "agent runtime" in the US on 2026-11-16: 22 matches, page 1 holds 20 in the
// site's order and 20 sorted by date, 22 distinct between them. The board is
// read whole with one more request, and never with a page parameter.
func TestOverflowingTermIsReadWholeWithTheDateSortedPage(t *testing.T) {
	first := page(22, span(0, 20)...)
	second := page(22, append(span(2, 20), 20, 21)...)
	s, d := newSource(map[string]string{searchURL("x", nil): first, dateSortedURL("x", nil): second})
	got, err := s.List(context.Background(), board("x"))
	if err != nil || len(got) != 22 {
		t.Fatalf("got %d postings, %v; want all 22", len(got), err)
	}
	seen := map[string]bool{}
	for _, p := range got {
		if seen[string(p.Key)] {
			t.Fatalf("posting %s listed twice", p.Key)
		}
		seen[string(p.Key)] = true
	}
	urls := d.URLs()
	if len(urls) != 2 {
		t.Fatalf("requests %v", urls)
	}
	for _, u := range urls {
		parsed, err := url.Parse(u)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Query().Has("page") || strings.Contains(u, "page=") {
			t.Errorf("url carries a page parameter: %s", u)
		}
	}
	if q, _ := url.Parse(urls[1]); q.Query().Get("sort_by") != "date" || q.Query().Get("q") != "x" {
		t.Errorf("second request %s, want the term sorted by date", urls[1])
	}
}

// A term page 1 holds is never asked for twice.
func TestTermPageOneHoldsIsReadOnce(t *testing.T) {
	s, d := newSource(map[string]string{searchURL("x", nil): page(3, 0, 1, 2)})
	if got, err := s.List(context.Background(), board("x")); err != nil || len(got) != 3 || len(d.URLs()) != 1 {
		t.Fatalf("got %d postings, %v, %d requests", len(got), err, len(d.URLs()))
	}
}

// The two pages are kept only when they prove every match was read.
func TestOverflowThatTheTwoPagesDoNotProveFailsTheBoard(t *testing.T) {
	for name, second := range map[string]string{
		"union short of the total":   page(30, append(span(2, 20), 20, 21)...),
		"date page counts another":   page(23, append(span(2, 20), 20, 21)...),
		"same rows in both orders":   page(22, span(0, 20)...),
		"unreadable row":             wrap(`[[` + one("abc", "A") + `], null, 22, 20]`),
		"date page has no ds:1 data": `<html><body>Before you continue to Google</body></html>`,
	} {
		t.Run(name, func(t *testing.T) {
			total := 22
			if name == "union short of the total" {
				total = 30
			}
			s, _ := newSource(map[string]string{
				searchURL("x", nil):     page(total, span(0, 20)...),
				dateSortedURL("x", nil): second,
			})
			got, err := s.List(context.Background(), board("x"))
			if !errors.Is(err, ErrTruncated) || got != nil {
				t.Fatalf("got %d postings, %v; want ErrTruncated", len(got), err)
			}
		})
	}
}

// A total beyond what two page-1 views can hold is ErrTruncated without the
// date-sorted request: it could only end in the same error.
func TestTotalBeyondTwoViewsSkipsTheDateSortedRequest(t *testing.T) {
	s, d := newSource(map[string]string{searchURL("x", nil): page(300, span(0, 20)...)})
	got, err := s.List(context.Background(), board("x"))
	if !errors.Is(err, ErrTruncated) || got != nil {
		t.Fatalf("got %d postings, %v; want ErrTruncated", len(got), err)
	}
	if urls := d.URLs(); len(urls) != 1 || urls[0] != searchURL("x", nil) {
		t.Fatalf("requests %v, want only page 1", urls)
	}
	if !strings.Contains(err.Error(), "could not cover") {
		t.Errorf("err %q does not say the second view was not asked", err)
	}
}

// A total that two full views could hold still asks for the second view.
func TestTotalTwoViewsCanHoldAsksForTheDateSortedPage(t *testing.T) {
	first := page(40, span(0, 20)...)
	second := page(40, span(20, 40)...)
	s, d := newSource(map[string]string{searchURL("x", nil): first, dateSortedURL("x", nil): second})
	got, err := s.List(context.Background(), board("x"))
	if err != nil || len(got) != 40 || len(d.URLs()) != 2 {
		t.Fatalf("got %d postings, %v, %d requests; want 40 and 2", len(got), err, len(d.URLs()))
	}
}

// A date-sorted page that is rate limited stays a rate limit, so the run
// treats the board as one to retry next run, and is still truncation.
func TestDateSortedPageRateLimitedKeepsBothCauses(t *testing.T) {
	d := srctest.New(map[string]srctest.Reply{
		searchURL("x", nil):     {Status: 200, Body: []byte(page(22, span(0, 20)...))},
		dateSortedURL("x", nil): {Status: 429},
	})
	s, _ := newSource(nil)
	s.d = d
	ctx := srcutil.WithRetryBudget(context.Background(), 0)
	_, err := s.List(ctx, board("x"))
	if !errors.Is(err, ErrTruncated) || !errors.Is(err, srcutil.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrTruncated and ErrRateLimited", err)
	}
}

func TestDateSortedURLIsSearchURLSortedByDate(t *testing.T) {
	f := map[string][]string{"location": {"United States"}}
	u, err := url.Parse(dateSortedURL("LLM inference", f))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("q") != "LLM inference" || q.Get("location") != "United States" || q.Get("sort_by") != "date" || q.Has("page") {
		t.Errorf("query %v", q)
	}
}
