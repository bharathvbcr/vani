package apple

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func appleID(i int) string { return fmt.Sprint(300000000 + i) }

// appleSite is a jobs.apple.com of n positions for the search "a": its search
// pages (20 rows a page, reporting count as the total) and a details page for
// each position.
func appleSite(t *testing.T, n int, count string) map[string]string {
	t.Helper()
	jd := fixture(t)
	pages := map[string]string{}
	var rows [][3]string
	for i := range n {
		id := appleID(i)
		rows = append(rows, [3]string{id, "0001", "role-" + id})
		pages[detailURL(id, "0001", "role-"+id)] = detailsPage(t, with(jd, "positionId", id, "jobNumber", id+"-0001", "postingTitle", "Role "+id))
	}
	for p := 1; (p-1)*pageSize < max(n, 1); p++ {
		lo, hi := (p-1)*pageSize, min(p*pageSize, n)
		pages[pageURL("a", nil, p)] = searchPage(count, rows[lo:hi]...)
	}
	return pages
}

// withStored is a source over pages with the bounded detail step on: stored
// are the postings an earlier run stored; stats collects each report.
func withStored(pages map[string]string, stored []posting.Posting) (*source, *srctest.Doer, *[]Stats) {
	s, d := newSource(pages)
	var stats []Stats
	s.opt = &Options{Stored: func(sources.Board) []posting.Posting { return stored },
		Report: func(_ sources.Board, st Stats) { stats = append(stats, st) }}
	return s, d, &stats
}

func detailRequests(d *srctest.Doer) int {
	n := 0
	for _, u := range d.URLs() {
		if strings.Contains(u, "/details/") {
			n++
		}
	}
	return n
}

func accounted(st Stats) bool {
	return st.Reused+st.Fetched+st.Refreshed+st.Deferred+st.Gone+st.Unanswered == st.Listed
}

// With the stored postings to reuse, a board over the old 200 cap is listed to
// its count and its new positions fetched only up to MaxDetails a run; the rest
// are deferred: not returned, not stored, so their absence ages nothing.
func TestLargeBoardWithStoredPostingsIsBounded(t *testing.T) {
	n := MaxPostings + 50
	if n > MaxListed {
		t.Fatalf("fixture %d is over MaxListed %d", n, MaxListed)
	}
	s, d, stats := withStored(appleSite(t, n, fmt.Sprint(n)), nil)
	got, err := s.List(context.Background(), board("a"))
	if err != nil {
		t.Fatalf("a board over the old cap failed: %v", err)
	}
	st := (*stats)[0]
	if st.Listed != n || st.Fetched != MaxDetails || st.Deferred != n-MaxDetails || len(got) != MaxDetails || detailRequests(d) != MaxDetails || !accounted(st) {
		t.Fatalf("stats %+v, %d postings, %d detail requests", st, len(got), detailRequests(d))
	}
}

// Stored positions are returned whatever is deferred, and a position whose
// refresh turn is not due costs no request.
func TestStoredPositionsAreReusedAndNeverDropped(t *testing.T) {
	const stored = 60
	n := stored + MaxDetails + 20
	var have []posting.Posting
	for i := range stored {
		key, err := posting.AppleKey("apple", appleID(i))
		if err != nil {
			t.Fatal(err)
		}
		have = append(have, posting.Posting{Key: key, ATS: posting.Apple, Company: "Apple", Title: "Stored", Description: "stored text", SeenAt: fixedNow.Add(-48 * time.Hour)})
	}
	s, d, stats := withStored(appleSite(t, n, fmt.Sprint(n)), have)
	got, err := s.List(context.Background(), board("a"))
	if err != nil {
		t.Fatal(err)
	}
	st := (*stats)[0]
	if st.Fetched != MaxDetails || st.Deferred != 20 || st.Reused+st.Refreshed != stored || !accounted(st) {
		t.Fatalf("stats %+v", st)
	}
	if want := MaxDetails + st.Refreshed; detailRequests(d) != want {
		t.Errorf("%d detail requests, want %d (new ones, and stored ones on their turn)", detailRequests(d), want)
	}
	returned := map[posting.Key]bool{}
	for _, p := range got {
		returned[p.Key] = true
	}
	for _, p := range have {
		if !returned[p.Key] {
			t.Fatalf("stored position %s was left out", p.Key)
		}
	}
}

