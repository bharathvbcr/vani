package eightfold

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// sized is a pcsx site whose search serves the ids it has but reports count as
// its total, with the bounded detail step on (stored are the postings an
// earlier run stored; stats collects each report).
func sized(t *testing.T, ids []string, count int, stored []posting.Posting) (*source, *site, *[]Stats) {
	t.Helper()
	f := fixtureSite(t)
	f.positions = map[string][]string{"t": ids}
	f.count = map[string]int{"t": count}
	var stats []Stats
	s := &source{d: srctest.NewFunc(f.handle), now: func() time.Time { return fixedNow }, wait: noWait,
		opt: &Options{Stored: func(sources.Board) []posting.Posting { return stored },
			Report: func(_ sources.Board, st Stats) { stats = append(stats, st) }}}
	return s, f, &stats
}

func ids(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = id(i)
	}
	return out
}

// A board over the old 200-posting cap is listed, page by page to its count,
// and its new positions are fetched only up to MaxDetails a run: the rest are
// deferred (not returned, so nothing stored is lost) and fetched on later runs.
func TestLargeBoardIsListedAndNewDetailsAreBounded(t *testing.T) {
	n := 200 + MaxDetails
	if n > MaxPostings {
		t.Fatalf("fixture: %d positions is over MaxPostings %d", n, MaxPostings)
	}
	s, _, stats := sized(t, ids(n), n, nil)
	asked := detailCounter(s)
	got, err := s.List(context.Background(), board("t"))
	if err != nil {
		t.Fatalf("a board over the old cap failed: %v", err)
	}
	st := (*stats)[0]
	if st.Listed != n || st.Fetched != MaxDetails || st.Deferred != n-MaxDetails || len(*asked) != MaxDetails || len(got) != MaxDetails {
		t.Fatalf("stats %+v, %d details asked, %d postings returned; want %d listed, %d fetched, %d deferred", st, len(*asked), len(got), n, MaxDetails, n-MaxDetails)
	}
	if !accounted(st) {
		t.Errorf("every listed position must be counted once: %+v", st)
	}
}

// Stored positions are always returned, whatever is deferred: a deferred new
// position is not stored, so its absence ages nothing, but a stored one left
// out would die.
func TestDeferralNeverDropsStoredPositions(t *testing.T) {
	const stored = 120
	n := stored + MaxDetails + 30
	var have []posting.Posting
	for i := range stored {
		have = append(have, storedPosting(t, "microsoft.com", id(i)))
	}
	s, _, stats := sized(t, ids(n), n, have)
	got, err := s.List(context.Background(), board("t"))
	if err != nil {
		t.Fatal(err)
	}
	st := (*stats)[0]
	if st.Deferred != 30 || st.Fetched != MaxDetails {
		t.Fatalf("stats %+v, want %d fetched and 30 deferred", st, MaxDetails)
	}
	returned := map[string]bool{}
	for _, p := range got {
		returned[string(p.Key)] = true
	}
	for i := range stored {
		if k := "ef:microsoft.com:" + id(i); !returned[k] {
			t.Fatalf("stored position %s was left out of the listing", k)
		}
	}
	if !accounted(st) {
		t.Errorf("unaccounted: %+v", st)
	}
}

// Without the stored postings a deferred position cannot be told from a stored
// one left out, so a board that would need deferral fails rather than lists
// partially. Exactly MaxDetails positions fit.
func TestNoDeferralWithoutStoredPostings(t *testing.T) {
	for _, tc := range []struct {
		n       int
		wantErr bool
	}{{MaxDetails, false}, {MaxDetails + 1, true}} {
		s := goneSite(t, tc.n, nil) // no Options: Stored is nil
		_, err := s.List(context.Background(), board("t"))
		if got := errors.Is(err, srcutil.ErrTooManyPostings); got != tc.wantErr || (err != nil) != tc.wantErr {
			t.Errorf("%d positions without Stored: %v", tc.n, err)
		}
	}
}

// The count is still bounded: over MaxPostings fails the board before any
// detail, as it did over 200.
func TestBoardOverMaxPostingsStillFails(t *testing.T) {
	s, _, _ := sized(t, ids(20), MaxPostings+1, nil)
	asked := detailCounter(s)
	if _, err := s.List(context.Background(), board("t")); !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("over the cap: %v", err)
	}
	if len(*asked) != 0 {
		t.Errorf("%d details asked for a board over the cap", len(*asked))
	}
}

// A listing that cannot reach its count fails closed whatever the cause: the
// server stops serving rows partway, serves the same rows again on a later
// page, or the run ends in the middle of the paging. No partial listing is
// returned, so nothing ages.
func TestLargeListingFailsClosed(t *testing.T) {
	t.Run("the server runs out of rows short of its count", func(t *testing.T) {
		s, _, _ := sized(t, ids(135), 260, nil) // 13 full pages, a page of 5, then none
		ps, err := s.List(context.Background(), board("t"))
		if !errors.Is(err, ErrShape) || ps != nil {
			t.Fatalf("got %d postings, err %v; want ErrShape and no postings", len(ps), err)
		}
	})
	t.Run("the same rows again on a later page", func(t *testing.T) {
		// 25 ids with 5 repeated: 20 distinct against a count of 25.
		dup := append(ids(20), id(3), id(4), id(5), id(6), id(7))
		s, _, _ := sized(t, dup, 25, nil)
		if ps, err := s.List(context.Background(), board("t")); !errors.Is(err, ErrShape) || ps != nil {
			t.Fatalf("got %d postings, err %v; want ErrShape", len(ps), err)
		}
	})
	t.Run("the run ends in the middle of the paging", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s, f, _ := sized(t, ids(130), 130, nil)
		searches := 0
		s.d = srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
			if u, err := url.Parse(r.URL); err == nil && u.Path == "/api/pcsx/search" {
				if searches++; searches == 4 {
					cancel()
				}
			}
			return f.handle(r)
		})
		ps, err := s.List(ctx, board("t"))
		if !errors.Is(err, context.Canceled) || ps != nil {
			t.Fatalf("got %d postings, err %v; want the cancellation", len(ps), err)
		}
	})
	t.Run("a total that is missing", func(t *testing.T) {
		s, f, _ := sized(t, ids(12), 12, nil)
		s.d = srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
			rep, ok := f.handle(r)
			if u, err := url.Parse(r.URL); err == nil && u.Path == "/api/pcsx/search" {
				rep.Body = []byte(`{"status":200,"error":{"message":"","body":""},"data":{"positions":[]}}`)
			}
			return rep, ok
		})
		if ps, err := s.List(context.Background(), board("t")); !errors.Is(err, ErrShape) || ps != nil {
			t.Fatalf("got %d postings, err %v; want ErrShape", len(ps), err)
		}
	})
	t.Run("an empty board lists nothing and is not an error", func(t *testing.T) {
		s, _, stats := sized(t, nil, 0, nil)
		ps, err := s.List(context.Background(), board("t"))
		if err != nil || len(ps) != 0 || (*stats)[0].Listed != 0 {
			t.Fatalf("got %d postings, err %v", len(ps), err)
		}
	})
}