// The bounds: without the stored postings the 200 cap stands (a deferred
// position could not be told from a stored one left out); with them the board
// is listed to MaxListed, and an inexact count ("600+") or one over it still
// fails before any detail.
func TestBoardCaps(t *testing.T) {
	for _, tc := range []struct {
		name   string
		n      int
		count  string
		stored bool
		want   bool // fails with ErrTooManyPostings
	}{
		{"no stored, at the old cap", MaxPostings, fmt.Sprint(MaxPostings), false, false},
		{"no stored, over the old cap", MaxPostings + 1, fmt.Sprint(MaxPostings + 1), false, true},
		{"stored, at MaxListed", MaxListed, fmt.Sprint(MaxListed), true, false},
		{"stored, over MaxListed", MaxListed + 1, fmt.Sprint(MaxListed + 1), true, true},
		{"stored, an inexact count", 20, "600+", true, true},
	} {
		rows := tc.n
		if tc.want {
			rows = 40 // the count decides, and is refused on the first page
		}
		s, d := newSource(appleSite(t, rows, tc.count))
		if tc.stored {
			s.opt = &Options{Stored: func(sources.Board) []posting.Posting { return nil }}
		}
		_, err := s.List(context.Background(), board("a"))
		if got := errors.Is(err, srcutil.ErrTooManyPostings); got != tc.want || (err != nil) != tc.want {
			t.Errorf("%s: %v", tc.name, err)
		}
		if tc.want && detailRequests(d) != 0 {
			t.Errorf("%s: %d details asked for a board over its cap", tc.name, detailRequests(d))
		}
	}
}

// A listing that cannot reach its count fails closed: the pages run out short
// of it, or the run ends in the middle of the paging. Nothing partial is
// returned.
func TestLargeListingFailsClosed(t *testing.T) {
	t.Run("the pages run out short of the count", func(t *testing.T) {
		pages := appleSite(t, 130, "260") // 6 full pages and one of 10, then none
		pages[pageURL("a", nil, 8)] = searchPage("260")
		s, _, _ := withStored(pages, nil)
		ps, err := s.List(context.Background(), board("a"))
		if !errors.Is(err, ErrShape) || ps != nil {
			t.Fatalf("got %d postings, err %v; want ErrShape", len(ps), err)
		}
	})
	t.Run("the run ends in the middle of the paging", func(t *testing.T) {
		pages := appleSite(t, 130, "130")
		s, _, _ := withStored(pages, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		searches := 0
		s.d = srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
			if strings.Contains(r.URL, "/search") {
				if searches++; searches == 3 {
					cancel()
				}
			}
			body, ok := pages[r.URL]
			if !ok {
				return srctest.Reply{Status: 404}, true
			}
			return srctest.Reply{Status: 200, Body: []byte(body)}, true
		})
		ps, err := s.List(ctx, board("a"))
		if !errors.Is(err, context.Canceled) || ps != nil {
			t.Fatalf("got %d postings, err %v; want the cancellation", len(ps), err)
		}
	})
	t.Run("an empty search lists nothing", func(t *testing.T) {
		s, _, stats := withStored(map[string]string{pageURL("a", nil, 1): searchPage("0")}, nil)
		ps, err := s.List(context.Background(), board("a"))
		if err != nil || len(ps) != 0 || (*stats)[0].Listed != 0 {
			t.Fatalf("got %d postings, err %v", len(ps), err)
		}
	})
}
